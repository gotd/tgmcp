package main

import (
	"strings"
	"sync"

	"go.uber.org/zap"

	"github.com/gotd/td/tg"
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
	channels map[string]UnreadChannel

	store *dialogStore // optional; nil disables persistence.
	lg    *zap.Logger
}

func newDialogCache(store *dialogStore, lg *zap.Logger) *dialogCache {
	return &dialogCache{
		channels: make(map[string]UnreadChannel),
		store:    store,
		lg:       lg,
	}
}

func dialogCacheKey(ch UnreadChannel) string {
	return dialogKey(ch)
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

	m := make(map[string]UnreadChannel, len(chs))
	for _, ch := range chs {
		m[dialogCacheKey(ch)] = ch
	}

	c.mu.Lock()
	c.channels = m
	c.mu.Unlock()

	return len(m), nil
}

// replaceAll swaps the entire cache content and persists it. Used by the
// one-time full fetch.
func (c *dialogCache) replaceAll(chs []UnreadChannel) {
	m := make(map[string]UnreadChannel, len(chs))
	for _, ch := range chs {
		m[dialogCacheKey(ch)] = ch
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

// unread returns the cached broadcast channels that currently have unread
// messages or are manually marked as unread. Non-broadcast dialogs are
// excluded to preserve legacy behavior for list/mark tools.
func (c *dialogCache) unread() []UnreadChannel {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var out []UnreadChannel
	for _, ch := range c.channels {
		if !ch.broadcast() {
			continue
		}
		if ch.UnreadCount > 0 || ch.UnreadMark {
			out = append(out, ch)
		}
	}

	return out
}

// all returns every cached dialog regardless of type or unread state.
func (c *dialogCache) all() []UnreadChannel {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var out []UnreadChannel
	for _, ch := range c.channels {
		out = append(out, ch)
	}

	return out
}

func (c *dialogCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.channels)
}

// find resolves a cached channel by numeric ID or @username.
func (c *dialogCache) find(target string) (UnreadChannel, bool) {
	target = strings.TrimPrefix(strings.TrimSpace(target), "@")
	wantID, isID := parseID(target)
	if isID {
		return c.get(wantID)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, ch := range c.channels {
		if strings.EqualFold(ch.Username, target) {
			return ch, true
		}
	}

	return UnreadChannel{}, false
}

// get returns a cached channel by ID.
func (c *dialogCache) get(id int64) (UnreadChannel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, kind := range []string{"channel", "chat", "user"} {
		ch, ok := c.channels[dialogKeyParts(kind, id)]
		if ok {
			return ch, true
		}
	}

	return UnreadChannel{}, false
}

// getPeer returns a cached dialog matching the exact input peer namespace.
func (c *dialogCache) getPeer(p any) (UnreadChannel, bool) {
	key, ok := peerCacheKey(p)
	if !ok {
		return UnreadChannel{}, false
	}

	return c.getKey(key)
}

// getKey returns the cached dialog stored under key.
func (c *dialogCache) getKey(key string) (UnreadChannel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ch, ok := c.channels[key]
	return ch, ok
}

// peerCacheKey maps an input peer to its cache key. Reports false for peers
// that have no stable key, such as InputPeerSelf.
func peerCacheKey(p any) (string, bool) {
	switch v := p.(type) {
	case *tg.InputPeerChannel:
		return dialogKeyParts("channel", v.ChannelID), true
	case *tg.InputPeerChat:
		return dialogKeyParts("chat", v.ChatID), true
	case *tg.InputPeerUser:
		return dialogKeyParts("user", v.UserID), true
	default:
		return "", false
	}
}

// peerKey maps a peer to its cache key. Peers of a kind the cache does not
// hold, such as a secret chat, report false.
func peerKey(p tg.PeerClass) (string, bool) {
	switch v := p.(type) {
	case *tg.PeerChannel:
		return dialogKeyParts("channel", v.ChannelID), true
	case *tg.PeerChat:
		return dialogKeyParts("chat", v.ChatID), true
	case *tg.PeerUser:
		return dialogKeyParts("user", v.UserID), true
	default:
		return "", false
	}
}

// set upserts a fully-resolved channel and persists it. Used to resync a single
// channel after a too-long difference.
func (c *dialogCache) set(ch UnreadChannel) {
	c.mu.Lock()
	c.channels[dialogCacheKey(ch)] = ch
	c.mu.Unlock()

	c.persist(ch)
}

// learn upserts the metadata an update revealed about a channel and reports
// whether anything changed. Counters are left alone: they belong to the
// handlers that track reads and incoming messages, and a rename says nothing
// about them.
//
// The comparison holds the same lock as the write. Reading the entry first and
// writing it back after would let a concurrent unread increment land in between
// and be overwritten, and would let two updates for one channel both decide it
// was unknown.
//
// Returning early on an unchanged entry is what makes this safe to call from
// updateChannel, which fires on any change to any channel: without it every
// update would write a dialog to disk.
func (c *dialogCache) learn(fresh UnreadChannel) bool {
	key := dialogCacheKey(fresh)

	c.mu.Lock()
	old, known := c.channels[key]
	if known && sameChannelMeta(old, fresh) {
		c.mu.Unlock()

		return false
	}
	if known {
		old.Title = fresh.Title
		old.Username = fresh.Username
		old.Type = fresh.Type
		old.peer = fresh.peer
		fresh = old
	}
	c.channels[key] = fresh
	c.mu.Unlock()

	// Outside the lock: the store writes to disk, and the map is the
	// authoritative copy either way.
	c.persist(fresh)

	return true
}

// sameChannelMeta reports whether an update carries nothing new. The access
// hash counts: it addresses the chat, so a rotation matters even when the
// visible metadata is identical.
func sameChannelMeta(a, b UnreadChannel) bool {
	if a.Title != b.Title || a.Username != b.Username || a.Type != b.Type {
		return false
	}
	ap, aok := a.peer.(*tg.InputPeerChannel)
	bp, bok := b.peer.(*tg.InputPeerChannel)
	if aok && bok {
		return ap.AccessHash == bp.AccessHash
	}

	return aok == bok
}

// remove drops a channel from the cache and the store. Used when the channel is
// no longer accessible (e.g. CHANNEL_PRIVATE: we were kicked, banned, or it went
// private).
func (c *dialogCache) remove(channelID int64) {
	c.mu.Lock()
	var key string
	for _, kind := range []string{"channel", "chat", "user"} {
		k := dialogKeyParts(kind, channelID)
		if _, ok := c.channels[k]; ok {
			key = k
			break
		}
	}
	if key == "" {
		c.mu.Unlock()
		return
	}
	delete(c.channels, key)
	c.mu.Unlock()

	if c.store == nil {
		return
	}

	if err := c.store.delete(channelID); err != nil {
		c.lg.Error("Delete dialog", zap.Int64("id", channelID), zap.Error(err))
	}
}

// observeIncoming records an incoming message in a channel. If the channel is
// already cached its unread count is incremented; otherwise build is called to
// resolve channel metadata (from update entities) and the channel is inserted
// with a single unread message. build may return false when the channel cannot
// be resolved, in which case the message is dropped.
func (c *dialogCache) observeIncoming(p tg.PeerClass, build func() (UnreadChannel, bool)) {
	key, ok := peerKey(p)
	if !ok {
		return
	}

	c.mu.Lock()
	ch, ok := c.channels[key]
	if ok {
		ch.UnreadCount++
		c.channels[key] = ch
	} else if nch, built := build(); built {
		nch.UnreadCount = 1
		ch, ok = nch, true
		c.channels[dialogCacheKey(nch)] = nch
	}
	c.mu.Unlock()

	if ok {
		c.persist(ch)
	}
}

// setRead applies a read-inbox update: messages up to maxID are read and
// stillUnread messages remain. Unknown channels are ignored.
func (c *dialogCache) setRead(p tg.PeerClass, maxID, stillUnread int) {
	c.updatePeer(p, func(ch *UnreadChannel) {
		ch.readInboxMaxID = maxID
		if stillUnread >= 0 {
			ch.UnreadCount = stillUnread
		}
		ch.UnreadMark = false
	})
}

// setUnreadMark applies a manual unread mark toggle. Unknown channels are
// ignored.
func (c *dialogCache) setUnreadMark(p tg.PeerClass, mark bool) {
	c.updatePeer(p, func(ch *UnreadChannel) {
		ch.UnreadMark = mark
	})
}

// markReadPeer clears the unread state of a dialog after we mark it read
// locally. Peers that are not cached dialogs are ignored.
func (c *dialogCache) markReadPeer(p any) {
	key, ok := peerCacheKey(p)
	if !ok {
		return
	}

	c.updateKey(key, func(ch *UnreadChannel) {
		ch.UnreadCount = 0
		ch.UnreadMark = false
	})
}

// updatePeer applies mutate to the cached dialog of a peer and persists the
// result. Unknown dialogs are ignored.
func (c *dialogCache) updatePeer(p tg.PeerClass, mutate func(*UnreadChannel)) {
	key, ok := peerKey(p)
	if !ok {
		return
	}

	c.updateKey(key, mutate)
}

// updateKey applies mutate to the dialog stored under key and persists the
// result. Unknown keys are ignored.
func (c *dialogCache) updateKey(key string, mutate func(*UnreadChannel)) {
	c.mu.Lock()
	ch, ok := c.channels[key]
	if ok {
		mutate(&ch)
		c.channels[key] = ch
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
