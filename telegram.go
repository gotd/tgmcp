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
)

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

// listUnreadChannels iterates over all dialogs and returns the channels and
// supergroups that currently have unread messages.
func listUnreadChannels(ctx context.Context, api *tg.Client) ([]UnreadChannel, error) {
	var result []UnreadChannel

	iter := query.GetDialogs(api).Iter()
	for iter.Next(ctx) {
		ch, ok := unreadChannelFromDialog(iter.Value())
		if !ok {
			continue
		}
		result = append(result, ch)
	}
	if err := iter.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate dialogs")
	}

	return result, nil
}

// findChannel resolves a channel by @username or numeric ID by scanning the
// dialog list. Scanning the dialogs (rather than resolving directly) gives us
// the unread boundary and access hash in a single pass.
func findChannel(ctx context.Context, api *tg.Client, target string) (UnreadChannel, error) {
	target = strings.TrimPrefix(strings.TrimSpace(target), "@")
	wantID, isID := parseID(target)

	iter := query.GetDialogs(api).Iter()
	for iter.Next(ctx) {
		ch, ok := channelFromDialog(iter.Value())
		if !ok {
			continue
		}
		if isID && ch.ID == wantID {
			return ch, nil
		}
		if !isID && strings.EqualFold(ch.Username, target) {
			return ch, nil
		}
	}
	if err := iter.Err(); err != nil {
		return UnreadChannel{}, errors.Wrap(err, "iterate dialogs")
	}

	return UnreadChannel{}, errors.Errorf("channel %q not found in dialogs", target)
}

// readUnread returns the unread messages of a channel, newest first, capped at
// limit. A non-positive limit defaults to 50.
func readUnread(ctx context.Context, api *tg.Client, target string, limit int) (UnreadChannel, []Message, error) {
	if limit <= 0 {
		limit = 50
	}

	ch, err := findChannel(ctx, api, target)
	if err != nil {
		return UnreadChannel{}, nil, err
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
		Broadcast:      c.Broadcast,
		Megagroup:      c.Megagroup,
		readInboxMaxID: dlg.ReadInboxMaxID,
		peer:           elem.Peer,
	}, true
}

// unreadChannelFromDialog is like channelFromDialog but only returns channels
// that have unread messages.
func unreadChannelFromDialog(elem dialogElem) (UnreadChannel, bool) {
	ch, ok := channelFromDialog(elem)
	if !ok || ch.UnreadCount <= 0 {
		return UnreadChannel{}, false
	}
	return ch, true
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
func markChannelRead(ctx context.Context, api *tg.Client, ch UnreadChannel) error {
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
	return errors.Wrap(err, "channels.readHistory")
}

// markAllChannelsRead marks every unread channel as read and returns how many
// channels were marked.
func markAllChannelsRead(ctx context.Context, api *tg.Client) (int, error) {
	channels, err := listUnreadChannels(ctx, api)
	if err != nil {
		return 0, errors.Wrap(err, "list unread channels")
	}
	for _, ch := range channels {
		if err := markChannelRead(ctx, api, ch); err != nil {
			return 0, errors.Wrapf(err, "mark channel %d (%s) as read", ch.ID, ch.Title)
		}
	}
	return len(channels), nil
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}
