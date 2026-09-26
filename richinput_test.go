package main

import (
	"context"
	"strings"
	"testing"
)

func TestIsRichMode(t *testing.T) {
	for _, tt := range []struct {
		mode string
		want bool
	}{
		{"rich_markdown", true},
		{"RICH_HTML", true},
		{"  rich_markdown ", true},
		{"markdown", false},
		{"html", false},
		{"", false},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			if got := isRichMode(tt.mode); got != tt.want {
				t.Errorf("isRichMode(%q) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestRichWithFooter(t *testing.T) {
	for _, tt := range []struct {
		name, mode, footer, want string
	}{
		{name: "markdown", mode: "rich_markdown", footer: "by an agent", want: "# t\n\n_by an agent_"},
		{name: "html", mode: "rich_html", footer: "by an agent", want: "# t\n<p><i>by an agent</i></p>"},
		{name: "html escapes", mode: "rich_html", footer: "a<b>", want: "# t\n<p><i>a&lt;b&gt;</i></p>"},
		{name: "empty footer", mode: "rich_markdown", footer: "", want: "# t"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := richWithFooter("# t", tt.mode, tt.footer); got != tt.want {
				t.Errorf("richWithFooter: got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPreviewRichTable is the round trip that matters: a Markdown table must
// parse into a real table block, not into a paragraph of pipes.
func TestPreviewRichTable(t *testing.T) {
	srv := &server{}
	src := "# Title\n\n| Регион | Процент |\n| --- | ---: |\n| Брянская | 9.5% |\n| Курская | 13.1% |\n"

	_, out, err := srv.handlePreviewFormat(context.Background(), nil, previewFormatInput{
		Text: src, ParseMode: "rich_markdown",
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if out.Kind != "rich" {
		t.Errorf("kind: got %q, want %q", out.Kind, "rich")
	}
	if len(out.Entities) != 0 {
		t.Errorf("entities: got %#v, want none", out.Entities)
	}
	if out.Note == "" {
		t.Error("note: want the local-parsing caveat, got none")
	}

	want := []PreviewBlock{
		{Type: "heading1", Text: "# Title"},
		{Type: "table", Rows: 3, Columns: 2},
	}
	if len(out.Blocks) != len(want) {
		t.Fatalf("blocks: got %#v, want %#v", out.Blocks, want)
	}
	for i, w := range want {
		if out.Blocks[i] != w {
			t.Errorf("block %d: got %#v, want %#v", i, out.Blocks[i], w)
		}
	}
	if !strings.Contains(out.Text, "| Брянская | 9.5% |") {
		t.Errorf("text: table row missing from %q", out.Text)
	}
}
