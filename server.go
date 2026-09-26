package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-faster/errors"
	"github.com/go-faster/gooners/blob"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/query/messages"
	"github.com/gotd/td/tg"
)

// server holds the dependencies shared by the MCP tool handlers.
type server struct {
	api   *tg.Client
	cache *dialogCache
	// resolved holds chat metadata learned while resolving a target, for
	// dialogs the cache does not have, see [resolvedPeers].
	resolved         *resolvedPeers
	msgs             *messageStore
	lg               *zap.Logger
	fileRootVal      string
	allowSend        bool
	allowProfileEdit bool
	allowInlineMedia bool
	blobs            blob.Store

	// Agent attribution, see [attributionMode].
	attribution attributionMode
	strict      bool
	footer      string
	botToken    string
	botUsername string
	sessionDir  string
	spool       *inlineSpool
	// selfID is the signed-in account, stamped on spooled payloads so that
	// only it can claim them.
	selfID int64

	botMu sync.Mutex
	bot   tg.InputUserClass
}

// listChannelsInput has no parameters.
type listChannelsInput struct{}

type listChannelsOutput struct {
	Channels []UnreadChannel `json:"channels" jsonschema:"channels with unread messages"`
}

type readChannelInput struct {
	Channel string `json:"channel" jsonschema:"channel @username or numeric ID, as returned by list_unread_channels"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of messages to return (default 50)"`
}

type readChannelOutput struct {
	Channel  UnreadChannel `json:"channel" jsonschema:"the resolved channel"`
	Messages []Message     `json:"messages" jsonschema:"unread messages, newest first"`
}

type markChatReadInput struct {
	Chat string `json:"chat" jsonschema:"chat target (id, @username, me, t.me link)"`
}

type markChatReadOutput struct {
	Chat UnreadChannel `json:"chat" jsonschema:"the dialog that was marked as read"`
}

// getMeInput has no parameters.
type getMeInput struct{}

type getMeOutput struct {
	User PeerInfo `json:"user" jsonschema:"the signed-in account"`
}

type searchChatsInput struct {
	Query string `json:"query" jsonschema:"name, @username or phone to search for"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum results (default 20, max 100)"`
}

type searchChatsOutput struct {
	MyResults     []PeerInfo `json:"my_results" jsonschema:"matches among own dialogs and contacts"`
	GlobalResults []PeerInfo `json:"global_results" jsonschema:"public matches from the global directory"`
	Limit         int        `json:"limit" jsonschema:"applied result limit"`
}

type resolvePeerInput struct {
	Target string `json:"target" jsonschema:"peer target (id, @username, me, t.me link, phone)"`
}

type resolvePeerOutput struct {
	Peer PeerInfo `json:"peer" jsonschema:"the resolved peer"`
}

// markAllChannelsReadInput has no parameters.
type markAllChannelsReadInput struct{}

type markAllChannelsReadOutput struct {
	MarkedCount int `json:"marked_count" jsonschema:"number of channels marked as read"`
}

type listChatsInput struct {
	Query      string `json:"query,omitempty" jsonschema:"case-insensitive substring match on title or @username"`
	Type       string `json:"type,omitempty" jsonschema:"filter by type: private, group, supergroup, channel"`
	UnreadOnly bool   `json:"unread_only,omitempty" jsonschema:"only return dialogs with unread>0"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum results (default 100)"`
}

type listChatsOutput struct {
	Chats        []UnreadChannel `json:"chats" jsonschema:"cached dialogs"`
	Limit        int             `json:"limit" jsonschema:"applied result limit"`
	Limited      bool            `json:"limited" jsonschema:"true if results were capped by limit"`
	TotalMatched int             `json:"total_matched" jsonschema:"number of cached dialogs matching filters before limit"`
}

type getChatMessagesInput struct {
	Chat   string `json:"chat" jsonschema:"chat target (id, @username, me, t.me link)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max messages (default 50)"`
	Offset int    `json:"offset_id,omitempty" jsonschema:"start after this message id"`
}

type getChatMessagesOutput struct {
	Chat         UnreadChannel `json:"chat" jsonschema:"resolved chat"`
	Messages     []Message     `json:"messages" jsonschema:"messages newest first"`
	Limit        int           `json:"limit" jsonschema:"applied result limit"`
	Limited      bool          `json:"limited" jsonschema:"true if results reached the applied limit; use next_offset_id to continue"`
	NextOffsetID int           `json:"next_offset_id,omitempty" jsonschema:"message id to pass as offset_id for the next page"`
}

