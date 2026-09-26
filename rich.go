package main

import (
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
)

// renderRich renders the blocks of a rich message as Markdown.
//
// A rich message carries structured content — headings, lists, tables, math —
// instead of a flat string, and leaves [tg.Message.Message] empty. Without
// this, such a message reads as blank.
//
// The rendering is lossy by design: it is meant to be read, not to round-trip.
// Blocks with no textual form, such as photos and embeds, become a bracketed
// placeholder so that their presence is still visible.
func renderRich(blocks []tg.PageBlockClass) string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if s := renderBlock(b); s != "" {
			out = append(out, s)
		}
	}

	return strings.Join(out, "\n\n")
}

func renderBlock(block tg.PageBlockClass) string {
	switch b := block.(type) {
	case *tg.PageBlockTitle:
		return heading(1, b.Text)
	case *tg.PageBlockSubtitle:
		return heading(2, b.Text)
	case *tg.PageBlockHeading1:
		return heading(1, b.Text)
	case *tg.PageBlockHeading2:
		return heading(2, b.Text)
	case *tg.PageBlockHeading3:
		return heading(3, b.Text)
	case *tg.PageBlockHeading4:
		return heading(4, b.Text)
	case *tg.PageBlockHeading5:
		return heading(5, b.Text)
	case *tg.PageBlockHeading6:
		return heading(6, b.Text)
	case *tg.PageBlockHeader:
		return heading(2, b.Text)
	case *tg.PageBlockSubheader:
		return heading(3, b.Text)
	case *tg.PageBlockKicker:
		return renderText(b.Text)
	case *tg.PageBlockFooter:
		return renderText(b.Text)
	case *tg.PageBlockParagraph:
		return renderText(b.Text)
	case *tg.PageBlockPreformatted:
		return "```" + b.Language + "\n" + renderText(b.Text) + "\n```"
	case *tg.PageBlockBlockquote:
		return withCaption(quote(renderText(b.Text)), renderText(b.Caption))
	case *tg.PageBlockPullquote:
		return withCaption(quote(renderText(b.Text)), renderText(b.Caption))
	case *tg.PageBlockBlockquoteBlocks:
		return withCaption(quote(renderRich(b.Blocks)), renderText(b.Caption))
	case *tg.PageBlockDivider:
		return "---"
	case *tg.PageBlockList:
		return renderList(b.Items)
	case *tg.PageBlockOrderedList:
		return renderOrderedList(b.Items)
	case *tg.PageBlockTable:
		return renderTable(b)
	case *tg.PageBlockDetails:
		return withCaption("**"+renderText(b.Title)+"**", renderRich(b.Blocks))
	case *tg.PageBlockMath:
		return "$$\n" + b.Source + "\n$$"
	case *tg.PageBlockThinking:
		return quote(renderText(b.Text))
	case *tg.PageBlockAnchor:
		return ""
	case *tg.PageBlockPhoto:
		return placeholder("photo", b.Caption)
	case *tg.PageBlockVideo:
		return placeholder("video", b.Caption)
	case *tg.PageBlockAudio:
		return placeholder("audio", b.Caption)
	case *tg.PageBlockCollage:
		return placeholder(fmt.Sprintf("collage of %d", len(b.Items)), b.Caption)
	case *tg.PageBlockSlideshow:
		return placeholder(fmt.Sprintf("slideshow of %d", len(b.Items)), b.Caption)
	case *tg.PageBlockEmbed:
		return placeholder("embed", b.Caption)
	case *tg.PageBlockEmbedPost:
		return placeholder("embedded post", b.Caption)
	case *tg.PageBlockMap:
		return placeholder("map", b.Caption)
	case *tg.PageBlockCover:
		return renderBlock(b.Cover)
	case *tg.PageBlockAuthorDate:
		return renderText(b.Author)
	default:
		return "[" + strings.TrimPrefix(block.TypeName(), "pageBlock") + "]"
	}
}

// heading renders a heading block. The text is trimmed: a heading cannot span
// lines, and Telegram lets one carry leading or trailing newlines.
func heading(level int, text tg.RichTextClass) string {
	body := strings.TrimSpace(renderText(text))
	if body == "" {
		return ""
	}

	return strings.Repeat("#", level) + " " + body
}

func renderList(items []tg.PageListItemClass) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case *tg.PageListItemText:
			lines = append(lines, "- "+checkbox(v.Checkbox, v.Checked)+renderText(v.Text))
		case *tg.PageListItemBlocks:
			lines = append(lines, "- "+checkbox(v.Checkbox, v.Checked)+indent(renderRich(v.Blocks)))
		}
	}

	return strings.Join(lines, "\n")
}

func renderOrderedList(items []tg.PageListOrderedItemClass) string {
	lines := make([]string, 0, len(items))
	for i, item := range items {
		num := fmt.Sprintf("%d.", i+1)
		switch v := item.(type) {
		case *tg.PageListOrderedItemText:
			if v.Num != "" {
				num = v.Num
			}
			lines = append(lines, num+" "+checkbox(v.Checkbox, v.Checked)+renderText(v.Text))
		case *tg.PageListOrderedItemBlocks:
			if v.Num != "" {
				num = v.Num
			}
			lines = append(lines, num+" "+checkbox(v.Checkbox, v.Checked)+indent(renderRich(v.Blocks)))
		}
	}

	return strings.Join(lines, "\n")
}

