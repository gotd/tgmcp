package main

import (
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"
)

// sentDocument sends one file of the given kind through a mock invoker and
// returns the uploaded document Telegram would receive.
//
// Asserting on the request is the only way to know what a kind actually sends:
// the attributes are what decide whether the same bytes arrive as a video, an
// animation or a round video message, and Telegram silently downgrades a
// combination it does not accept.
func sentDocument(t *testing.T, kind fileKind, in sendFileInput) *tg.InputMediaUploadedDocument {
	t.Helper()

	var got *tg.InputMediaUploadedDocument
	mock := tgmock.New(t)
	mock.ExpectFunc(func(body bin.Encoder) {
		req, ok := body.(*tg.MessagesSendMediaRequest)
		if !ok {
			t.Fatalf("request is %T, want messages.sendMedia", body)
		}
		doc, ok := req.Media.(*tg.InputMediaUploadedDocument)
		if !ok {
			t.Fatalf("media is %T, want an uploaded document", req.Media)
		}
		got = doc
	}).ThenResult(&tg.Updates{})

	sender := message.NewSender(tg.NewClient(mock))
	src := uploadSource{name: "clip.mp4", mimeType: "video/mp4", release: func() {}}
	f := &tg.InputFile{ID: 1, Parts: 1, Name: src.name}

	if _, err := sender.Self().Media(t.Context(), kind.mediaOption(f, nil, src, in, nil)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got == nil {
		t.Fatal("no uploaded document was sent")
	}

	return got
}

func TestFileKindAttributes(t *testing.T) {
	in := sendFileInput{DurationSeconds: 6, Width: 720, Height: 720}

	t.Run("video", func(t *testing.T) {
		doc := sentDocument(t, fileKindVideo, in)
		v := videoAttr(t, doc)
		if !v.SupportsStreaming {
			t.Error("video: want supports_streaming")
		}
		if v.RoundMessage {
			t.Error("video: round_message must not be set")
		}
		if v.W != 720 || v.H != 720 || v.Duration != 6 {
			t.Errorf("video: got %dx%d %.0fs, want 720x720 6s", v.W, v.H, v.Duration)
		}
		// Without it a silent mp4 comes back as an animation.
		if !doc.NosoundVideo {
			t.Error("video: want nosound_video, so a silent file stays a video")
		}
		if fileName(doc) != "clip.mp4" {
			t.Errorf("video: file name %q, want clip.mp4", fileName(doc))
		}
	})

	// tdlib sends round_message with nosound_video, see VideoNotesManager.
	t.Run("video_note is round and unnamed", func(t *testing.T) {
		doc := sentDocument(t, fileKindVideoNote, in)
		if !videoAttr(t, doc).RoundMessage {
			t.Error("video_note: want round_message")
		}
		if !doc.NosoundVideo {
			t.Error("video_note: want nosound_video, as tdlib sends")
		}
		if n := fileName(doc); n != "" {
			t.Errorf("video_note: file name %q, want none", n)
		}
	})

	t.Run("gif is animated and not nosound", func(t *testing.T) {
		doc := sentDocument(t, fileKindGIF, in)
		if !hasAttr[*tg.DocumentAttributeAnimated](doc) {
			t.Error("gif: want the animated attribute, as tdesktop sends")
		}
		// nosound_video means "send as a video even without audio", and is
		// documented to suppress documentAttributeAnimated when set.
		if doc.NosoundVideo {
			t.Error("gif: nosound_video suppresses the animation")
		}
		// gotd's GIF() helper forces image/gif, under which an mp4 is an
		// unreadable document.
		if doc.MimeType != "video/mp4" {
			t.Errorf("gif: mime %q, want video/mp4", doc.MimeType)
		}
		if v := videoAttr(t, doc); v.SupportsStreaming {
			t.Error("gif: tdlib does not mark an animation streamable")
		}
	})

	t.Run("voice is unnamed", func(t *testing.T) {
		doc := sentDocument(t, fileKindVoice, in)
		a := audioAttr(t, doc)
		if !a.Voice {
			t.Error("voice: want the voice flag")
		}
		if n := fileName(doc); n != "" {
			t.Errorf("voice: file name %q, want none", n)
		}
	})

	t.Run("document is forced", func(t *testing.T) {
		doc := sentDocument(t, fileKindDocument, in)
		if !doc.ForceFile {
			t.Error("document: want force_file")
		}
		if hasAttr[*tg.DocumentAttributeVideo](doc) {
			t.Error("document: must carry no video attribute")
		}
	})
}

func videoAttr(t *testing.T, doc *tg.InputMediaUploadedDocument) *tg.DocumentAttributeVideo {
	t.Helper()
	for _, a := range doc.Attributes {
		if v, ok := a.(*tg.DocumentAttributeVideo); ok {
			return v
		}
	}
	t.Fatal("no video attribute")

	return nil
}

func audioAttr(t *testing.T, doc *tg.InputMediaUploadedDocument) *tg.DocumentAttributeAudio {
	t.Helper()
	for _, a := range doc.Attributes {
		if v, ok := a.(*tg.DocumentAttributeAudio); ok {
			return v
		}
	}
	t.Fatal("no audio attribute")

	return nil
}

func fileName(doc *tg.InputMediaUploadedDocument) string {
	for _, a := range doc.Attributes {
		if v, ok := a.(*tg.DocumentAttributeFilename); ok {
			return v.FileName
		}
	}

	return ""
}

func hasAttr[T tg.DocumentAttributeClass](doc *tg.InputMediaUploadedDocument) bool {
	for _, a := range doc.Attributes {
		if _, ok := a.(T); ok {
			return true
		}
	}

	return false
}
