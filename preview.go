package main

import (
	"context"
	"strings"
	"unicode/utf16"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

type previewFormatInput struct {
	Text      string `json:"text" jsonschema:"text to render"`
	ParseMode string `json:"parse_mode,omitempty" jsonschema:"text format: plain (default, sent as-is), markdown, html, rich_markdown or rich_html"`
}

type previewFormatOutput struct {
	Kind        string         `json:"kind" jsonschema:"styled for entity-formatted text, rich for a rich message"`
	Text        string         `json:"text" jsonschema:"the text Telegram will display, with the markup removed; for a rich message, its blocks rendered back to Markdown"`
	Entities    []TextEntity   `json:"entities" jsonschema:"formatting that will be applied, in message order; empty for a rich message"`
	Blocks      []PreviewBlock `json:"blocks,omitempty" jsonschema:"top-level blocks of a rich message"`
	Length      int            `json:"length" jsonschema:"length in UTF-16 units, the unit Telegram limits: 4096 for a message, 1024 for a caption"`
	Attribution string         `json:"attribution" jsonschema:"how the message would be marked as agent-sent: off, footer or bot"`
	Note        string         `json:"note,omitempty" jsonschema:"caveat that applies to this preview"`
}

// PreviewBlock is one top-level block of a previewed rich message.
type PreviewBlock struct {
	Type    string `json:"type" jsonschema:"block type, e.g. heading1, paragraph, table, list"`
	Text    string `json:"text,omitempty" jsonschema:"first line of the block, truncated"`
	Rows    int    `json:"rows,omitempty" jsonschema:"row count, for a table"`
	Columns int    `json:"columns,omitempty" jsonschema:"column count, for a table"`
	Items   int    `json:"items,omitempty" jsonschema:"item count, for a list"`
}

// TextEntity is one formatting range of a rendered message.
type TextEntity struct {
	Type       string `json:"type" jsonschema:"entity type, e.g. bold, italic, code, text_url"`
	Offset     int    `json:"offset" jsonschema:"start of the range, in UTF-16 units"`
	Length     int    `json:"length" jsonschema:"length of the range, in UTF-16 units"`
	Text       string `json:"text" jsonschema:"the covered substring"`
	URL        string `json:"url,omitempty" jsonschema:"link target, for text_url"`
	Language   string `json:"language,omitempty" jsonschema:"code language, for pre"`
	UserID     int64  `json:"user_id,omitempty" jsonschema:"mentioned user, for mention_name"`
	DocumentID int64  `json:"document_id,omitempty" jsonschema:"emoji document, for custom_emoji"`
}

// handlePreviewFormat renders text without sending it, so that a caller can
// check the result — and catch a markup error — before posting.
func (s *server) handlePreviewFormat(_ context.Context, _ *mcp.CallToolRequest, in previewFormatInput) (*mcp.CallToolResult, previewFormatOutput, error) {
	if in.Text == "" {
		return nil, previewFormatOutput{}, errors.New("text is required")
	}
	if isRichMode(in.ParseMode) {
		return s.previewRich(in)
	}

	opts, attribution, err := s.messageOptions(in.Text, in.ParseMode)
	if err != nil {
		return nil, previewFormatOutput{}, err
	}

	text, entities, err := renderStyled(opts...)
	if err != nil {
		return nil, previewFormatOutput{}, err
	}

	return nil, previewFormatOutput{
		Kind:        "styled",
		Text:        text,
		Entities:    describeEntities(text, entities),
		Length:      entity.ComputeLength(text),
		Attribution: string(attribution),
	}, nil
}

// previewRich previews a rich message by parsing its source locally.
//
// Sending hands the source to Telegram, which parses it itself, so this is a
// structural check rather than a byte-exact preview: the note says so.
func (s *server) previewRich(in previewFormatInput) (*mcp.CallToolResult, previewFormatOutput, error) {
	text, attribution := in.Text, attributionOff
	if s.attribution != attributionOff {
		text = richWithFooter(text, in.ParseMode, s.footer)
		attribution = attributionFooter
	}

	blocks, err := parseRich(text, in.ParseMode)
	if err != nil {
		return nil, previewFormatOutput{}, err
	}
	rendered := renderRich(blocks)

	return nil, previewFormatOutput{
		Kind:        "rich",
		Text:        rendered,
		Entities:    []TextEntity{},
		Blocks:      describeBlocks(blocks),
		Length:      entity.ComputeLength(rendered),
		Attribution: string(attribution),
		Note:        "parsed locally; Telegram parses the source itself when sending and may differ on media, footnotes and maps",
	}, nil
}

func describeBlocks(blocks []tg.PageBlockClass) []PreviewBlock {
	out := make([]PreviewBlock, 0, len(blocks))
	for _, b := range blocks {
		pb := PreviewBlock{Type: blockType(b), Text: firstLine(renderBlock(b))}
		switch v := b.(type) {
		case *tg.PageBlockTable:
			pb.Text, pb.Rows = "", len(v.Rows)
			if len(v.Rows) > 0 {
				pb.Columns = len(v.Rows[0].Cells)
			}
		case *tg.PageBlockList:
			pb.Text, pb.Items = "", len(v.Items)
		case *tg.PageBlockOrderedList:
			pb.Text, pb.Items = "", len(v.Items)
		}
		out = append(out, pb)
	}

	return out
}

// firstLine shortens a block rendering to something an agent can scan.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if r := []rune(line); len(r) > 80 {
		return string(r[:80]) + "…"
	}

	return line
}

