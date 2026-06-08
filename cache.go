package main

import (
	"strings"
	"sync"

	"go.uber.org/zap"
)

// dialogCache is an in-memory snapshot of the user's channel and supergroup
// dialogs. It is seeded at startup (from persistent storage, or a one-time full
// fetch on first run) and then kept live by the Telegram updates stream.
//
// This replaces the previous approach of re-fetching the entire dialog list on
// every tool call, which fired one messages.getDialogs RPC per dialog and
// triggered FLOOD_WAIT. It mirrors how tdlib maintains per-dialog unread counts:
// load once, then mutate in place as updates arrive.
//
// Mutations are written through to the store so the cache survives restarts;
// the updates manager then reconciles it via getDifference.
type dialogCache struct {
	mu       sync.RWMutex
	channels map[int64]UnreadChannel

	store *dialogStore // optional; nil disables persistence.
	lg    *zap.Logger
}

func newDialogCache(store *dialogStore, lg *zap.Logger) *dialogCache {
	return &dialogCache{
		channels: make(map[int64]UnreadChannel),
		store:    store,
		lg:       lg,
	}
}

// loadFromStore replaces the in-memory cache with the persisted dialogs and
// returns how many were loaded. Returns 0 when no store is configured or none
// are persisted yet.
func (c *dialogCache) loadFromStore() (int, error) {
	if c.store == nil {
		return 0, nil
	}

	chs, err := c.store.load()
	if err != nil {
		return 0, err
	}

	m := make(map[int64]UnreadChannel, len(chs))
	for _, ch := range chs {
		m[ch.ID] = ch
	}

	c.mu.Lock()
	c.channels = m
	c.mu.Unlock()

	return len(chs), nil
}

// replaceAll swaps the entire cache content and persists it. Used by the
// one-time full fetch.
func (c *dialogCache) replaceAll(chs []UnreadChannel) {
	m := make(map[int64]UnreadChannel, len(chs))
	for _, ch := range chs {
		m[ch.ID] = ch
	}

	c.mu.Lock()
	c.channels = m
	c.mu.Unlock()

	if c.store != nil {
		if err := c.store.putAll(chs); err != nil {
			c.lg.Error("Persist dialogs", zap.Error(err))
		}
	}
}

// unread returns the cached channels that currently have unread messages or are
// manually marked as unread.
func (c *dialogCache) unread() []UnreadChannel {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var out []UnreadChannel
	for _, ch := range c.channels {
		if ch.UnreadCount > 0 || ch.UnreadMark {
			out = append(out, ch)
		}
	}

	return out
}

// find resolves a cached channel by numeric ID or @username.
func (c *dialogCache) find(target string) (UnreadChannel, bool) {
	target = strings.TrimPrefix(strings.TrimSpace(target), "@")
	wantID, isID := parseID(target)

	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, ch := range c.channels {
		if isID && ch.ID == wantID {
			return ch, true
		}
		if !isID && strings.EqualFold(ch.Username, target) {
			return ch, true
		}
	}

	return UnreadChannel{}, false
}

// get returns a cached channel by ID.
func (c *dialogCache) get(id int64) (UnreadChannel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ch, ok := c.channels[id]

	return ch, ok
}

// set upserts a fully-resolved channel and persists it. Used to resync a single
// channel after a too-long difference.
func (c *dialogCache) set(ch UnreadChannel) {
	c.mu.Lock()
	c.channels[ch.ID] = ch
	c.mu.Unlock()

	c.persist(ch)
}

// observeIncoming records an incoming message in a channel. If the channel is
// already cached its unread count is incremented; otherwise build is called to
// resolve channel metadata (from update entities) and the channel is inserted
// with a single unread message. build may return false when the channel cannot
// be resolved, in which case the message is dropped.
func (c *dialogCache) observeIncoming(channelID int64, build func() (UnreadChannel, bool)) {
	c.mu.Lock()
	ch, ok := c.channels[channelID]
	if ok {
		ch.UnreadCount++
		c.channels[channelID] = ch
	} else if nch, built := build(); built {
		nch.UnreadCount = 1
		ch, ok = nch, true
		c.channels[channelID] = nch
	}
	c.mu.Unlock()

	if ok {
		c.persist(ch)
	}
}

// setRead applies a read-inbox update: messages up to maxID are read and
// stillUnread messages remain. Unknown channels are ignored.
func (c *dialogCache) setRead(channelID int64, maxID, stillUnread int) {
	c.update(channelID, func(ch *UnreadChannel) {
		ch.readInboxMaxID = maxID
		if stillUnread >= 0 {
			ch.UnreadCount = stillUnread
		}
		ch.UnreadMark = false
	})
}

// setUnreadMark applies a manual unread mark toggle. Unknown channels are
// ignored.
func (c *dialogCache) setUnreadMark(channelID int64, mark bool) {
	c.update(channelID, func(ch *UnreadChannel) {
		ch.UnreadMark = mark
	})
}

// markRead clears the unread state of a channel after we mark it read locally.
func (c *dialogCache) markRead(channelID int64) {
	c.update(channelID, func(ch *UnreadChannel) {
		ch.UnreadCount = 0
		ch.UnreadMark = false
	})
}

// update applies mutate to a cached channel under lock and persists the result.
// Unknown channels are ignored.
func (c *dialogCache) update(channelID int64, mutate func(*UnreadChannel)) {
	c.mu.Lock()
	ch, ok := c.channels[channelID]
	if ok {
		mutate(&ch)
		c.channels[channelID] = ch
	}
	c.mu.Unlock()

	if ok {
		c.persist(ch)
	}
}

// persist write-throughs a single channel to the store, logging any error.
func (c *dialogCache) persist(ch UnreadChannel) {
	if c.store == nil {
		return
	}
	if err := c.store.put(ch); err != nil {
		c.lg.Error("Persist dialog", zap.Int64("id", ch.ID), zap.Error(err))
	}
}
