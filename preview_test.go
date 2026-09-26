package main

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

func TestPreviewFormat(t *testing.T) {
	for _, tt := range []struct {
		name     string
		srv      *server
		in       previewFormatInput
		wantText string
		wantEnts []TextEntity
		wantLen  int
		wantAttr string
		wantErr  bool
	}{
		{
			name:     "plain keeps markup literal",
			in:       previewFormatInput{Text: "**bold**"},
			wantText: "**bold**",
			wantEnts: []TextEntity{},
			wantLen:  8,
			wantAttr: "off",
		},
		{
			name:     "markdown",
			in:       previewFormatInput{Text: "a **b** c", ParseMode: "markdown"},
			wantText: "a b c",
			wantEnts: []TextEntity{{Type: "bold", Offset: 2, Length: 1, Text: "b"}},
			wantLen:  5,
			wantAttr: "off",
		},
		{
			name:     "html link",
			in:       previewFormatInput{Text: `see <a href="https://go.dev">go</a>`, ParseMode: "html"},
			wantText: "see go",
			wantEnts: []TextEntity{{Type: "text_url", Offset: 4, Length: 2, Text: "go", URL: "https://go.dev"}},
			wantLen:  6,
			wantAttr: "off",
		},
		{
			name:     "offsets are utf-16 units",
			in:       previewFormatInput{Text: "🙂 **b**", ParseMode: "markdown"},
			wantText: "🙂 b",
			wantEnts: []TextEntity{{Type: "bold", Offset: 3, Length: 1, Text: "b"}},
			wantLen:  4,
			wantAttr: "off",
		},
		{
			name:     "footer attribution is included",
			srv:      &server{attribution: attributionFooter, footer: "by an agent"},
			in:       previewFormatInput{Text: "hi"},
			wantText: "hi\n\nby an agent",
			wantEnts: []TextEntity{{Type: "italic", Offset: 4, Length: 11, Text: "by an agent"}},
			wantLen:  15,
			wantAttr: "footer",
		},
		{
			name:     "bot attribution leaves the text alone",
			srv:      &server{attribution: attributionBot, footer: "by an agent"},
			in:       previewFormatInput{Text: "hi"},
			wantText: "hi",
			wantEnts: []TextEntity{},
			wantLen:  2,
			wantAttr: "bot",
		},
		{
			name:    "empty text",
			in:      previewFormatInput{},
			wantErr: true,
		},
		{
			name:    "unknown parse mode",
			in:      previewFormatInput{Text: "hi", ParseMode: "bbcode"},
			wantErr: true,
		},
		{
			name:    "malformed html",
			in:      previewFormatInput{Text: "oops</b>", ParseMode: "html"},
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := tt.srv
			if srv == nil {
				srv = &server{}
			}
			_, out, err := srv.handlePreviewFormat(context.Background(), nil, tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("handlePreviewFormat(%#v): want error, got %#v", tt.in, out)
				}

				return
			}
			if err != nil {
				t.Fatalf("handlePreviewFormat(%#v): %v", tt.in, err)
			}
			if out.Text != tt.wantText {
				t.Errorf("text: got %q, want %q", out.Text, tt.wantText)
			}
			if out.Length != tt.wantLen {
				t.Errorf("length: got %d, want %d", out.Length, tt.wantLen)
			}
			if out.Attribution != tt.wantAttr {
				t.Errorf("attribution: got %q, want %q", out.Attribution, tt.wantAttr)
			}
			if len(out.Entities) != len(tt.wantEnts) {
				t.Fatalf("entities: got %#v, want %#v", out.Entities, tt.wantEnts)
			}
			for i, want := range tt.wantEnts {
				if out.Entities[i] != want {
					t.Errorf("entity %d: got %#v, want %#v", i, out.Entities[i], want)
				}
			}
		})
	}
}

func TestEntityType(t *testing.T) {
	for _, tt := range []struct {
		entity tg.MessageEntityClass
		want   string
	}{
		{&tg.MessageEntityBold{}, "bold"},
		{&tg.MessageEntityTextURL{}, "text_url"},
		{&tg.MessageEntityMentionName{}, "mention_name"},
		{&tg.MessageEntityCustomEmoji{}, "custom_emoji"},
		{&tg.MessageEntityPre{}, "pre"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			if got := entityType(tt.entity); got != tt.want {
				t.Errorf("entityType(%s): got %q, want %q", tt.entity.TypeName(), got, tt.want)
			}
		})
	}
}