type searchChatMessagesInput struct {
	Chat   string `json:"chat" jsonschema:"chat target"`
	Query  string `json:"query" jsonschema:"search query"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max results (default 50)"`
	Offset int    `json:"offset_id,omitempty" jsonschema:"start after this message id"`
	Filter string `json:"filter,omitempty" jsonschema:"empty, photo, video, document, url, photos, photo_video, voice, music"`
}

type searchChatMessagesOutput struct {
	Chat         UnreadChannel `json:"chat" jsonschema:"resolved chat"`
	Messages     []Message     `json:"messages" jsonschema:"matching messages"`
	Limit        int           `json:"limit" jsonschema:"applied result limit"`
	Limited      bool          `json:"limited" jsonschema:"true if results reached the applied limit; use next_offset_id to continue"`
	NextOffsetID int           `json:"next_offset_id,omitempty" jsonschema:"message id to pass as offset_id for the next page"`
}

type sendMessageInput struct {
	Chat             string `json:"chat" jsonschema:"chat target"`
	Text             string `json:"text" jsonschema:"message text"`
	ParseMode        string `json:"parse_mode,omitempty" jsonschema:"text format: plain (default, sent as-is), markdown, html, rich_markdown or rich_html"`
	ReplyToMessageID int    `json:"reply_to_message_id,omitempty" jsonschema:"reply to this message id"`
	Silent           bool   `json:"silent,omitempty" jsonschema:"send without notification"`
	NoWebpage        bool   `json:"no_webpage,omitempty" jsonschema:"disable link preview"`
}

type sendMessageOutput struct {
	OK          bool   `json:"ok" jsonschema:"true on success"`
	MessageID   int    `json:"message_id,omitempty" jsonschema:"sent message id if known"`
	Attribution string `json:"attribution" jsonschema:"how the message was marked as agent-sent: off, footer or bot"`
}

type sendFileInput struct {
	// Source names a stored object to upload, as "blob". It is embedded
	// rather than declared so that every server names it the same way.
	blob.Source

	Chat             string `json:"chat" jsonschema:"chat target"`
	Path             string `json:"path,omitempty" jsonschema:"path relative to TG_FILE_ROOT or absolute inside it; give either this or blob"`
	Caption          string `json:"caption,omitempty" jsonschema:"optional caption"`
	ParseMode        string `json:"parse_mode,omitempty" jsonschema:"caption format: plain (default, sent as-is), markdown or html"`
	Kind             string `json:"kind,omitempty" jsonschema:"how Telegram should render it: auto (default, from the MIME type), document, photo, video, gif, audio, voice, video_note or sticker"`
	DurationSeconds  int    `json:"duration_seconds,omitempty" jsonschema:"duration, for video, gif, audio, voice and video_note; without it the player shows none"`
	Width            int    `json:"width,omitempty" jsonschema:"pixel width, for video, gif and video_note"`
	Height           int    `json:"height,omitempty" jsonschema:"pixel height, for video, gif and video_note"`
	ThumbnailPath    string `json:"thumbnail_path,omitempty" jsonschema:"JPEG cover under TG_FILE_ROOT; Telegram needs one to treat a file as an animation"`
	ThumbnailBlob    string `json:"thumbnail_blob,omitempty" jsonschema:"stored JPEG cover, as an alternative to thumbnail_path; a blob id, like blob"`
	Title            string `json:"title,omitempty" jsonschema:"track title, for audio"`
	Performer        string `json:"performer,omitempty" jsonschema:"track performer, for audio"`
	ReplyToMessageID int    `json:"reply_to_message_id,omitempty" jsonschema:"reply to this message id"`
	Silent           bool   `json:"silent,omitempty" jsonschema:"send without notification"`
}

type sendFileOutput struct {
	OK          bool   `json:"ok" jsonschema:"true on success"`
	MessageID   int    `json:"message_id,omitempty" jsonschema:"sent message id if known"`
	Kind        string `json:"kind" jsonschema:"the kind actually sent, with auto resolved"`
	Attribution string `json:"attribution" jsonschema:"how the caption was marked as agent-sent: off or footer"`
}

