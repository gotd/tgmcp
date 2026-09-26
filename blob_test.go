package main

import (
	"strings"
	"testing"

	"github.com/go-faster/gooners/blob"
)

// TestBlobMountPath checks that the handler is mounted where the URLs it hands
// out actually resolve. A mismatch produces links that 404, which is the
// failure the blob package exists to prevent.
func TestBlobMountPath(t *testing.T) {
	for _, tt := range []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{name: "path", baseURL: "http://127.0.0.1:8080/blob", want: "/blob/"},
		{name: "trailing slash kept", baseURL: "http://127.0.0.1:8080/blob/", want: "/blob/"},
		{name: "nested path", baseURL: "https://mcp.example.com/tg/files", want: "/tg/files/"},
		{name: "no path", baseURL: "http://127.0.0.1:8080", want: blobPathPrefix},
		{name: "root path", baseURL: "http://127.0.0.1:8080/", want: blobPathPrefix},
		{name: "invalid", baseURL: "://nope", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := blobMountPath(tt.baseURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("blobMountPath(%q): want error, got nil", tt.baseURL)
				}

				return
			}
			if err != nil {
				t.Fatalf("blobMountPath(%q): %v", tt.baseURL, err)
			}
			if got != tt.want {
				t.Errorf("blobMountPath(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}

// TestNewBlobStoreDenies checks that an unconfigured base URL yields a store
// that fails loudly, rather than one handing out URLs that resolve nowhere.
func TestNewBlobStoreDenies(t *testing.T) {
	s, err := newBlobStore(t.Context(), Config{}, nil)
	if err != nil {
		t.Fatalf("newBlobStore: %v", err)
	}
	if _, _, err := s.Open(t.Context(), "anything"); err == nil {
		t.Error("Open on an unconfigured store: want error, got nil")
	}
}

// TestSendFileSourceIsExclusive checks the argument validation of send_file:
// a path and a blob id name different bytes, so accepting both would mean
// silently ignoring one.
func TestSendFileSourceIsExclusive(t *testing.T) {
	srv := &server{fileRootVal: t.TempDir()}

	for _, tt := range []struct {
		name    string
		in      sendFileInput
		wantErr string
	}{
		{name: "no chat", in: sendFileInput{Path: "a.txt"}, wantErr: "chat is required"},
		{name: "neither", in: sendFileInput{Chat: "me"}, wantErr: "exactly one"},
		{
			name:    "both",
			in:      sendFileInput{Chat: "me", Path: "a.txt", Source: blob.Source{Blob: "tgmcp/x"}},
			wantErr: "exactly one",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := srv.handleSendFile(t.Context(), nil, tt.in)
			if err == nil {
				t.Fatalf("handleSendFile(%#v): want error, got nil", tt.in)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestUploadSourceFromBlob checks that a stored object can be uploaded back,
// which is what makes another server's output usable as a Telegram upload.
func TestUploadSourceFromBlob(t *testing.T) {
	store, err := blob.NewHTTP(blob.HTTPOptions{
		BaseURL: "http://127.0.0.1:0/blob",
		FS:      blob.Dir(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	const body = "uploaded from a blob"
	b, err := store.Put(t.Context(), strings.NewReader(body), blob.PutOptions{
		Name:     "note.txt",
		MIMEType: "text/plain",
		Size:     int64(len(body)),
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	srv := &server{blobs: store}
	src, err := srv.uploadSource(t.Context(), sendFileInput{Chat: "me", Source: blob.Source{Blob: b.ID}})
	if err != nil {
		t.Fatalf("uploadSource: %v", err)
	}
	defer src.release()
	if src.option == nil {
		t.Fatal("uploadSource returned no upload option")
	}
	// The name and type must survive the round trip: Telegram shows a document
	// with no filename as its numeric id, which is what "Unknown Track" is.
	if src.name != "note.txt" {
		t.Errorf("name: got %q, want %q", src.name, "note.txt")
	}
	if src.mimeType != "text/plain" {
		t.Errorf("mime type: got %q, want %q", src.mimeType, "text/plain")
	}

	if _, err := srv.uploadSource(t.Context(), sendFileInput{Chat: "me", Source: blob.Source{Blob: "no-such-blob"}}); err == nil {
		t.Error("uploadSource on an unknown id: want error, got nil")
	}
}