// renderStyled resolves opts into the message text and entities that would be
// sent, exactly as the sending builders do.
func renderStyled(opts ...styling.StyledTextOption) (string, []tg.MessageEntityClass, error) {
	var b entity.Builder
	if err := styling.Perform(&b, opts...); err != nil {
		return "", nil, errors.Wrap(err, "perform styling")
	}
	text, entities := b.Complete()

	return text, entities, nil
}

func describeEntities(text string, entities []tg.MessageEntityClass) []TextEntity {
	out := make([]TextEntity, 0, len(entities))
	units := utf16.Encode([]rune(text))

	for _, e := range entities {
		te := TextEntity{
			Type:   entityType(e),
			Offset: e.GetOffset(),
			Length: e.GetLength(),
			Text:   utf16Slice(units, e.GetOffset(), e.GetLength()),
		}
		if v, ok := e.(interface{ GetURL() string }); ok {
			te.URL = v.GetURL()
		}
		if v, ok := e.(interface{ GetLanguage() string }); ok {
			te.Language = v.GetLanguage()
		}
		if v, ok := e.(interface{ GetUserID() int64 }); ok {
			te.UserID = v.GetUserID()
		}
		if v, ok := e.(interface{ GetDocumentID() int64 }); ok {
			te.DocumentID = v.GetDocumentID()
		}

		out = append(out, te)
	}

	return out
}

// entityType turns a TL constructor name into a snake_case type, so that
// "messageEntityTextUrl" reads as "text_url".
func entityType(e tg.MessageEntityClass) string {
	return snakeCase(strings.TrimPrefix(e.TypeName(), "messageEntity"))
}

// blockType names a page block the same way, so that "pageBlockOrderedList"
// reads as "ordered_list".
func blockType(b tg.PageBlockClass) string {
	return snakeCase(strings.TrimPrefix(b.TypeName(), "pageBlock"))
}

// utf16Slice returns the substring an entity covers. Telegram counts offsets
// in UTF-16 units, not in bytes or runes.
func utf16Slice(units []uint16, offset, length int) string {
	if offset < 0 || length <= 0 || offset >= len(units) {
		return ""
	}
	end := offset + length
	if end > len(units) {
		end = len(units)
	}

	return string(utf16.Decode(units[offset:end]))
}
