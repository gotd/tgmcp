package main

import (
	"context"
	stdhtml "html"
	"strings"

	"github.com/go-faster/errors"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/rich"
	"github.com/gotd/td/tg"
)

// Parse modes that produce a rich message instead of styled text.
const (
	parseModeRichMarkdown = "rich_markdown"
	parseModeRichHTML     = "rich_html"
)

// isRichMode reports whether mode selects a rich message.
func isRichMode(mode string) bool {
	switch normalizeMode(mode) {
	case parseModeRichMarkdown, parseModeRichHTML:
		return true
	default:
		return false
	}
}

// richInput builds the rich message source for a rich parse mode.
//
// The source is parsed by Telegram rather than locally: gotd can parse it
// here, but documents that as best-effort, while the servers do what the
// official clients get.
func richInput(text, mode string) (tg.InputRichMessageClass, error) {
	switch normalizeMode(mode) {
	case parseModeRichMarkdown:
		return rich.Rich().Markdown(text), nil
	case parseModeRichHTML:
		return rich.Rich().HTML(text), nil
	default:
		return nil, errors.Errorf("parse_mode %q is not a rich message mode", mode)
	}
}

// parseRich parses a rich source into blocks locally, for previewing. gotd
// documents this parser as best-effort: sending uses [richInput], which lets
// Telegram parse the same source.
func parseRich(text, mode string) ([]tg.PageBlockClass, error) {
	switch normalizeMode(mode) {
	case parseModeRichMarkdown:
		blocks, err := rich.ParseMarkdown(strings.NewReader(text))
		if err != nil {
			return nil, errors.Wrap(err, "parse markdown")
		}

		return blocks, nil
	case parseModeRichHTML:
		blocks, err := rich.ParseHTML(strings.NewReader(text))
		if err != nil {
			return nil, errors.Wrap(err, "parse html")
		}

		return blocks, nil
	default:
		return nil, errors.Errorf("parse_mode %q is not a rich message mode", mode)
	}
}

// richWithFooter appends the agent footer to a rich source, in the syntax of
// that source.
func richWithFooter(text, mode, footer string) string {
	if footer == "" {
		return text
	}
	if normalizeMode(mode) == parseModeRichHTML {
		return text + "\n<p><i>" + stdhtml.EscapeString(footer) + "</i></p>"
	}

	return text + "\n\n_" + footer + "_"
}

// richMessage builds the rich source for text and reports which attribution
// was applied. A rich message cannot be carried by an inline result, so
// [attributionBot] degrades to the footer here, as it does for uploads.
func (s *server) richMessage(text, mode string) (tg.InputRichMessageClass, attributionMode, error) {
	attribution := attributionOff
	if s.attribution != attributionOff {
		text = richWithFooter(text, mode, s.footer)
		attribution = attributionFooter
	}

	src, err := richInput(text, mode)
	if err != nil {
		return nil, "", err
	}

	return src, attribution, nil
}

// sendRich sends in.Text as a rich message and reports which attribution was
// applied.
func (s *server) sendRich(ctx context.Context, p tg.InputPeerClass, in sendMessageInput) (tg.UpdatesClass, attributionMode, error) {
	src, attribution, err := s.richMessage(in.Text, in.ParseMode)
	if err != nil {
		return nil, "", err
	}

	b := message.NewSender(s.api).To(p)
	if in.Silent {
		b.Silent()
	}
	if in.NoWebpage {
		b.NoWebpage()
	}
	if in.ReplyToMessageID > 0 {
		b.Reply(in.ReplyToMessageID)
	}

	upd, err := b.RichMessage(ctx, src)
	if err != nil {
		return nil, "", errors.Wrap(err, "send rich message")
	}

	return upd, attribution, nil
}
