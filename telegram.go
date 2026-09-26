package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

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

// UnreadChannel describes a dialog (channel, supergroup, group or private chat)
// that has unread messages.
type UnreadChannel struct {
	ID          int64  `json:"id" jsonschema:"Telegram dialog ID"`
	Title       string `json:"title" jsonschema:"dialog title"`
	Username    string `json:"username,omitempty" jsonschema:"public @username, if any"`
	UnreadCount int    `json:"unread_count" jsonschema:"number of unread messages"`
	UnreadMark  bool   `json:"unread_mark,omitempty" jsonschema:"true if manually marked as unread"`
	// Type is the only record of what the dialog is. It used to be flanked by
	// broadcast and megagroup bools, three fields for one fact, and they
	// disagreed: a dialog resolved but not cached reported an empty type and
	// broadcast false, which reads as a known "not a channel" for something
	// that is one.
	Type string `json:"type" jsonschema:"dialog type: private, group, supergroup or channel; empty if not known"`

	// readInboxMaxID is the ID of the last message marked as read. Messages with
	// a greater ID are unread. Kept unexported: it is an implementation detail.
	readInboxMaxID int
	peer           tg.InputPeerClass
}

// Dialog types, as reported in [UnreadChannel.Type].
const (
	typePrivate    = "private"
	typeGroup      = "group"
	typeSupergroup = "supergroup"
	typeChannel    = "channel"
)

// broadcast reports whether the dialog is a broadcast channel. False for a
// dialog whose type is not known, which is the safe direction: the tools that
// ask refuse to act on anything else.
func (c UnreadChannel) broadcast() bool {
	return c.Type == typeChannel
}

// channelType names what a channel object is, the one place the distinction
// between a broadcast channel and a supergroup is drawn.
func channelType(c *tg.Channel) string {
	if c.Megagroup {
		return typeSupergroup
	}

	return typeChannel
}

