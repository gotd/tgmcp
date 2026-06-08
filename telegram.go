package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// isChannelGone reports whether err means the channel is no longer accessible:
// we were kicked or banned, or it went private. The cached entry should be
// dropped in that case.
func isChannelGone(err error) bool {
	return tgerr.Is(err, "CHANNEL_PRIVATE")
}

// Local aliases to keep function signatures compact.
type (
	dialogElem = dialogs.Elem
	entities   = peer.Entities
)

// UnreadChannel describes a channel (or supergroup) that has unread messages.
type UnreadChannel struct {
	ID          int64  `json:"id" jsonschema:"Telegram channel ID"`
	Title       string `json:"title" jsonschema:"channel title"`
	Username    string `json:"username,omitempty" jsonschema:"public @username, if any"`
	UnreadCount int    `json:"unread_count" jsonschema:"number of unread messages"`
	UnreadMark  bool   `json:"unread_mark,omitempty" jsonschema:"true if manually marked as unread"`
	Broadcast   bool   `json:"broadcast" jsonschema:"true for broadcast channels"`
	Megagroup   bool   `json:"megagroup" jsonschema:"true for supergroups"`

	// readInboxMaxID is the ID of the last message marked as read. Messages with
	// a greater ID are unread. Kept unexported: it is an implementation detail.
	readInboxMaxID int
	peer           tg.InputPeerClass
}

// Message is a single message returned to the MCP client.
type Message struct {
	ID     int    `json:"id" jsonschema:"message ID"`
	Date   string `json:"date" jsonschema:"send time in RFC3339"`
	Text   string `json:"text" jsonschema:"message text"`
	Author string `json:"author,omitempty" jsonschema:"sender name, for groups"`
}

// bootstrapDialogs loads the full dialog list once and seeds the cache. It
// fetches dialogs in batches of MAX_GET_DIALOGS (100, the server-side limit)
// rather than one at a time, which is the default of the gotd iterator.
func bootstrapDialogs(ctx context.Context, api *tg.Client, cache *dialogCache) error {
	var result []UnreadChannel

	iter := query.GetDialogs(api).BatchSize(100).Iter()
	for iter.Next(ctx) {
		ch, ok := channelFromDialog(iter.Value())
		if !ok {
			continue
		}
		result = append(result, ch)
	}
	if err := iter.Err(); err != nil {
		return errors.Wrap(err, "iterate dialogs")
	}

	cache.replaceAll(result)

	return nil
}

// refreshChannel refetches a single channel's dialog and replaces its cached
// entry. It is called when the updates manager reports a channel difference too
// long to recover incrementally (OnChannelTooLong): the manager advances pts
// but the intermediate updates are lost, so the unread count must be resynced.
func refreshChannel(ctx context.Context, api *tg.Client, cache *dialogCache, channelID int64) error {
	ch, ok := cache.get(channelID)
	if !ok {
		// Unknown channel: nothing cached to refresh.
		return nil
	}
	ipc, ok := ch.peer.(*tg.InputPeerChannel)
	if !ok {
		return errors.Errorf("channel %d has no input peer", channelID)
	}

	res, err := api.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{
		&tg.InputDialogPeer{Peer: ipc},
	})
	if err != nil {
		if isChannelGone(err) {
			cache.remove(channelID)

			return nil
		}

		return errors.Wrap(err, "get peer dialogs")
	}

	ent := peer.EntitiesFromResult(res)
	for _, dlg := range res.Dialogs {
		refreshed, ok := channelFromDialog(dialogElem{Dialog: dlg, Peer: ipc, Entities: ent})
		if ok && refreshed.ID == channelID {
			cache.set(refreshed)
			return nil
		}
	}

	return nil
}

// readUnread returns the unread messages of a channel, newest first, capped at
// limit. A non-positive limit defaults to 50.
func readUnread(ctx context.Context, api *tg.Client, cache *dialogCache, target string, limit int) (UnreadChannel, []Message, error) {
	if limit <= 0 {
		limit = 50
	}

	ch, ok := cache.find(target)
	if !ok {
		return UnreadChannel{}, nil, errors.Errorf("channel %q not found in dialogs", target)
	}

	var out []Message
	iter := messages.NewQueryBuilder(api).GetHistory(ch.peer).Iter()
	for iter.Next(ctx) {
		msg, ok := iter.Value().Msg.(*tg.Message)
		if !ok {
			// Skip service messages and the like.
			continue
		}
		// History is returned newest-first; once we reach a message that is
		// already read, everything after it is read too.
		if msg.ID <= ch.readInboxMaxID {
			break
		}
		out = append(out, messageFromTG(msg, iter.Value().Entities))
		if len(out) >= limit {
			break
		}
	}
	if err := iter.Err(); err != nil {
		if isChannelGone(err) {
			cache.remove(ch.ID)

			return UnreadChannel{}, nil, errors.Errorf("channel %q is no longer accessible", target)
		}

		return UnreadChannel{}, nil, errors.Wrap(err, "fetch history")
	}

	return ch, out, nil
}

