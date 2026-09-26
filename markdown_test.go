package main

import (
	"reflect"
	"testing"

	"go.uber.org/zap"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// render applies the option produced by styledText and returns the resulting
// message text with its entities.
func render(t *testing.T, s *server, text, mode string) (string, []tg.MessageEntityClass) {
	t.Helper()

	opt, err := s.styledText(text, mode)
	if err != nil {
		t.Fatalf("styled text: %v", err)
	}

	var b entity.Builder
	if err := styling.Perform(&b, opt); err != nil {
		t.Fatalf("perform: %v", err)
	}
	out, entities := b.Complete()

	return out, entities
}

func TestStyledTextMode(t *testing.T) {
	srv := &server{}

	for _, tt := range []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{name: "empty defaults to plain", mode: ""},
		{name: "plain", mode: "plain"},
		{name: "markdown", mode: "markdown"},
		{name: "uppercase", mode: "Markdown"},
		{name: "padded", mode: "  markdown "},
		{name: "html", mode: "html"},
		// Rich modes are valid parse modes, but not for styled text: they
		// build page blocks, which only a rich message can carry.
		{name: "rich markdown", mode: "rich_markdown", wantErr: true},
		{name: "rich html", mode: "rich_html", wantErr: true},
		{name: "unknown", mode: "bbcode", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.styledText("hi", tt.mode)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("styledText(%q): want error, got nil", tt.mode)
				}

				return
			}
			if err != nil {
				t.Fatalf("styledText(%q): %v", tt.mode, err)
			}
		})
	}
}

// TestStyledTextPlain checks that plain mode sends Markdown syntax verbatim,
// which is what keeps existing callers working.
func TestStyledTextPlain(t *testing.T) {
	srv := &server{}

	const src = "**bold** and _italic_ and `code`"
	text, entities := render(t, srv, src, "")
	if text != src {
		t.Errorf("text = %q, want %q", text, src)
	}
	if len(entities) != 0 {
		t.Errorf("entities = %d, want 0", len(entities))
	}
}

func TestStyledTextMarkdown(t *testing.T) {
	srv := &server{}

	for _, tt := range []struct {
		name     string
		src      string
		wantText string
		wantType any
	}{
		{name: "bold", src: "**bold**", wantText: "bold", wantType: &tg.MessageEntityBold{}},
		{name: "italic", src: "_italic_", wantText: "italic", wantType: &tg.MessageEntityItalic{}},
		{name: "strike", src: "~~gone~~", wantText: "gone", wantType: &tg.MessageEntityStrike{}},
		{name: "code", src: "`code`", wantText: "code", wantType: &tg.MessageEntityCode{}},
		{
			name:     "link",
			src:      "[site](https://example.com)",
			wantText: "site",
			wantType: &tg.MessageEntityTextURL{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text, entities := render(t, srv, tt.src, "markdown")
			if text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
			if len(entities) != 1 {
				t.Fatalf("entities = %d, want 1", len(entities))
			}
			if got, want := entities[0], tt.wantType; !sameType(got, want) {
				t.Errorf("entity = %T, want %T", got, want)
			}
		})
	}
}

// TestStyledTextMarkdownFencedCode covers the multi-line case, where the
// language tag has to survive into the pre entity.
func TestStyledTextMarkdownFencedCode(t *testing.T) {
	srv := &server{}

	text, entities := render(t, srv, "```go\nfmt.Println(\"hi\")\n```", "markdown")
	if want := "fmt.Println(\"hi\")"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if len(entities) != 1 {
		t.Fatalf("entities = %d, want 1", len(entities))
	}
	pre, ok := entities[0].(*tg.MessageEntityPre)
	if !ok {
		t.Fatalf("entity = %T, want *tg.MessageEntityPre", entities[0])
	}
	if pre.Language != "go" {
		t.Errorf("language = %q, want %q", pre.Language, "go")
	}
}

// TestStyledTextMarkdownUnsupported documents that constructs without a
// Telegram entity keep their Markdown markers instead of being dropped.
func TestStyledTextMarkdownUnsupported(t *testing.T) {
	srv := &server{}

	text, entities := render(t, srv, "# Heading\n\n- item\n", "markdown")
	if want := "# Heading\n\n- item"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if len(entities) != 0 {
		t.Errorf("entities = %d, want 0", len(entities))
	}
}

// TestResolveUserUncached checks that a mention of an unknown user fails
// loudly rather than sending a mention with a zero access hash.
func TestResolveUserUncached(t *testing.T) {
	srv := &server{cache: newDialogCache(nil, zap.NewNop())}

	if _, err := srv.resolveUser(42); err == nil {
		t.Fatal("resolveUser(42): want error, got nil")
	}
}

func sameType(a, b any) bool {
	return reflect.TypeOf(a) == reflect.TypeOf(b)
}