type sendReactionInput struct {
	Chat      string `json:"chat" jsonschema:"chat target"`
	MessageID int    `json:"message_id" jsonschema:"id of the message to react to"`
	Emoji     string `json:"emoji,omitempty" jsonschema:"reaction emoji, e.g. \U0001F44D; empty removes the reaction"`
	Big       bool   `json:"big,omitempty" jsonschema:"show a big animated reaction"`
}

type sendReactionOutput struct {
	OK bool `json:"ok" jsonschema:"true on success"`
}

type sendChatActionInput struct {
	Chat   string `json:"chat" jsonschema:"chat target"`
	Action string `json:"action" jsonschema:"one of: typing, cancel, upload_photo, upload_document, record_audio, upload_audio, record_video, upload_video, choose_sticker, geo, record_round, upload_round"`
}

type sendChatActionOutput struct {
	OK bool `json:"ok" jsonschema:"true on success"`
}

type sendScreenshotNotificationInput struct {
	Chat      string `json:"chat" jsonschema:"chat target (id, @username, me, t.me link)"`
	MessageID int    `json:"message_id,omitempty" jsonschema:"id of the message that was screenshotted; omit to not point at one"`
}

type sendScreenshotNotificationOutput struct {
	OK bool `json:"ok" jsonschema:"true on success"`
}

type getFileInput struct {
	Chat      string `json:"chat" jsonschema:"chat target"`
	MessageID int    `json:"message_id" jsonschema:"id of the message whose media to download"`
	Path      string `json:"path,omitempty" jsonschema:"destination path relative to TG_FILE_ROOT; defaults to a name derived from the media"`
	Inline    bool   `json:"inline,omitempty" jsonschema:"return the file in the tool result instead of writing it to disk; size-limited, requires TG_ALLOW_INLINE_MEDIA"`
}

type getFileOutput struct {
	// Result carries the stored object: its id for another tool, and its URL
	// for the agent. Embedded so that every server returns the same shape.
	blob.Result

	OK     bool   `json:"ok" jsonschema:"true on success"`
	Path   string `json:"path,omitempty" jsonschema:"path written, relative to TG_FILE_ROOT; empty when inline"`
	Inline bool   `json:"inline,omitempty" jsonschema:"true if the file was returned in the tool result"`
}

type updateProfileInput struct {
	FirstName *string `json:"first_name,omitempty" jsonschema:"new first name; omit to leave unchanged"`
	LastName  *string `json:"last_name,omitempty" jsonschema:"new last name; omit to leave unchanged"`
	About     *string `json:"about,omitempty" jsonschema:"new bio/about text; omit to leave unchanged"`
}

type updateProfileOutput struct {
	OK bool `json:"ok" jsonschema:"true on success"`
}

type updateProfilePhotoInput struct {
	Path string `json:"path" jsonschema:"path relative to TG_FILE_ROOT or absolute inside it"`
}

type updateProfilePhotoOutput struct {
	OK bool `json:"ok" jsonschema:"true on success"`
}

