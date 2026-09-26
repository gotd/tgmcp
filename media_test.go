package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestLargestPhotoSize(t *testing.T) {
	tests := []struct {
		name    string
		sizes   []tg.PhotoSizeClass
		wantOK  bool
		wantTyp string
	}{
		{name: "empty", sizes: nil, wantOK: false},
		{
			name: "skips unsupported variants",
			sizes: []tg.PhotoSizeClass{
				&tg.PhotoCachedSize{Type: "s"},
				&tg.PhotoStrippedSize{Type: "i"},
			},
			wantOK: false,
		},
		{
			name: "picks largest area",
			sizes: []tg.PhotoSizeClass{
				&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 100},
				&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: 5000},
				&tg.PhotoSize{Type: "m", W: 320, H: 240, Size: 800},
			},
			wantOK:  true,
			wantTyp: "x",
		},
		{
			name: "considers progressive sizes",
			sizes: []tg.PhotoSizeClass{
				&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 100},
				&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 960, Sizes: []int{100, 500, 9000}},
			},
			wantOK:  true,
			wantTyp: "y",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := largestPhotoSize(tt.sizes)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Type != tt.wantTyp {
				t.Errorf("type = %q, want %q", got.Type, tt.wantTyp)
			}
		})
	}
}

func TestDocumentFileName(t *testing.T) {
	tests := []struct {
		name string
		doc  *tg.Document
		want string
	}{
		{
			name: "uses explicit filename attribute",
			doc: &tg.Document{
				ID:         42,
				MimeType:   "application/pdf",
				Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "report.pdf"}},
			},
			want: "report.pdf",
		},
		{
			name: "falls back to id and guessed extension",
			doc:  &tg.Document{ID: 7, MimeType: "image/png"},
			want: "7.png",
		},
		{
			name: "falls back to id without extension for unknown mime",
			doc:  &tg.Document{ID: 9, MimeType: "application/x-does-not-exist"},
			want: "9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := documentFileName(tt.doc); got != tt.want {
				t.Errorf("documentFileName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// withMedia attaches media to a message via SetMedia, which also sets the
// conditional-field flag that GetMedia checks.
func withMedia(msg *tg.Message, media tg.MessageMediaClass) *tg.Message {
	msg.SetMedia(media)
	return msg
}

func TestMediaLocation(t *testing.T) {
	tests := []struct {
		name        string
		msg         *tg.Message
		wantErr     bool
		wantName    string
		wantMime    string
		wantMediaID int64
	}{
		{
			name:    "no media",
			msg:     &tg.Message{ID: 1},
			wantErr: true,
		},
		{
			name: "photo",
			msg: withMedia(&tg.Message{ID: 2}, &tg.MessageMediaPhoto{
				Photo: &tg.Photo{
					ID: 555,
					Sizes: []tg.PhotoSizeClass{
						&tg.PhotoSize{Type: "x", W: 800, H: 600, Size: 1234},
					},
				},
			}),
			wantName:    "555.jpg",
			wantMime:    "image/jpeg",
			wantMediaID: 555,
		},
		{
			name: "document",
			msg: withMedia(&tg.Message{ID: 3}, &tg.MessageMediaDocument{
				Document: &tg.Document{
					ID:         777,
					MimeType:   "text/plain",
					Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "notes.txt"}},
				},
			}),
			wantName:    "notes.txt",
			wantMime:    "text/plain",
			wantMediaID: 777,
		},
		{
			name:    "unsupported media",
			msg:     withMedia(&tg.Message{ID: 4}, &tg.MessageMediaGeo{}),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, name, mimeType, _, err := mediaLocation(tt.msg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if mimeType != tt.wantMime {
				t.Errorf("mime = %q, want %q", mimeType, tt.wantMime)
			}
			switch l := loc.(type) {
			case *tg.InputPhotoFileLocation:
				if l.ID != tt.wantMediaID {
					t.Errorf("photo id = %d, want %d", l.ID, tt.wantMediaID)
				}
			case *tg.InputDocumentFileLocation:
				if l.ID != tt.wantMediaID {
					t.Errorf("document id = %d, want %d", l.ID, tt.wantMediaID)
				}
			default:
				t.Fatalf("unexpected location type %T", loc)
			}
		})
	}
}