// renderTable renders a table as a Markdown table, synthesizing an empty
// header row when the first row does not hold header cells: Markdown has no
// syntax for a table without one.
func renderTable(t *tg.PageBlockTable) string {
	if len(t.Rows) == 0 {
		return ""
	}

	var lines []string
	if title := renderText(t.Title); title != "" {
		lines = append(lines, "**"+title+"**", "")
	}

	head := t.Rows[0]
	rest := t.Rows[1:]
	if !headerRow(head) {
		head = blankRow(len(head.Cells))
		rest = t.Rows
	}

	lines = append(lines, tableRow(head), tableRule(head))
	for _, r := range rest {
		lines = append(lines, tableRow(r))
	}

	return strings.Join(lines, "\n")
}

func headerRow(r tg.PageTableRow) bool {
	for _, c := range r.Cells {
		if !c.Header {
			return false
		}
	}

	return len(r.Cells) > 0
}

func blankRow(n int) tg.PageTableRow {
	return tg.PageTableRow{Cells: make([]tg.PageTableCell, n)}
}

func tableRow(r tg.PageTableRow) string {
	cells := make([]string, 0, len(r.Cells))
	for _, c := range r.Cells {
		cells = append(cells, cellText(c))
	}

	return "| " + strings.Join(cells, " | ") + " |"
}

func tableRule(r tg.PageTableRow) string {
	rules := make([]string, 0, len(r.Cells))
	for _, c := range r.Cells {
		switch {
		case c.AlignCenter:
			rules = append(rules, ":---:")
		case c.AlignRight:
			rules = append(rules, "---:")
		default:
			rules = append(rules, "---")
		}
	}

	return "| " + strings.Join(rules, " | ") + " |"
}

// cellText flattens a cell: a Markdown table cell cannot hold a line break,
// and a bare pipe would end the cell.
func cellText(c tg.PageTableCell) string {
	s := strings.ReplaceAll(renderText(c.Text), "|", `\|`)

	return strings.Join(strings.Fields(s), " ")
}

func checkbox(has, checked bool) string {
	switch {
	case !has:
		return ""
	case checked:
		return "[x] "
	default:
		return "[ ] "
	}
}

func quote(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}

	return strings.Join(lines, "\n")
}

func indent(s string) string {
	return strings.ReplaceAll(s, "\n", "\n  ")
}

func withCaption(body, caption string) string {
	if caption == "" {
		return body
	}
	if body == "" {
		return caption
	}

	return body + "\n\n" + caption
}

func placeholder(kind string, c tg.PageCaption) string {
	return withCaption("["+kind+"]", renderText(c.Text))
}

// renderText renders rich text as Markdown inline formatting.
func renderText(text tg.RichTextClass) string {
	switch t := text.(type) {
	case nil, *tg.TextEmpty:
		return ""
	case *tg.TextPlain:
		return t.Text
	case *tg.TextConcat:
		var sb strings.Builder
		for _, part := range t.Texts {
			sb.WriteString(renderText(part))
		}

		return sb.String()
	case *tg.TextBold:
		return wrap("**", renderText(t.Text))
	case *tg.TextItalic:
		return wrap("*", renderText(t.Text))
	case *tg.TextStrike:
		return wrap("~~", renderText(t.Text))
	case *tg.TextMarked:
		return wrap("==", renderText(t.Text))
	case *tg.TextSpoiler:
		return wrap("||", renderText(t.Text))
	case *tg.TextFixed:
		return wrap("`", renderText(t.Text))
	case *tg.TextUnderline:
		return tagged("u", renderText(t.Text))
	case *tg.TextSubscript:
		return tagged("sub", renderText(t.Text))
	case *tg.TextSuperscript:
		return tagged("sup", renderText(t.Text))
	case *tg.TextURL:
		return link(renderText(t.Text), t.URL)
	case *tg.TextEmail:
		return link(renderText(t.Text), "mailto:"+t.Email)
	case *tg.TextPhone:
		return link(renderText(t.Text), "tel:"+t.Phone)
	case *tg.TextMentionName:
		return link(renderText(t.Text), fmt.Sprintf("tg://user?id=%d", t.UserID))
	case *tg.TextCustomEmoji:
		return t.Alt
	case *tg.TextImage:
		return "[image]"
	case *tg.TextMath:
		return "$" + t.Source + "$"
	default:
		// Anchors, dates, auto-detected links, hashtags and anything added
		// later carry their own text; unwrap it rather than dropping it.
		if inner, ok := text.(interface{ GetText() tg.RichTextClass }); ok {
			return renderText(inner.GetText())
		}

		return ""
	}
}

// wrap applies an inline marker, hoisting any surrounding whitespace out of
// it: Markdown emphasis does not span a marker followed by a space, so
// "** bold**" would render literally, markers included.
//
// Empty text is skipped so that "****" cannot appear in the output.
func wrap(marker, s string) string {
	body := strings.TrimSpace(s)
	if body == "" {
		return s
	}
	lead := s[:strings.Index(s, body)]

	return lead + marker + body + marker + s[len(lead)+len(body):]
}

func tagged(tag, s string) string {
	if s == "" {
		return ""
	}

	return "<" + tag + ">" + s + "</" + tag + ">"
}

func link(text, url string) string {
	if text == "" {
		return url
	}

	return "[" + text + "](" + url + ")"
}