// register wires the tools onto an MCP server.
func (s *server) register(m *mcp.Server) {
	mcp.AddTool(m, &mcp.Tool{
		Name:        "list_unread_channels",
		Description: "List Telegram broadcast channels that currently have unread messages, with their unread counts.",
	}, s.handleListChannels)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "read_channel_unread",
		Description: "Read the unread messages of a Telegram channel, newest first. Reading does not mark them as read.",
	}, s.handleReadChannel)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "mark_chat_read",
		Description: "Mark all messages in a dialog as read. Works for private chats, groups, supergroups and channels.",
	}, s.handleMarkChatRead)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "mark_all_channels_read",
		Description: "Mark all unread Telegram broadcast channels as read in one call. Does not touch private chats or groups.",
	}, s.handleMarkAllChannelsRead)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "list_chats",
		Description: "List cached dialogs with id, title, username, type and unread count. Optional query matches title or username.",
	}, s.handleListChats)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "get_me",
		Description: "Get the signed-in Telegram account: id, name, username and bio.",
	}, s.handleGetMe)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "search_chats",
		Description: "Search Telegram for users, bots, groups and channels by name or @username, including public ones not in the dialog list.",
	}, s.handleSearchChats)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "resolve_peer",
		Description: "Resolve a user, bot, group or channel by id, @username, t.me link or phone, and return its details.",
	}, s.handleResolvePeer)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "get_chat_messages",
		Description: "Fetch recent messages from a chat by target (id, @username, me, t.me link).",
	}, s.handleGetChatMessages)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "search_chat_messages",
		Description: "Search messages in a chat by query. Optional filter: photo, video, document, url, photos, photo_video, voice, music.",
	}, s.handleSearchChatMessages)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "get_file",
		Description: "Download the media attached to a message into TG_FILE_ROOT.",
	}, s.handleGetFile)

	if s.allowSend {
		mcp.AddTool(m, &mcp.Tool{
			Name:        "preview_format",
			Description: "Render text with a parse_mode without sending it: returns the text Telegram will display, the formatting entities and the length, or the block outline for a rich message. Use it to check markup, and to catch a markup error, before posting.",
		}, s.handlePreviewFormat)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "send_message",
			Description: "Send text message to a chat. Supports parse_mode (plain, markdown, html, or rich_markdown/rich_html for a rich message with headings, lists and tables), reply_to_message_id, silent, no_webpage.",
		}, s.handleSendMessage)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "edit_message",
			Description: "Replace the text, or the media caption, of a message you sent. Supports parse_mode (plain, markdown, html, rich_markdown, rich_html) and no_webpage.",
		}, s.handleEditMessage)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "send_file",
			Description: "Send a file, from TG_FILE_ROOT (path) or the blob store (blob). kind picks how Telegram renders it: auto, document, photo, video, gif, audio, voice, video_note, sticker.",
		}, s.handleSendFile)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "send_reaction",
			Description: "React to a message with an emoji. Empty emoji removes the reaction.",
		}, s.handleSendReaction)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "send_chat_action",
			Description: "Send a transient chat action (typing, uploading, recording, etc).",
		}, s.handleSendChatAction)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "send_screenshot_notification",
			Description: "Notify a chat that a screenshot was taken. Posts a visible service message.",
		}, s.handleSendScreenshotNotification)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "send_poll",
			Description: "Send a poll or quiz to a chat. Needs at least 2 options; quizzes need correct_option.",
		}, s.handleSendPoll)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "vote_poll",
			Description: "Vote in a poll by option index. Empty options retracts the vote. Returns the updated tally.",
		}, s.handleVotePoll)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "list_inline_results",
			Description: "Ask an inline bot what it offers for a query, without sending anything. The query is delivered to the bot's operator.",
		}, s.handleListInlineResults)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "send_inline_result",
			Description: "Send a result from an inline bot, which marks the message 'via @bot'. Defaults to the first result.",
		}, s.handleSendInlineResult)

		mcp.AddTool(m, &mcp.Tool{
			Name: "start_stream",
			Description: "Start a live stream in a channel or group and return the RTMP url and key to push frames to, " +
				"e.g. ffmpeg -re -i input.mp4 -c copy -f flv \"<url><key>\". " +
				"Needs the right to manage video chats. The key belongs to the chat and outlives the stream, so treat it as a secret: " +
				"anyone holding it can broadcast to the chat the next time a stream runs. Use revoke_stream_key to burn one that leaked.",
		}, s.handleStartStream)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "stop_stream",
			Description: "End the live stream, or any video chat, running in a chat. Succeeds with stopped=false if nothing was live.",
		}, s.handleStopStream)

		mcp.AddTool(m, &mcp.Tool{
			Name: "revoke_stream_key",
			Description: "Replace a chat's RTMP stream key without disclosing the replacement, breaking any encoder still pushing with the old one. " +
				"Requires owning the chat; admin rights are not enough. Call start_stream to obtain the new key.",
		}, s.handleRevokeStreamKey)
	}

	if s.allowProfileEdit {
		mcp.AddTool(m, &mcp.Tool{
			Name:        "update_profile",
			Description: "Update the account's first name, last name and/or bio (about text).",
		}, s.handleUpdateProfile)

		mcp.AddTool(m, &mcp.Tool{
			Name:        "update_profile_photo",
			Description: "Set the account's profile photo from a file under TG_FILE_ROOT.",
		}, s.handleUpdateProfilePhoto)
	}
}

func (s *server) handleListChannels(_ context.Context, _ *mcp.CallToolRequest, _ listChannelsInput) (*mcp.CallToolResult, listChannelsOutput, error) {
	return nil, listChannelsOutput{Channels: s.cache.unread()}, nil
}

