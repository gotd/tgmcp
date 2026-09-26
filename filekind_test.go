package main

import "testing"

func TestParseFileKind(t *testing.T) {
	for _, tt := range []struct {
		in      string
		want    fileKind
		wantErr bool
	}{
		{in: "", want: fileKindAuto},
		{in: "auto", want: fileKindAuto},
		{in: "  VIDEO ", want: fileKindVideo},
		{in: "video_note", want: fileKindVideoNote},
		{in: "voice", want: fileKindVoice},
		{in: "sticker", want: fileKindSticker},
		{in: "as_photo", wantErr: true},
		{in: "movie", wantErr: true},
	} {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseFileKind(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseFileKind(%q): want error, got %q", tt.in, got)
				}

				return
			}
			if err != nil {
				t.Fatalf("parseFileKind(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseFileKind(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFileKindResolve(t *testing.T) {
	for _, tt := range []struct {
		name     string
		kind     fileKind
		mimeType string
		want     fileKind
	}{
		{name: "video", kind: fileKindAuto, mimeType: "video/mp4", want: fileKindVideo},
		{name: "photo", kind: fileKindAuto, mimeType: "image/png", want: fileKindPhoto},
		{name: "gif is an animation", kind: fileKindAuto, mimeType: "image/gif", want: fileKindGIF},
		{name: "audio", kind: fileKindAuto, mimeType: "audio/mpeg", want: fileKindAudio},
		{name: "with parameters", kind: fileKindAuto, mimeType: "text/plain; charset=utf-8", want: fileKindDocument},
		{name: "uppercase", kind: fileKindAuto, mimeType: "VIDEO/MP4", want: fileKindVideo},
		{name: "unknown", kind: fileKindAuto, mimeType: "application/zip", want: fileKindDocument},
		{name: "empty", kind: fileKindAuto, mimeType: "", want: fileKindDocument},
		// Never inferred: these say how the sender means the bytes, not what
		// they are, and guessing wrong is not something the caller can undo.
		{name: "voice is not inferred", kind: fileKindAuto, mimeType: "audio/ogg", want: fileKindAudio},
		// An explicit kind always wins, however the bytes look.
		{name: "explicit beats mime", kind: fileKindDocument, mimeType: "video/mp4", want: fileKindDocument},
		{name: "explicit voice", kind: fileKindVoice, mimeType: "audio/ogg", want: fileKindVoice},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.kind.resolve(tt.mimeType); got != tt.want {
				t.Errorf("%q.resolve(%q) = %q, want %q", tt.kind, tt.mimeType, got, tt.want)
			}
		})
	}
}
