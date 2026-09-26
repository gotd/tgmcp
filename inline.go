package main

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/tg"
)

// InlineResult is one answer offered by an inline bot.
//
// Media results carry no title or description, so the file fields are the only
// way to tell what sending one would post.
type InlineResult struct {
	ID          string `json:"id" jsonschema:"result id, as passed to send_inline_result"`
	Type        string `json:"type" jsonschema:"result kind, e.g. article, photo, gif"`
	Title       string `json:"title,omitempty" jsonschema:"result title, often empty for media"`
	Description string `json:"description,omitempty" jsonschema:"result description, often empty for media"`
	URL         string `json:"url,omitempty" jsonschema:"source URL, for link and article results"`
	MimeType    string `json:"mime_type,omitempty" jsonschema:"MIME type of the attached file"`
	FileName    string `json:"file_name,omitempty" jsonschema:"file name of the attached document"`
	Size        int64  `json:"size,omitempty" jsonschema:"file size in bytes"`
	Width       int    `json:"width,omitempty" jsonschema:"pixel width, for images and video"`
	Height      int    `json:"height,omitempty" jsonschema:"pixel height, for images and video"`
	Duration    int    `json:"duration,omitempty" jsonschema:"duration in seconds, for audio and video"`
}

// inlineResultFrom describes r as fully as its variant allows.
func inlineResultFrom(r tg.BotInlineResultClass) InlineResult {
	title, _ := r.GetTitle()
	description, _ := r.GetDescription()
	out := InlineResult{
		ID:          r.GetID(),
		Type:        r.GetType(),
		Title:       title,
		Description: description,
	}

	switch v := r.(type) {
	case *tg.BotInlineResult:
		out.URL, _ = v.GetURL()
	case *tg.BotInlineMediaResult:
		if doc, ok := v.Document.(*tg.Document); ok {
			out.MimeType = doc.MimeType
			out.Size = doc.Size
			out.FileName = documentFileName(doc)
			describeDocument(doc, &out)
		}
		if photo, ok := v.Photo.(*tg.Photo); ok {
			out.MimeType = "image"
			describePhoto(photo, &out)
		}
	}

	return out
}

// describeDocument fills in the dimensions and duration carried by a
// document's attributes.
func describeDocument(doc *tg.Document, out *InlineResult) {
	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeVideo:
			out.Width, out.Height = a.W, a.H
			out.Duration = int(a.Duration)
		case *tg.DocumentAttributeImageSize:
			out.Width, out.Height = a.W, a.H
		case *tg.DocumentAttributeAudio:
			out.Duration = a.Duration
		}
	}
}

// describePhoto reports the largest size the photo is available in.
func describePhoto(photo *tg.Photo, out *InlineResult) {
	for _, s := range photo.Sizes {
		size, ok := s.(*tg.PhotoSize)
		if !ok {
			continue
		}
		if size.W > out.Width {
			out.Width, out.Height = size.W, size.H
		}
	}
}

type listInlineResultsInput struct {
	Bot   string `json:"bot" jsonschema:"inline bot target (@username or id)"`
	Chat  string `json:"chat" jsonschema:"chat the results would be sent to; bots may answer differently per chat"`
	Query string `json:"query" jsonschema:"query text, as typed after the bot username"`
}

type listInlineResultsOutput struct {
	Results    []InlineResult `json:"results" jsonschema:"offered results"`
	NextOffset string         `json:"next_offset,omitempty" jsonschema:"offset for the next page, when the bot paginates"`
}

type sendInlineResultInput struct {
	Bot              string `json:"bot" jsonschema:"inline bot target (@username or id)"`
	Chat             string `json:"chat" jsonschema:"chat target"`
	Query            string `json:"query" jsonschema:"query text, as typed after the bot username"`
	ResultID         string `json:"result_id,omitempty" jsonschema:"id of the result to send; defaults to the first"`
	ReplyToMessageID int    `json:"reply_to_message_id,omitempty" jsonschema:"reply to this message id"`
	Silent           bool   `json:"silent,omitempty" jsonschema:"send without notification"`
	HideVia          bool   `json:"hide_via,omitempty" jsonschema:"hide the 'via @bot' header, where the bot allows it"`
}