func (s *server) handleReadChannel(ctx context.Context, _ *mcp.CallToolRequest, in readChannelInput) (*mcp.CallToolResult, readChannelOutput, error) {
	if in.Channel == "" {
		return nil, readChannelOutput{}, errors.New("channel is required")
	}
	ch, msgs, err := readUnread(ctx, s.api, s.cache, s.msgs, in.Channel, in.Limit)
	if err != nil {
		return nil, readChannelOutput{}, err
	}
	if !ch.broadcast() {
		return nil, readChannelOutput{}, errors.Errorf("channel %q is not a broadcast channel", in.Channel)
	}
	return nil, readChannelOutput{Channel: ch, Messages: msgs}, nil
}

func (s *server) handleMarkChatRead(ctx context.Context, _ *mcp.CallToolRequest, in markChatReadInput) (*mcp.CallToolResult, markChatReadOutput, error) {
	if in.Chat == "" {
		return nil, markChatReadOutput{}, errors.New("chat is required")
	}
	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, markChatReadOutput{}, err
	}
	if err := markPeerRead(ctx, s.api, s.cache, p); err != nil {
		return nil, markChatReadOutput{}, err
	}

	return nil, markChatReadOutput{Chat: s.peerToChannel(p)}, nil
}

func (s *server) handleMarkAllChannelsRead(ctx context.Context, _ *mcp.CallToolRequest, _ markAllChannelsReadInput) (*mcp.CallToolResult, markAllChannelsReadOutput, error) {
	n, err := markAllChannelsRead(ctx, s.api, s.cache)
	if err != nil {
		return nil, markAllChannelsReadOutput{}, err
	}
	return nil, markAllChannelsReadOutput{MarkedCount: n}, nil
}

func (s *server) handleListChats(_ context.Context, _ *mcp.CallToolRequest, in listChatsInput) (*mcp.CallToolResult, listChatsOutput, error) {
	all := s.cache.all()
	if in.UnreadOnly {
		var f []UnreadChannel
		for _, c := range all {
			if c.UnreadCount > 0 || c.UnreadMark {
				f = append(f, c)
			}
		}
		all = f
	}
	if in.Type != "" {
		typ := strings.ToLower(in.Type)
		var f []UnreadChannel
		for _, c := range all {
			if strings.EqualFold(c.Type, typ) {
				f = append(f, c)
			}
		}
		all = f
	}
	if q := strings.TrimPrefix(strings.TrimSpace(in.Query), "@"); q != "" {
		var f []UnreadChannel
		for _, c := range all {
			if matchesDialog(c, q) {
				f = append(f, c)
			}
		}
		all = f
	}
	lim := in.Limit
	if lim <= 0 || lim > 100 {
		lim = 100
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ID == all[j].ID {
			return all[i].Type < all[j].Type
		}

		return all[i].ID > all[j].ID
	})
	total := len(all)
	limited := len(all) > lim
	if len(all) > lim {
		all = all[:lim]
	}
	return nil, listChatsOutput{Chats: all, Limit: lim, Limited: limited, TotalMatched: total}, nil
}

// matchesDialog reports whether a dialog title or username contains q,
// case-insensitively.
func matchesDialog(ch UnreadChannel, q string) bool {
	return strings.Contains(strings.ToLower(ch.Title), strings.ToLower(q)) ||
		strings.Contains(strings.ToLower(ch.Username), strings.ToLower(q))
}

func (s *server) handleGetChatMessages(ctx context.Context, _ *mcp.CallToolRequest, in getChatMessagesInput) (*mcp.CallToolResult, getChatMessagesOutput, error) {
	if in.Chat == "" {
		return nil, getChatMessagesOutput{}, errors.New("chat is required")
	}
	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, getChatMessagesOutput{}, err
	}
	lim := in.Limit
	if lim <= 0 {
		lim = 50
	}
	if lim > 100 {
		lim = 100
	}
	ch := s.peerToChannel(p)
	msgs, err := fetchMessages(ctx, s.api, p, in.Offset, lim)
	if err != nil {
		return nil, getChatMessagesOutput{}, err
	}
	return nil, getChatMessagesOutput{
		Chat:         ch,
		Messages:     msgs,
		Limit:        lim,
		Limited:      len(msgs) >= lim,
		NextOffsetID: nextOffsetID(msgs, lim),
	}, nil
}

