package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

func plain(s string) tg.RichTextClass { return &tg.TextPlain{Text: s} }

func cell(s string, header bool) tg.PageTableCell {
	return tg.PageTableCell{Header: header, AlignCenter: true, Text: plain(s)}
}

func TestRenderRich(t *testing.T) {
	for _, tt := range []struct {
		name   string
		blocks []tg.PageBlockClass
		want   string
	}{
		{
			name:   "heading",
			blocks: []tg.PageBlockClass{&tg.PageBlockHeading2{Text: plain("Но есть нюанс")}},
			want:   "## Но есть нюанс",
		},
		{
			name: "inline styles",
			blocks: []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: []tg.RichTextClass{
				plain("a "),
				&tg.TextBold{Text: plain("b")},
				plain(" "),
				&tg.TextURL{Text: plain("c"), URL: "https://go.dev"},
			}}}},
			want: "a **b** [c](https://go.dev)",
		},
		{
			name: "table, as posted by @vpn_liberty",
			blocks: []tg.PageBlockClass{&tg.PageBlockTable{
				Bordered: true,
				Title:    &tg.TextEmpty{},
				Rows: []tg.PageTableRow{
					{Cells: []tg.PageTableCell{cell("Регион", true), cell("Процент", true)}},
					{Cells: []tg.PageTableCell{cell("Брянская", false), cell("9.5%", false)}},
					{Cells: []tg.PageTableCell{cell("Курская", false), cell("13.1%", false)}},
				},
			}},
			want: "| Регион | Процент |\n| :---: | :---: |\n| Брянская | 9.5% |\n| Курская | 13.1% |",
		},
		{
			name: "table without a header row gets one",
			blocks: []tg.PageBlockClass{&tg.PageBlockTable{
				Title: &tg.TextEmpty{},
				Rows: []tg.PageTableRow{
					{Cells: []tg.PageTableCell{{Text: plain("a")}, {Text: plain("b")}}},
				},
			}},
			want: "|  |  |\n| --- | --- |\n| a | b |",
		},
		{
			name: "cell pipes and newlines are flattened",
			blocks: []tg.PageBlockClass{&tg.PageBlockTable{
				Title: &tg.TextEmpty{},
				Rows: []tg.PageTableRow{
					{Cells: []tg.PageTableCell{cell("a|b\nc", true)}},
				},
			}},
			want: `| a\|b c |` + "\n| :---: |",
		},
		{
			// Telegram lets a style span its own padding; Markdown does not,
			// so the whitespace has to move outside the markers.
			name: "whitespace is hoisted out of emphasis",
			blocks: []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: []tg.RichTextClass{
				plain("и"),
				&tg.TextBold{Text: plain(" снова")},
				&tg.TextBold{Text: plain("\nна новой строке\n")},
			}}}},
			want: "и **снова**\n**на новой строке**\n",
		},
		{
			name:   "blank emphasis is left alone",
			blocks: []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextBold{Text: plain("  ")}}},
			want:   "  ",
		},
		{
			name: "checklist",
			blocks: []tg.PageBlockClass{&tg.PageBlockList{Items: []tg.PageListItemClass{
				&tg.PageListItemText{Checkbox: true, Checked: true, Text: plain("done")},
				&tg.PageListItemText{Checkbox: true, Text: plain("todo")},
				&tg.PageListItemText{Text: plain("plain")},
			}}},
			want: "- [x] done\n- [ ] todo\n- plain",
		},
		{
			name: "ordered list keeps its own numbering",
			blocks: []tg.PageBlockClass{&tg.PageBlockOrderedList{Items: []tg.PageListOrderedItemClass{
				&tg.PageListOrderedItemText{Num: "4.", Text: plain("four")},
				&tg.PageListOrderedItemText{Text: plain("five")},
			}}},
			want: "4. four\n2. five",
		},
		{
			name: "code block",
			blocks: []tg.PageBlockClass{&tg.PageBlockPreformatted{
				Language: "go", Text: plain("func main() {}"),
			}},
			want: "```go\nfunc main() {}\n```",
		},
		{
			name: "blockquote with caption",
			blocks: []tg.PageBlockClass{&tg.PageBlockBlockquote{
				Text: plain("a\nb"), Caption: plain("source"),
			}},
			want: "> a\n> b\n\nsource",
		},
		{
			name:   "media becomes a placeholder",
			blocks: []tg.PageBlockClass{&tg.PageBlockPhoto{PhotoID: 1}},
			want:   "[photo]",
		},
		{
			name: "blocks are separated by a blank line",
			blocks: []tg.PageBlockClass{
				&tg.PageBlockHeading1{Text: plain("t")},
				&tg.PageBlockParagraph{Text: plain("p")},
				&tg.PageBlockDivider{},
			},
			want: "# t\n\np\n\n---",
		},
		{
			name:   "empty text yields nothing",
			blocks: []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextEmpty{}}},
			want:   "",
		},
		{
			name:   "unknown block is named, not dropped",
			blocks: []tg.PageBlockClass{&tg.PageBlockUnsupported{}},
			want:   "[Unsupported]",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderRich(tt.blocks); got != tt.want {
				t.Errorf("renderRich:\ngot  %q\nwant %q", got, tt.want)
			}
		})
	}
}
