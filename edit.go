package main

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tgerr"
)

type editMessageInput struct {
	Chat      string `json:"chat" jsonschema:"chat target"`
	MessageID int    `json:"message_id" jsonschema:"id of the message to edit; it must be one you sent"`
	Text      string `json:"text" jsonschema:"new text, or new caption for a media message"`
	ParseMode string `json:"parse_mode,omitempty" jsonschema:"text format: plain (default, sent as-is), markdown, html, rich_markdown or rich_html"`
	NoWebpage bool   `json:"no_webpage,omitempty" jsonschema:"disable link preview"`
}

type editMessageOutput struct {
	OK          bool   `json:"ok" jsonschema:"true on success"`
	MessageID   int    `json:"message_id" jsonschema:"the edited message id"`
	Attribution string `json:"attribution" jsonschema:"how the new text is marked as agent-sent: off, footer or bot"`
}

// handleEditMessage replaces the text of a message, re-applying the agent
// footer so that an edit cannot quietly strip the mark.
//
// A message sent through the echo bot cannot be edited at all, see
// [editError].
func (s *server) handleEditMessage(ctx context.Context, _ *mcp.CallToolRequest, in editMessageInput) (*mcp.CallToolResult, editMessageOutput, error) {
	if in.Chat == "" || in.MessageID <= 0 || in.Text == "" {
		return nil, editMessageOutput{}, errors.New("chat, message_id and text are required")
	}

	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, editMessageOutput{}, err
	}

	b := message.NewSender(s.api).To(p)
	if in.NoWebpage {
		b.NoWebpage()
	}

	var attribution attributionMode
	if isRichMode(in.ParseMode) {
		src, mode, err := s.richMessage(in.Text, in.ParseMode)
		if err != nil {
			return nil, editMessageOutput{}, err
		}
		attribution = mode
		if _, err := b.Edit(in.MessageID).RichMessage(ctx, src); err != nil {
			return nil, editMessageOutput{}, editError(err)
		}
	} else {
		body, mode, err := s.messageOptions(in.Text, in.ParseMode)
		if err != nil {
			return nil, editMessageOutput{}, err
		}
		attribution = mode
		if _, err := b.Edit(in.MessageID).StyledText(ctx, body...); err != nil {
			return nil, editMessageOutput{}, editError(err)
		}
	}

	return nil, editMessageOutput{
		OK:          true,
		MessageID:   in.MessageID,
		Attribution: string(attribution),
	}, nil
}

// editError explains the one failure a caller cannot act on from the raw RPC
// error.
//
// Telegram refuses to edit a message that was sent through an inline bot: only
// the bot may, through messages.editInlineBotMessage, and that needs an inline
// message id which the sending account never receives. So under
// TG_ATTRIBUTION=bot, messages sent by tgmcp cannot be edited afterwards.
func editError(err error) error {
	if tgerr.Is(err, "INLINE_BOT_REQUIRED") {
		return errors.Wrap(err, "this message was sent via the inline echo bot, and only that bot could edit it; send with TG_ATTRIBUTION=footer or off if the message must stay editable")
	}

	return errors.Wrap(err, "edit message")
}