// fetchMessages gets up to limit messages from peer starting after offsetID.
func fetchMessages(ctx context.Context, api *tg.Client, p tg.InputPeerClass, offsetID, limit int) ([]Message, error) {
	var out []Message
	iter := messages.NewQueryBuilder(api).GetHistory(p).BatchSize(min(limit, 100)).Iter()
	if offsetID > 0 {
		iter = iter.OffsetID(offsetID)
	}
	for iter.Next(ctx) {
		msg, ok := messageFromClass(iter.Value().Msg, iter.Value().Entities)
		if !ok {
			continue
		}
		out = append(out, msg)
		if len(out) >= limit {
			break
		}
	}
	if err := iter.Err(); err != nil {
		return nil, errors.Wrap(err, "get history")
	}
	return out, nil
}

func (s *server) handleSearchChatMessages(ctx context.Context, _ *mcp.CallToolRequest, in searchChatMessagesInput) (*mcp.CallToolResult, searchChatMessagesOutput, error) {
	if in.Chat == "" || (in.Query == "" && in.Filter == "") {
		return nil, searchChatMessagesOutput{}, errors.New("chat and query or filter are required")
	}
	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, searchChatMessagesOutput{}, err
	}
	lim := in.Limit
	if lim <= 0 {
		lim = 50
	}
	if lim > 100 {
		lim = 100
	}
	filt, err := searchFilter(in.Filter)
	if err != nil {
		return nil, searchChatMessagesOutput{}, err
	}
	iter := messages.NewQueryBuilder(s.api).Search(p).Q(in.Query).Filter(filt).BatchSize(min(lim, 100)).Iter()
	if in.Offset > 0 {
		iter = iter.OffsetID(in.Offset)
	}
	var out []Message
	for iter.Next(ctx) && len(out) < lim {
		m, ok := messageFromClass(iter.Value().Msg, iter.Value().Entities)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	if err := iter.Err(); err != nil {
		return nil, searchChatMessagesOutput{}, errors.Wrap(err, "search")
	}
	ch := s.peerToChannel(p)
	return nil, searchChatMessagesOutput{
		Chat:         ch,
		Messages:     out,
		Limit:        lim,
		Limited:      len(out) >= lim,
		NextOffsetID: nextOffsetID(out, lim),
	}, nil
}

func nextOffsetID(msgs []Message, limit int) int {
	if len(msgs) < limit || len(msgs) == 0 {
		return 0
	}

	return msgs[len(msgs)-1].ID
}

func (s *server) handleSendMessage(ctx context.Context, _ *mcp.CallToolRequest, in sendMessageInput) (*mcp.CallToolResult, sendMessageOutput, error) {
	if in.Chat == "" || in.Text == "" {
		return nil, sendMessageOutput{}, errors.New("chat and text are required")
	}
	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, sendMessageOutput{}, err
	}
	upd, attribution, err := s.sendText(ctx, p, in)
	if err != nil {
		return nil, sendMessageOutput{}, errors.Wrap(err, "send")
	}
	id := extractSentMessageID(upd)
	return nil, sendMessageOutput{
		OK:          true,
		MessageID:   id,
		Attribution: string(attribution),
	}, nil
}

func (s *server) handleSendFile(ctx context.Context, _ *mcp.CallToolRequest, in sendFileInput) (*mcp.CallToolResult, sendFileOutput, error) {
	if in.Chat == "" {
		return nil, sendFileOutput{}, errors.New("chat is required")
	}
	if (in.Path == "") == (in.Blob == "") {
		return nil, sendFileOutput{}, errors.New("exactly one of path or blob is required")
	}

	kind, err := parseFileKind(in.Kind)
	if err != nil {
		return nil, sendFileOutput{}, err
	}

	src, err := s.uploadSource(ctx, in)
	if err != nil {
		return nil, sendFileOutput{}, err
	}
	defer src.release()

	// Only the source knows the MIME type, so "auto" resolves here.
	kind = kind.resolve(src.mimeType)

	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, sendFileOutput{}, err
	}
	b := message.NewSender(s.api).To(p)
	if in.Silent {
		b.Silent()
	}
	if in.ReplyToMessageID > 0 {
		b.Reply(in.ReplyToMessageID)
	}
	caption, attribution, err := s.styledCaption(in.Caption, in.ParseMode)
	if err != nil {
		return nil, sendFileOutput{}, err
	}
	// Upload first, then describe the result: the attributes decide whether
	// the same bytes arrive as an attachment, a video or a voice message.
	f, err := b.Upload(src.option).AsInputFile(ctx)
	if err != nil {
		return nil, sendFileOutput{}, errors.Wrap(err, "upload file")
	}

	thumb, err := s.uploadThumbnail(ctx, b, in)
	if err != nil {
		return nil, sendFileOutput{}, err
	}

	upd, err := b.Media(ctx, kind.mediaOption(f, thumb, src, in, caption))
	if err != nil {
		return nil, sendFileOutput{}, errors.Wrap(err, "send file")
	}
	id := extractSentMessageID(upd)
	return nil, sendFileOutput{
		OK:          true,
		MessageID:   id,
		Kind:        string(kind),
		Attribution: string(attribution),
	}, nil
}