// Message is a single message returned to the MCP client.
type Message struct {
	ID        int    `json:"id" jsonschema:"message ID"`
	Date      string `json:"date" jsonschema:"send time in RFC3339"`
	EditDate  string `json:"edit_date,omitempty" jsonschema:"time of the last edit in RFC3339, if the message was edited"`
	Text      string `json:"text" jsonschema:"message text; for a rich message, its blocks rendered as Markdown"`
	Rich      bool   `json:"rich,omitempty" jsonschema:"true if the message is a rich message: structured blocks such as headings, lists and tables"`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"true if the rich message was delivered in part and more content exists"`
	Author    string `json:"author,omitempty" jsonschema:"sender name, for groups"`
	Out       bool   `json:"out,omitempty" jsonschema:"true if outgoing"`
	ReplyToID int    `json:"reply_to_id,omitempty" jsonschema:"ID of message being replied to"`
	// Forward is a pointer so that "not a forward" and "a forward whose source
	// is hidden" stay distinguishable.
	Forward   *Forward   `json:"forward,omitempty" jsonschema:"where the message was forwarded from; absent if the sender wrote it"`
	Reactions []Reaction `json:"reactions,omitempty" jsonschema:"reaction tally, most used first"`
	AlbumID   string     `json:"album_id,omitempty" jsonschema:"messages sharing this value were sent as one album, and only one of them carries the caption"`
	HasMedia  bool       `json:"has_media,omitempty" jsonschema:"true if the message has downloadable media (see get_file)"`
	MediaType string     `json:"media_type,omitempty" jsonschema:"media kind: photo, video, gif, video_note, audio, voice, sticker, document or poll"`
	FileName  string     `json:"file_name,omitempty" jsonschema:"file name of the attached document, if any"`
	Poll      *Poll      `json:"poll,omitempty" jsonschema:"poll contents and tally, when media_type is poll"`
	Service   bool       `json:"service,omitempty" jsonschema:"true for service messages, which carry an action instead of text"`
	Action    string     `json:"action,omitempty" jsonschema:"service action, e.g. screenshot_taken, pin_message, chat_add_user"`
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
//
// It serves from the in-process message buffer (fed by the update stream) when
// that holds enough of the newest unread messages, avoiding a getHistory RPC.
// Otherwise it falls back to messages.getHistory and backfills the buffer so the
// next read is free. This mirrors tdlib, which serves history locally and only
// hits the server to fill gaps.
func readUnread(ctx context.Context, api *tg.Client, cache *dialogCache, msgs *messageStore, target string, limit int) (UnreadChannel, []Message, error) {
	if limit <= 0 {
		limit = 50
	}

	ch, ok := cache.find(target)
	if !ok {
		return UnreadChannel{}, nil, errors.Errorf("channel %q not found in dialogs", target)
	}
	if !ch.broadcast() {
		return UnreadChannel{}, nil, errors.Errorf("channel %q is not a broadcast channel", target)
	}

	// Buffer first: the buffer always holds a contiguous newest suffix, so it is
	// authoritative when it has at least `limit` unread, or all of them.
	if msgs != nil {
		buffered, err := msgs.load(ch.ID, ch.readInboxMaxID, 0)
		if err != nil {
			cache.lg.Warn("Load buffered messages", zap.Int64("id", ch.ID), zap.Error(err))
		} else if len(buffered) >= limit || len(buffered) >= ch.UnreadCount {
			if len(buffered) > limit {
				buffered = buffered[:limit]
			}

			return ch, buffered, nil
		}
	}

	out, err := fetchUnreadHistory(ctx, api, cache, ch, limit)
	if err != nil {
		return UnreadChannel{}, nil, err
	}

	// Backfill so subsequent reads of this channel are served from the buffer.
	if msgs != nil {
		for _, m := range out {
			if err := msgs.append(ch.ID, m); err != nil {
				cache.lg.Warn("Backfill buffered message", zap.Int64("id", ch.ID), zap.Error(err))
				break
			}
		}
	}

	return ch, out, nil
}

// fetchUnreadHistory pulls the unread messages of a channel from the server,
// newest first, capped at limit.
func fetchUnreadHistory(ctx context.Context, api *tg.Client, cache *dialogCache, ch UnreadChannel, limit int) ([]Message, error) {
	var out []Message
	iter := messages.NewQueryBuilder(api).GetHistory(ch.peer).BatchSize(min(limit, 100)).Iter()
	for iter.Next(ctx) {
		msg, ok := messageFromClass(iter.Value().Msg, iter.Value().Entities)
		if !ok {
			continue
		}
		// History is returned newest-first; once we reach a message that is
		// already read, everything after it is read too.
		if msg.ID <= ch.readInboxMaxID {
			break
		}
		out = append(out, msg)
		if len(out) >= limit {
			break
		}
	}
	if err := iter.Err(); err != nil {
		if isChannelGone(err) {
			cache.remove(ch.ID)

			return nil, errors.Errorf("channel %d is no longer accessible", ch.ID)
		}

		return nil, errors.Wrap(err, "fetch history")
	}

	return out, nil
}

// channelFromDialog extracts dialog information from a dialog element,
// regardless of unread state. Returns false for unsupported dialogs.
func channelFromDialog(elem dialogElem) (UnreadChannel, bool) {
	dlg, ok := elem.Dialog.(*tg.Dialog)
	if !ok {
		return UnreadChannel{}, false
	}

	switch p := dlg.Peer.(type) {
	case *tg.PeerChannel:
		c, ok := elem.Entities.Channel(p.ChannelID)
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
			Type:           channelType(c),
			readInboxMaxID: dlg.ReadInboxMaxID,
			peer:           elem.Peer,
		}, true
	case *tg.PeerChat:
		ch, ok := elem.Entities.Chat(p.ChatID)
		if !ok {
			return UnreadChannel{}, false
		}
		return UnreadChannel{
			ID:             ch.ID,
			Title:          ch.Title,
			UnreadCount:    dlg.UnreadCount,
			UnreadMark:     dlg.UnreadMark,
			Type:           typeGroup,
			readInboxMaxID: dlg.ReadInboxMaxID,
			peer:           elem.Peer,
		}, true
	case *tg.PeerUser:
		u, ok := elem.Entities.User(p.UserID)
		if !ok {
			return UnreadChannel{}, false
		}
		name := strings.TrimSpace(u.FirstName + " " + u.LastName)
		if name == "" {
			name, _ = u.GetUsername()
		}
		username, _ := u.GetUsername()
		accessHash, _ := u.GetAccessHash()
		return UnreadChannel{
			ID:             u.ID,
			Title:          name,
			Username:       username,
			UnreadCount:    dlg.UnreadCount,
			UnreadMark:     dlg.UnreadMark,
			Type:           typePrivate,
			readInboxMaxID: dlg.ReadInboxMaxID,
			peer:           &tg.InputPeerUser{UserID: u.ID, AccessHash: accessHash},
		}, true
	default:
		return UnreadChannel{}, false
	}
}

// channelFromEntity builds an UnreadChannel from a channel object received in
// update entities, when the dialog is not yet cached.
func channelFromEntity(c *tg.Channel) UnreadChannel {
	username, _ := c.GetUsername()
	accessHash, _ := c.GetAccessHash()
	return UnreadChannel{
		ID:       c.ID,
		Title:    c.Title,
		Username: username,
		Type:     channelType(c),
		peer: &tg.InputPeerChannel{
			ChannelID:  c.ID,
			AccessHash: accessHash,
		},
	}
}

// messageFromClass converts a history item to a Message. Reports false for
// items that carry no content, such as tombstones of deleted messages.
func messageFromClass(m tg.NotEmptyMessage, ent entities) (Message, bool) {
	switch v := m.(type) {
	case *tg.Message:
		return messageFromTG(v, ent), true
	case *tg.MessageService:
		return messageFromService(v, ent), true
	default:
		return Message{}, false
	}
}

func messageFromTG(msg *tg.Message, ent entities) Message {
	from, hasFrom := msg.GetFromID()
	m := Message{
		ID:     msg.ID,
		Date:   time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
		Text:   msg.Message,
		Author: authorName(ent, from, hasFrom),
		Out:    msg.Out,
	}
	if rich, ok := msg.GetRichMessage(); ok {
		// A rich message keeps its content in page blocks and leaves the flat
		// text empty, so without rendering it the message reads as blank.
		m.Text = renderRich(rich.Blocks)
		m.Rich = true
		m.Truncated = rich.Part
	}
	if rt, ok := msg.GetReplyTo(); ok {
		if rtm, ok := rt.(*tg.MessageReplyHeader); ok {
			m.ReplyToID = rtm.ReplyToMsgID
		}
	}
	if d, ok := msg.GetEditDate(); ok {
		m.EditDate = time.Unix(int64(d), 0).UTC().Format(time.RFC3339)
	}
	if fwd, ok := msg.GetFwdFrom(); ok {
		m.Forward = forwardFrom(ent, fwd)
	}
	if r, ok := msg.GetReactions(); ok {
		m.Reactions = reactionsFrom(r)
	}
	if id, ok := msg.GetGroupedID(); ok {
		// As a string: the id is a random int64, and a JSON number keeps only
		// the top 53 bits of one in any client that parses it as a double.
		m.AlbumID = strconv.FormatInt(id, 10)
	}
	if media, ok := msg.GetMedia(); ok {
		switch mm := media.(type) {
		case *tg.MessageMediaPhoto:
			if _, ok := mm.Photo.(*tg.Photo); ok {
				m.HasMedia = true
				m.MediaType = "photo"
			}
		case *tg.MessageMediaDocument:
			if doc, ok := mm.Document.(*tg.Document); ok {
				m.HasMedia = true
				m.MediaType = documentKind(doc)
				m.FileName = documentFileName(doc)
			}
		case *tg.MessageMediaPoll:
			// Not downloadable, so HasMedia stays false: the poll is returned
			// inline instead of through get_file.
			m.MediaType = "poll"
			m.Poll = pollFromMedia(mm)
		}
	}
	return m
}

// messageFromService converts a service message, such as "took a screenshot"
// or "pinned a message", into a Message carrying the action name.
func messageFromService(msg *tg.MessageService, ent entities) Message {
	from, hasFrom := msg.GetFromID()
	m := Message{
		ID:      msg.ID,
		Date:    time.Unix(int64(msg.Date), 0).UTC().Format(time.RFC3339),
		Author:  authorName(ent, from, hasFrom),
		Out:     msg.Out,
		Service: true,
		Action:  actionName(msg.Action),
	}
	if rt, ok := msg.GetReplyTo(); ok {
		if rtm, ok := rt.(*tg.MessageReplyHeader); ok {
			m.ReplyToID = rtm.ReplyToMsgID
		}
	}
	if r, ok := msg.GetReactions(); ok {
		m.Reactions = reactionsFrom(r)
	}

	return m
}

// actionName converts a service action to a snake_case name, derived from its
// TL type: messageActionScreenshotTaken becomes screenshot_taken.
func actionName(a tg.MessageActionClass) string {
	if a == nil {
		return ""
	}

	return snakeCase(strings.TrimPrefix(a.TypeName(), "messageAction"))
}

// snakeCase converts a CamelCase TL type name to snake_case.
func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}

	return b.String()
}

// authorName resolves a human-readable sender name, when the sender is
// present in the entities.
func authorName(ent entities, from tg.PeerClass, ok bool) string {
	if !ok {
		return ""
	}

	return peerName(ent, from)
}

// peerName resolves a human-readable name for a peer present in the entities:
// a user's full name, or a group's or channel's title.
//
// Not only users: an anonymous admin and a signed channel post both name a
// channel as their sender, and reporting nothing for those reads as if the
// message had no author at all.
func peerName(ent entities, p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		u, ok := ent.User(v.UserID)
		if !ok {
			return ""
		}
		name := strings.TrimSpace(u.FirstName + " " + u.LastName)
		if name == "" {
			name, _ = u.GetUsername()
		}

		return name
	case *tg.PeerChat:
		c, ok := ent.Chat(v.ChatID)
		if !ok {
			return ""
		}

		return c.Title
	case *tg.PeerChannel:
		c, ok := ent.Channel(v.ChannelID)
		if !ok {
			return ""
		}

		return c.Title
	default:
		return ""
	}
}

// markPeerRead marks all messages in a dialog as read up to and including the
// latest message (MaxID=0 means "all messages"). Channels and supergroups use
// channels.readHistory, users and legacy groups messages.readHistory.
func markPeerRead(ctx context.Context, api *tg.Client, cache *dialogCache, p tg.InputPeerClass) error {
	switch v := p.(type) {
	case *tg.InputPeerChannel:
		_, err := api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{
				ChannelID:  v.ChannelID,
				AccessHash: v.AccessHash,
			},
			MaxID: 0,
		})
		if err != nil {
			if isChannelGone(err) {
				cache.remove(v.ChannelID)

				return nil
			}

			return errors.Wrap(err, "channels.readHistory")
		}
	default:
		if _, err := api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{
			Peer:  p,
			MaxID: 0,
		}); err != nil {
			return errors.Wrap(err, "messages.readHistory")
		}
	}

	cache.markReadPeer(p)

	return nil
}

// markChannelRead marks a cached dialog as read.
func markChannelRead(ctx context.Context, api *tg.Client, cache *dialogCache, ch UnreadChannel) error {
	return markPeerRead(ctx, api, cache, rebuildPeer(ch))
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

// messageBufferCap bounds how many recent messages are buffered per channel.
const messageBufferCap = 200

// registerCacheHandlers wires the update handlers that keep the dialog cache's
// unread counts and the per-channel message buffer live, mirroring how tdlib
// maintains them from the update stream. Each processed update is logged at
// debug level with structured fields.
func registerCacheHandlers(d *tg.UpdateDispatcher, cache *dialogCache, msgs *messageStore, lg *zap.Logger) {
	d.OnNewChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			lg.Debug("New channel message ignored", zap.String("type", fmt.Sprintf("%T", u.Message)))
			return nil
		}
		pc, ok := msg.PeerID.(*tg.PeerChannel)
		if !ok {
			return nil
		}
		if msg.Out {
			lg.Debug("New channel message (own, skipped)",
				zap.Int64("channel_id", pc.ChannelID), zap.Int("msg_id", msg.ID))
			return nil
		}

		id := pc.ChannelID

		// Ignore new messages in group chats (supergroups): tgmcp tracks
		// broadcast channels only.
		broadcast := false
		if c, ok := e.Channels[id]; ok {
			broadcast = c.Broadcast
		} else if ch, ok := cache.get(id); ok {
			broadcast = ch.broadcast()
		}
		if !broadcast {
			lg.Debug("New channel message ignored (chat)",
				zap.Int64("channel_id", id), zap.Int("msg_id", msg.ID))

			return nil
		}

		cache.observeIncoming(pc, func() (UnreadChannel, bool) {
			c, ok := e.Channels[id]
			if !ok {
				return UnreadChannel{}, false
			}
			return channelFromEntity(c), true
		})

		// Buffer the body so read_channel_unread can serve it without an RPC.
		buffered := false
		if msgs != nil {
			if err := msgs.append(id, messageFromTG(msg, peer.EntitiesFromUpdate(e))); err != nil {
				cache.lg.Warn("Buffer message", zap.Int64("id", id), zap.Error(err))
			} else {
				buffered = true
			}
		}

		lg.Debug("New channel message",
			zap.Int64("channel_id", id),
			zap.Int("msg_id", msg.ID),
			zap.Int("len", len(msg.Message)),
			zap.Bool("buffered", buffered),
		)

		return nil
	})

	d.OnEditChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		if msgs == nil {
			return nil
		}
		msg, ok := u.Message.(*tg.Message)
		if !ok {
			return nil
		}
		pc, ok := msg.PeerID.(*tg.PeerChannel)
		if !ok {
			return nil
		}
		if err := msgs.edit(pc.ChannelID, messageFromTG(msg, peer.EntitiesFromUpdate(e))); err != nil {
			cache.lg.Warn("Edit buffered message", zap.Int64("id", pc.ChannelID), zap.Error(err))
		}

		lg.Debug("Edit channel message",
			zap.Int64("channel_id", pc.ChannelID), zap.Int("msg_id", msg.ID))

		return nil
	})

	d.OnDeleteChannelMessages(func(_ context.Context, _ tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		if msgs == nil {
			return nil
		}
		if err := msgs.deleteMessages(u.ChannelID, u.Messages); err != nil {
			cache.lg.Warn("Delete buffered messages", zap.Int64("id", u.ChannelID), zap.Error(err))
		}

		lg.Debug("Delete channel messages",
			zap.Int64("channel_id", u.ChannelID), zap.Int("count", len(u.Messages)))

		return nil
	})

	d.OnReadChannelInbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadChannelInbox) error {
		cache.setRead(&tg.PeerChannel{ChannelID: u.ChannelID}, u.MaxID, u.StillUnreadCount)
		if msgs != nil {
			if err := msgs.pruneRead(u.ChannelID, u.MaxID); err != nil {
				cache.lg.Warn("Prune buffered messages", zap.Int64("id", u.ChannelID), zap.Error(err))
			}
		}

		lg.Debug("Read channel inbox",
			zap.Int64("channel_id", u.ChannelID),
			zap.Int("max_id", u.MaxID),
			zap.Int("still_unread", u.StillUnreadCount),
		)

		return nil
	})

	// updateNewMessage covers private chats and legacy groups, the dialogs
	// updateNewChannelMessage never reports. Without it their unread counts
	// only ever went down: nothing incremented them after the initial dialog
	// fetch, so a chat that received messages while the process ran looked
	// read.
	d.OnNewMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		observeNewMessage(cache, lg, e, u.Message)

		return nil
	})

	// updateChannel says only that something about a channel changed: the id is
	// the entire payload. When the updates container happens to carry the
	// entity as well, that is enough to learn a channel the cache has never
	// seen, such as one just created, without spending an RPC. When it does
	// not, drop it: the next start refetches the dialog list anyway.
	d.OnChannel(func(_ context.Context, e tg.Entities, u *tg.UpdateChannel) error {
		c, ok := e.Channels[u.ChannelID]
		if !ok {
			return nil
		}
		if cache.learn(channelFromEntity(c)) {
			lg.Debug("Learned channel", zap.Int64("channel_id", u.ChannelID), zap.String("title", c.Title))
		}

		return nil
	})

	d.OnReadHistoryInbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadHistoryInbox) error {
		cache.setRead(u.Peer, u.MaxID, u.StillUnreadCount)

		lg.Debug("Read history inbox",
			zap.String("peer", fmt.Sprintf("%T", u.Peer)),
			zap.Int("max_id", u.MaxID),
			zap.Int("still_unread", u.StillUnreadCount),
		)

		return nil
	})

	d.OnDialogUnreadMark(func(_ context.Context, _ tg.Entities, u *tg.UpdateDialogUnreadMark) error {
		peer, ok := u.Peer.(*tg.DialogPeer)
		if !ok {
			return nil
		}
		cache.setUnreadMark(peer.Peer, u.Unread)

		lg.Debug("Dialog unread mark",
			zap.String("peer", fmt.Sprintf("%T", peer.Peer)), zap.Bool("unread", u.Unread))

		return nil
	})
}

// observeNewMessage records an incoming message in a private chat or a legacy
// group, so that the cached unread count follows it.
//
// The message body is not buffered: the buffer exists to let
// read_channel_unread answer without an RPC, and that tool serves broadcast
// channels only. Buffering every direct message would cost memory no reader
// spends.
func observeNewMessage(cache *dialogCache, lg *zap.Logger, e tg.Entities, m tg.MessageClass) {
	var (
		peerID tg.PeerClass
		out    bool
		id     int
	)
	switch v := m.(type) {
	case *tg.Message:
		peerID, out, id = v.PeerID, v.Out, v.ID
	case *tg.MessageService:
		// Counted too: joining a group or pinning a message leaves an unread
		// message in the dialog like any other.
		peerID, out, id = v.PeerID, v.Out, v.ID
	default:
		return
	}

	switch peerID.(type) {
	case *tg.PeerUser, *tg.PeerChat:
	default:
		// A channel arrives as updateNewChannelMessage, which counts it. Doing
		// it here as well would count it twice.
		return
	}
	if out {
		return
	}

	ent := peer.EntitiesFromUpdate(e)
	cache.observeIncoming(peerID, func() (UnreadChannel, bool) {
		return channelFromEntities(ent, peerID)
	})

	lg.Debug("New message",
		zap.String("peer", fmt.Sprintf("%T", peerID)),
		zap.Int("msg_id", id),
	)
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// resolvePeer resolves a chat target string to an InputPeerClass.
//
// Supported forms:
//   - "me" or "self" -> &tg.InputPeerSelf{}
//   - numeric ID or cached @username -> cached peer if present
//   - @username / t.me link / phone / domain -> use peer resolver (gotd)
func (s *server) resolvePeer(ctx context.Context, target string) (tg.InputPeerClass, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("target is required")
	}
	lower := strings.ToLower(target)
	if lower == "me" || lower == "self" {
		return &tg.InputPeerSelf{}, nil
	}

	// Try cached dialog first by numeric ID or @username.
	if ch, ok := s.cache.find(target); ok && ch.peer != nil {
		return ch.peer, nil
	}

	// Fallback to the peer resolver (supports @user, t.me, phone, domain),
	// which files away what the chat turns out to be, see [resolver].
	p, err := peer.Resolve(target).Bind(newResolver(s.api, s.resolved))(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve %q", target)
	}
	return p, nil
}

// rebuildPeerFromDialog returns a proper input peer for a cached dialog,
// preserving access hashes when present.
func rebuildPeer(ch UnreadChannel) tg.InputPeerClass {
	if ch.peer != nil {
		return ch.peer
	}
	// Fallback construction when peer is missing (legacy data).
	if ch.Type == typeChannel || ch.Type == typeSupergroup {
		return &tg.InputPeerChannel{ChannelID: ch.ID}
	}
	if ch.Type == "private" {
		return &tg.InputPeerUser{UserID: ch.ID}
	}
	if ch.Type == "group" {
		return &tg.InputPeerChat{ChatID: ch.ID}
	}
	return &tg.InputPeerChannel{ChannelID: ch.ID}
}