// channelFromDialog extracts channel information from a dialog element,
// regardless of unread state. Returns false for non-channel dialogs.
func channelFromDialog(elem dialogElem) (UnreadChannel, bool) {
	dlg, ok := elem.Dialog.(*tg.Dialog)
	if !ok {
		return UnreadChannel{}, false
	}
	pc, ok := dlg.Peer.(*tg.PeerChannel)
	if !ok {
		return UnreadChannel{}, false
	}
	c, ok := elem.Entities.Channel(pc.ChannelID)
	if !ok {
		return UnreadChannel{}, false
	}

	username, _ := c.GetUsername()
	return UnreadChannel{
		ID:             c.ID,
		Title:          c.Title,
		Username:       username,
		UnreadCount:    dlg.UnreadCount,
		UnreadMark:     dlg.UnreadMark,
		Broadcast:      c.Broadcast,
		Megagroup:      c.Megagroup,
		readInboxMaxID: dlg.ReadInboxMaxID,
		peer:           elem.Peer,
	}, true
}

// channelFromEntity builds an UnreadChannel from a channel object received in
// update entities, when the dialog is not yet cached.
func channelFromEntity(c *tg.Channel) UnreadChannel {
	username, _ := c.GetUsername()
	accessHash, _ := c.GetAccessHash()
	return UnreadChannel{
		ID:        c.ID,
		Title:     c.Title,
		Username:  username,
		Broadcast: c.Broadcast,
		Megagroup: c.Megagroup,
		peer: &tg.InputPeerChannel{
			ChannelID:  c.ID,
			AccessHash: accessHash,
		},
	}
}

func messageFromTG(msg *tg.Message, ent entities) Message {
	return Message{
		ID:     msg.ID,
		Date:   time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
		Text:   msg.Message,
		Author: authorName(msg, ent),
	}
}

// authorName resolves a human-readable sender name from a message, when the
// sender is a user present in the entities.
func authorName(msg *tg.Message, ent entities) string {
	from, ok := msg.GetFromID()
	if !ok {
		return ""
	}
	pu, ok := from.(*tg.PeerUser)
	if !ok {
		return ""
	}
	u, ok := ent.User(pu.UserID)
	if !ok {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name, _ = u.GetUsername()
	}
	return name
}

// markChannelRead marks all messages in a channel as read up to and including
// the latest message (MaxID=0 means "all messages").
func markChannelRead(ctx context.Context, api *tg.Client, cache *dialogCache, ch UnreadChannel) error {
	ipc, ok := ch.peer.(*tg.InputPeerChannel)
	if !ok {
		return errors.Errorf("peer for channel %d is not an InputPeerChannel", ch.ID)
	}
	_, err := api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
		Channel: &tg.InputChannel{
			ChannelID:  ipc.ChannelID,
			AccessHash: ipc.AccessHash,
		},
		MaxID: 0, // 0 = mark everything as read
	})
	if err != nil {
		if isChannelGone(err) {
			cache.remove(ch.ID)

			return nil
		}

		return errors.Wrap(err, "channels.readHistory")
	}

	cache.markRead(ch.ID)

	return nil
}

// markAllChannelsRead marks every unread channel as read and returns how many
// channels were marked.
func markAllChannelsRead(ctx context.Context, api *tg.Client, cache *dialogCache) (int, error) {
	channels := cache.unread()
	for _, ch := range channels {
		if err := markChannelRead(ctx, api, cache, ch); err != nil {
			return 0, errors.Wrapf(err, "mark channel %d (%s) as read", ch.ID, ch.Title)
		}
	}

	return len(channels), nil
}

// registerCacheHandlers wires the update handlers that keep the dialog cache's
// unread counts live, mirroring how tdlib maintains them from the update stream.
func registerCacheHandlers(d *tg.UpdateDispatcher, cache *dialogCache) {
	d.OnNewChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			// Service messages and the like do not count as unread here.
			return nil
		}
		pc, ok := msg.PeerID.(*tg.PeerChannel)
		if !ok {
			return nil
		}
		if msg.Out {
			// Our own message: not unread.
			return nil
		}

		id := pc.ChannelID
		cache.observeIncoming(id, func() (UnreadChannel, bool) {
			c, ok := e.Channels[id]
			if !ok {
				return UnreadChannel{}, false
			}
			return channelFromEntity(c), true
		})

		return nil
	})

	d.OnReadChannelInbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadChannelInbox) error {
		cache.setRead(u.ChannelID, u.MaxID, u.StillUnreadCount)

		return nil
	})

	d.OnDialogUnreadMark(func(_ context.Context, _ tg.Entities, u *tg.UpdateDialogUnreadMark) error {
		peer, ok := u.Peer.(*tg.DialogPeer)
		if !ok {
			return nil
		}
		pc, ok := peer.Peer.(*tg.PeerChannel)
		if !ok {
			return nil
		}
		cache.setUnreadMark(pc.ChannelID, u.Unread)

		return nil
	})
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