func (s *server) handleSendReaction(ctx context.Context, _ *mcp.CallToolRequest, in sendReactionInput) (*mcp.CallToolResult, sendReactionOutput, error) {
	if in.Chat == "" || in.MessageID <= 0 {
		return nil, sendReactionOutput{}, errors.New("chat and message_id are required")
	}
	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, sendReactionOutput{}, err
	}
	var reaction []tg.ReactionClass
	if in.Emoji != "" {
		reaction = append(reaction, &tg.ReactionEmoji{Emoticon: in.Emoji})
	}
	_, err = s.api.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
		Big:         in.Big,
		AddToRecent: true,
		Peer:        p,
		MsgID:       in.MessageID,
		Reaction:    reaction,
	})
	if err != nil {
		return nil, sendReactionOutput{}, errors.Wrap(err, "send reaction")
	}
	return nil, sendReactionOutput{OK: true}, nil
}

func (s *server) handleSendChatAction(ctx context.Context, _ *mcp.CallToolRequest, in sendChatActionInput) (*mcp.CallToolResult, sendChatActionOutput, error) {
	if in.Chat == "" || in.Action == "" {
		return nil, sendChatActionOutput{}, errors.New("chat and action are required")
	}
	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, sendChatActionOutput{}, err
	}
	b := message.NewSender(s.api).To(p).TypingAction()
	if err := sendChatAction(ctx, b, in.Action); err != nil {
		return nil, sendChatActionOutput{}, err
	}
	return nil, sendChatActionOutput{OK: true}, nil
}

func (s *server) handleSendScreenshotNotification(ctx context.Context, _ *mcp.CallToolRequest, in sendScreenshotNotificationInput) (*mcp.CallToolResult, sendScreenshotNotificationOutput, error) {
	if in.Chat == "" {
		return nil, sendScreenshotNotificationOutput{}, errors.New("chat is required")
	}
	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, sendScreenshotNotificationOutput{}, err
	}
	randomID, err := randomInt64()
	if err != nil {
		return nil, sendScreenshotNotificationOutput{}, err
	}
	if _, err := s.api.MessagesSendScreenshotNotification(ctx, &tg.MessagesSendScreenshotNotificationRequest{
		Peer:     p,
		ReplyTo:  &tg.InputReplyToMessage{ReplyToMsgID: in.MessageID},
		RandomID: randomID,
	}); err != nil {
		return nil, sendScreenshotNotificationOutput{}, errors.Wrap(err, "messages.sendScreenshotNotification")
	}

	return nil, sendScreenshotNotificationOutput{OK: true}, nil
}

// randomInt64 returns a cryptographically random ID for deduplicating sends.
func randomInt64() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, errors.Wrap(err, "random id")
	}

	return int64(binary.LittleEndian.Uint64(buf[:])), nil
}

// sendChatAction dispatches a chat action name to the matching
// TypingActionBuilder method.
func sendChatAction(ctx context.Context, b *message.TypingActionBuilder, action string) error {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "typing":
		return b.Typing(ctx)
	case "cancel":
		return b.Cancel(ctx)
	case "upload_photo":
		return b.UploadPhoto(ctx, 0)
	case "upload_document":
		return b.UploadDocument(ctx, 0)
	case "record_audio":
		return b.RecordAudio(ctx)
	case "upload_audio":
		return b.UploadAudio(ctx, 0)
	case "record_video":
		return b.RecordVideo(ctx)
	case "upload_video":
		return b.UploadVideo(ctx, 0)
	case "choose_sticker":
		return b.ChooseSticker(ctx)
	case "geo":
		return b.GeoLocation(ctx)
	case "record_round":
		return b.RecordRound(ctx)
	case "upload_round":
		return b.UploadRound(ctx, 0)
	default:
		return errors.Errorf("unsupported chat action %q", action)
	}
}