// TestGetFileInlineGate verifies that inline media is refused before any
// Telegram request is made when the right is not granted.
func TestGetFileInlineGate(t *testing.T) {
	srv := &server{api: nil, fileRootVal: t.TempDir()}
	_, _, err := srv.handleGetFile(context.Background(), nil, getFileInput{
		Chat:      "me",
		MessageID: 1,
		Inline:    true,
	})
	if err == nil {
		t.Fatal("inline download allowed without TG_ALLOW_INLINE_MEDIA")
	}
	if !strings.Contains(err.Error(), "TG_ALLOW_INLINE_MEDIA") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestVisionPhotoSize(t *testing.T) {
	// The variants Telegram typically offers for a photo.
	full := []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "s", W: 100, H: 75},
		&tg.PhotoSize{Type: "m", W: 320, H: 240},
		&tg.PhotoSize{Type: "x", W: 800, H: 600},
		&tg.PhotoSize{Type: "y", W: 1280, H: 960},
		&tg.PhotoSize{Type: "w", W: 2560, H: 1920},
	}
	tests := []struct {
		name    string
		sizes   []tg.PhotoSizeClass
		wantOK  bool
		wantTyp string
	}{
		{name: "empty", sizes: nil, wantOK: false},
		{name: "smallest that reaches the target", sizes: full, wantOK: true, wantTyp: "y"},
		{
			name: "falls back to largest when all are below target",
			sizes: []tg.PhotoSizeClass{
				&tg.PhotoSize{Type: "s", W: 100, H: 75},
				&tg.PhotoSize{Type: "x", W: 800, H: 600},
			},
			wantOK:  true,
			wantTyp: "x",
		},
		{
			name: "long edge counts, not width",
			sizes: []tg.PhotoSizeClass{
				&tg.PhotoSize{Type: "x", W: 600, H: 800},
				&tg.PhotoSize{Type: "y", W: 960, H: 1280},
			},
			wantOK:  true,
			wantTyp: "y",
		},
		{
			name: "skips variants with no downloadable file",
			sizes: []tg.PhotoSizeClass{
				&tg.PhotoStrippedSize{Type: "i"},
				&tg.PhotoSize{Type: "x", W: 800, H: 600},
			},
			wantOK:  true,
			wantTyp: "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := visionPhotoSize(tt.sizes)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Type != tt.wantTyp {
				t.Errorf("type = %q, want %q", got.Type, tt.wantTyp)
			}
		})
	}

	// The inline pick must not silently become the biggest variant.
	if got, _ := largestPhotoSize(full); got.Type != "w" {
		t.Errorf("largestPhotoSize = %q, want %q", got.Type, "w")
	}
}

// TestDocumentKind checks that a document is named by what it is. Everything
// that is not a photo arrives as a document, so the attributes are the only
// thing separating a video from a PDF.
func TestDocumentKind(t *testing.T) {
	for _, tt := range []struct {
		name  string
		attrs []tg.DocumentAttributeClass
		want  string
	}{
		{name: "plain", want: "document"},
		{
			name:  "video",
			attrs: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{W: 320, H: 320}},
			want:  "video",
		},
		{
			name:  "round video beats video",
			attrs: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{RoundMessage: true}},
			want:  "video_note",
		},
		{
			name: "animation beats video",
			attrs: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{},
				&tg.DocumentAttributeAnimated{},
			},
			want: "gif",
		},
		{
			name:  "audio",
			attrs: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Title: "t"}},
			want:  "audio",
		},
		{
			name:  "voice beats audio",
			attrs: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}},
			want:  "voice",
		},
		{
			name: "sticker beats everything",
			attrs: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{},
				&tg.DocumentAttributeSticker{},
			},
			want: "sticker",
		},
		{
			name:  "filename alone is a document",
			attrs: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "a.mp4"}},
			want:  "document",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := documentKind(&tg.Document{Attributes: tt.attrs}); got != tt.want {
				t.Errorf("documentKind() = %q, want %q", got, tt.want)
			}
		})
	}
}
