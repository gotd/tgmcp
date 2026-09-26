package main

import (
	"testing"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

func TestParseAttributionMode(t *testing.T) {
	for _, tt := range []struct {
		name    string
		in      string
		want    attributionMode
		wantErr bool
	}{
		{name: "empty", in: "", want: attributionOff},
		{name: "off", in: "off", want: attributionOff},
		{name: "footer", in: "footer", want: attributionFooter},
		{name: "bot", in: "bot", want: attributionBot},
		{name: "uppercase", in: "Bot", want: attributionBot},
		{name: "padded", in: "  footer ", want: attributionFooter},
		{name: "unknown", in: "channel", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAttributionMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseAttributionMode(%q): want error, got nil", tt.in)
				}

				return
			}
			if err != nil {
				t.Fatalf("parseAttributionMode(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("mode = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFooterOptions checks that the footer is appended as a trailing italic
// run, leaving the body's own formatting untouched.
func TestFooterOptions(t *testing.T) {
	body := styling.Plain("hello")

	var b entity.Builder
	if err := styling.Perform(&b, footerOptions(body, "sent by an agent")...); err != nil {
		t.Fatalf("perform: %v", err)
	}
	text, entities := b.Complete()

	if want := "hello\n\nsent by an agent"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if len(entities) != 1 {
		t.Fatalf("entities = %d, want 1", len(entities))
	}
	it, ok := entities[0].(*tg.MessageEntityItalic)
	if !ok {
		t.Fatalf("entity = %T, want *tg.MessageEntityItalic", entities[0])
	}
	if it.Offset != len("hello\n\n") || it.Length != len("sent by an agent") {
		t.Errorf("italic range = (%d,%d), want (%d,%d)",
			it.Offset, it.Length, len("hello\n\n"), len("sent by an agent"))
	}
}

// TestFooterOptionsEmpty checks that an empty footer adds nothing, so an
// operator can disable the marker without disabling attribution handling.
func TestFooterOptionsEmpty(t *testing.T) {
	var b entity.Builder
	if err := styling.Perform(&b, footerOptions(styling.Plain("hello"), "")...); err != nil {
		t.Fatalf("perform: %v", err)
	}
	text, entities := b.Complete()

	if text != "hello" {
		t.Errorf("text = %q, want %q", text, "hello")
	}
	if len(entities) != 0 {
		t.Errorf("entities = %d, want 0", len(entities))
	}
}

func TestBotIDFromToken(t *testing.T) {
	for _, tt := range []struct {
		name    string
		token   string
		want    int64
		wantErr bool
	}{
		{name: "valid", token: "123456:AAHverysecret", want: 123456},
		{name: "no colon", token: "123456", wantErr: true},
		{name: "non numeric id", token: "abc:AAH", wantErr: true},
		{name: "empty", token: "", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := botIDFromToken(tt.token)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("botIDFromToken(%q): want error, got nil", tt.token)
				}

				return
			}
			if err != nil {
				t.Fatalf("botIDFromToken(%q): %v", tt.token, err)
			}
			if got != tt.want {
				t.Errorf("id = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestStyledCaptionAttribution checks that uploads fall back to the footer
// even in bot mode, since an inline result cannot carry a local file.
func TestStyledCaptionAttribution(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode attributionMode
		want attributionMode
	}{
		{name: "off", mode: attributionOff, want: attributionOff},
		{name: "footer", mode: attributionFooter, want: attributionFooter},
		{name: "bot degrades", mode: attributionBot, want: attributionFooter},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := &server{attribution: tt.mode, footer: "agent", strict: true}

			opts, got, err := srv.styledCaption("hi", "")
			if err != nil {
				t.Fatalf("styledCaption: %v", err)
			}
			if got != tt.want {
				t.Errorf("attribution = %q, want %q", got, tt.want)
			}
			if len(opts) == 0 {
				t.Error("opts is empty")
			}
		})
	}
}

func TestInlineQueryAllowed(t *testing.T) {
	const (
		owner   = int64(100)
		second  = int64(200)
		outside = int64(300)
	)

	for _, tt := range []struct {
		name    string
		allowed map[int64]bool
		owner   int64
		user    int64
		want    bool
	}{
		{name: "owner without list", owner: owner, user: owner, want: true},
		{name: "stranger without list", owner: owner, user: outside, want: false},
		{
			name:    "listed user",
			allowed: map[int64]bool{second: true},
			owner:   owner,
			user:    second,
			want:    true,
		},
		{
			name:    "unlisted user",
			allowed: map[int64]bool{second: true},
			owner:   owner,
			user:    outside,
			want:    false,
		},
		{
			name:    "owner still allowed alongside list",
			allowed: map[int64]bool{second: true},
			owner:   owner,
			user:    owner,
			want:    true,
		},
		// A payload written before owners were recorded must not make every
		// user look like the owner.
		{name: "no owner recorded", owner: 0, user: 0, want: false},
		{name: "no owner recorded, stranger", owner: 0, user: outside, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := inlineQueryAllowed(tt.allowed, tt.owner, tt.user); got != tt.want {
				t.Errorf("inlineQueryAllowed(%v, %d, %d) = %v, want %v",
					tt.allowed, tt.owner, tt.user, got, tt.want)
			}
		})
	}
}

func TestParseUserIDs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		in      string
		want    []int64
		wantErr bool
	}{
		{name: "empty", in: ""},
		{name: "single", in: "123", want: []int64{123}},
		{name: "multiple", in: "123,456", want: []int64{123, 456}},
		{name: "spaces and trailing comma", in: " 123 , 456 , ", want: []int64{123, 456}},
		{name: "negative", in: "-100123", want: []int64{-100123}},
		{name: "not a number", in: "123,abc", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUserIDs(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseUserIDs(%q): want error, got nil", tt.in)
				}

				return
			}
			if err != nil {
				t.Fatalf("parseUserIDs(%q): %v", tt.in, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ids = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ids = %v, want %v", got, tt.want)

					break
				}
			}
		})
	}
}