type sendInlineResultOutput struct {
	OK        bool   `json:"ok" jsonschema:"true on success"`
	MessageID int    `json:"message_id,omitempty" jsonschema:"sent message id if known"`
	ResultID  string `json:"result_id" jsonschema:"id of the result that was sent"`
}

func (s *server) handleListInlineResults(ctx context.Context, _ *mcp.CallToolRequest, in listInlineResultsInput) (*mcp.CallToolResult, listInlineResultsOutput, error) {
	res, _, err := s.inlineResults(ctx, in.Bot, in.Chat, in.Query)
	if err != nil {
		return nil, listInlineResultsOutput{}, err
	}

	out := listInlineResultsOutput{NextOffset: res.NextOffset}
	for _, r := range res.Results {
		out.Results = append(out.Results, inlineResultFrom(r))
	}

	return nil, out, nil
}

func (s *server) handleSendInlineResult(ctx context.Context, _ *mcp.CallToolRequest, in sendInlineResultInput) (*mcp.CallToolResult, sendInlineResultOutput, error) {
	res, peer, err := s.inlineResults(ctx, in.Bot, in.Chat, in.Query)
	if err != nil {
		return nil, sendInlineResultOutput{}, err
	}
	if len(res.Results) == 0 {
		return nil, sendInlineResultOutput{}, errors.Errorf("bot %q returned no results for %q", in.Bot, in.Query)
	}

	id := in.ResultID
	switch {
	case id != "":
		if !hasInlineResult(res.Results, id) {
			return nil, sendInlineResultOutput{}, errors.Errorf("bot %q did not offer result %q", in.Bot, id)
		}
	case len(res.Results) == 1:
		// Unambiguous: there is nothing else it could mean.
		id = res.Results[0].GetID()
	default:
		// Refuse to guess. A bot's first result is not necessarily the best
		// one, and for media results the caller cannot see what it is without
		// looking, so picking it blindly posts unknown content.
		return nil, sendInlineResultOutput{}, errors.Errorf(
			"bot %q offered %d results: pass result_id to choose one (see list_inline_results)",
			in.Bot, len(res.Results))
	}

	randomID, err := randomInt64()
	if err != nil {
		return nil, sendInlineResultOutput{}, err
	}

	req := &tg.MessagesSendInlineBotResultRequest{
		Peer:       peer,
		RandomID:   randomID,
		QueryID:    res.QueryID,
		ID:         id,
		Silent:     in.Silent,
		HideVia:    in.HideVia,
		ClearDraft: true,
	}
	if in.ReplyToMessageID > 0 {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: in.ReplyToMessageID})
	}

	upd, err := s.api.MessagesSendInlineBotResult(ctx, req)
	if err != nil {
		return nil, sendInlineResultOutput{}, errors.Wrap(err, "send inline bot result")
	}

	return nil, sendInlineResultOutput{
		OK:        true,
		MessageID: extractSentMessageID(upd),
		ResultID:  id,
	}, nil
}

// inlineResults queries bot for query as if it were typed in chat, returning
// the answer along with the resolved chat peer.
//
// The chat matters: a bot is told where the result would go and may answer
// differently, or refuse.
func (s *server) inlineResults(ctx context.Context, bot, chat, query string) (*tg.MessagesBotResults, tg.InputPeerClass, error) {
	if bot == "" || chat == "" {
		return nil, nil, errors.New("bot and chat are required")
	}

	botPeer, err := s.resolvePeer(ctx, bot)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "resolve bot %q", bot)
	}
	botUser, ok := botPeer.(*tg.InputPeerUser)
	if !ok {
		return nil, nil, errors.Errorf("%q is not a bot", bot)
	}

	peer, err := s.resolvePeer(ctx, chat)
	if err != nil {
		return nil, nil, err
	}

	res, err := s.api.MessagesGetInlineBotResults(ctx, &tg.MessagesGetInlineBotResultsRequest{
		Bot:   &tg.InputUser{UserID: botUser.UserID, AccessHash: botUser.AccessHash},
		Peer:  peer,
		Query: query,
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "get inline bot results")
	}

	return res, peer, nil
}

func hasInlineResult(results []tg.BotInlineResultClass, id string) bool {
	for _, r := range results {
		if r.GetID() == id {
			return true
		}
	}

	return false
}