// peerToChannel returns a lightweight UnreadChannel for output when we only
// have a resolved peer (no full dialog metadata).
func (s *server) peerToChannel(p tg.InputPeerClass) UnreadChannel {
	switch v := p.(type) {
	case *tg.InputPeerSelf:
		return UnreadChannel{ID: 0, Title: "me", Type: typePrivate, peer: v}
	case *tg.InputPeerUser:
		if ch, ok := s.peerKnown(v, v.UserID); ok {
			return ch
		}

		return UnreadChannel{ID: v.UserID, Type: typePrivate, peer: v}
	case *tg.InputPeerChat:
		if ch, ok := s.peerKnown(v, v.ChatID); ok {
			return ch
		}

		return UnreadChannel{ID: v.ChatID, Type: typeGroup, peer: v}
	case *tg.InputPeerChannel:
		if ch, ok := s.peerKnown(v, v.ChannelID); ok {
			return ch
		}

		// Type is left empty rather than guessed: an input peer says a channel
		// is a channel, not whether it broadcasts.
		return UnreadChannel{ID: v.ChannelID, peer: v}
	default:
		return UnreadChannel{peer: p}
	}
}

// peerKnown looks a dialog up in the cache, then in what resolving a target
// has already revealed.
func (s *server) peerKnown(p tg.InputPeerClass, id int64) (UnreadChannel, bool) {
	if ch, ok := s.cache.getPeer(p); ok {
		return ch, true
	}

	return s.resolved.get(id)
}

// safeJoin ensures p is inside root (no traversal). Accepts relative and
// absolute p as long as resulting path is under root.
func safeJoin(root, p string) (string, error) {
	if root == "" {
		return "", errors.New("no file root configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", errors.Wrap(err, "resolve file root")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.Wrap(err, "resolve file root symlinks")
	}
	clean := filepath.Clean(p)
	var abs string
	if filepath.IsAbs(clean) {
		abs, err = filepath.Abs(clean)
	} else {
		abs, err = filepath.Abs(filepath.Join(root, clean))
	}
	if err != nil {
		return "", errors.Wrap(err, "resolve path")
	}
	abs, err = filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", errors.Wrap(err, "resolve path symlinks")
	}
	root = filepath.Clean(root)
	if !strings.HasPrefix(abs+string(filepath.Separator), root+string(filepath.Separator)) && abs != root {
		return "", errors.Errorf("path %q outside root %q", p, root)
	}
	return abs, nil
}

// searchFilter maps a simple string to a MessagesFilterClass.
func searchFilter(f string) (tg.MessagesFilterClass, error) {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "":
		return &tg.InputMessagesFilterEmpty{}, nil
	case "photo":
		return &tg.InputMessagesFilterPhotos{}, nil
	case "video":
		return &tg.InputMessagesFilterVideo{}, nil
	case "document":
		return &tg.InputMessagesFilterDocument{}, nil
	case "url":
		return &tg.InputMessagesFilterURL{}, nil
	case "photos":
		return &tg.InputMessagesFilterPhotos{}, nil
	case "photo_video":
		return &tg.InputMessagesFilterPhotoVideo{}, nil
	case "voice":
		return &tg.InputMessagesFilterVoice{}, nil
	case "music":
		return &tg.InputMessagesFilterMusic{}, nil
	default:
		return nil, errors.Errorf("unsupported search filter %q", f)
	}
}

// extractSentMessageID tries to pull a message ID from common send result
// shapes. Returns 0 if not extractable.
func extractSentMessageID(upd tg.UpdatesClass) int {
	switch u := upd.(type) {
	case *tg.UpdateShortSentMessage:
		return u.ID
	case *tg.Updates:
		for _, x := range u.Updates {
			if m, ok := x.(*tg.UpdateMessageID); ok {
				return int(m.ID)
			}
			if nm, ok := x.(*tg.UpdateNewMessage); ok {
				if m, ok := nm.Message.(*tg.Message); ok {
					return m.ID
				}
			}
			if nm, ok := x.(*tg.UpdateNewChannelMessage); ok {
				if m, ok := nm.Message.(*tg.Message); ok {
					return m.ID
				}
			}
		}
	}
	return 0
}
