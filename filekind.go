package main

import (
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// fileKind selects how Telegram renders an uploaded file. The same bytes
// arrive as a plain attachment, a playable video, a looping animation or a
// round video message depending only on the attributes sent with them.
type fileKind string

const (
	// fileKindAuto picks a kind from the MIME type, see [fileKind.resolve].
	fileKindAuto fileKind = "auto"
	// fileKindDocument is a plain attachment, whatever the bytes are.
	fileKindDocument fileKind = "document"
	fileKindPhoto    fileKind = "photo"
	fileKindVideo    fileKind = "video"
	// fileKindGIF is an animation: on Telegram that is a soundless, looping
	// mp4, and a real GIF is converted to one on upload.
	fileKindGIF fileKind = "gif"
	// fileKindAudio is a music track, shown with a title and a performer.
	fileKindAudio fileKind = "audio"
	// fileKindVoice is a voice message, shown as a waveform.
	fileKindVoice fileKind = "voice"
	// fileKindVideoNote is a round video message.
	fileKindVideoNote fileKind = "video_note"
	fileKindSticker   fileKind = "sticker"
)

func parseFileKind(s string) (fileKind, error) {
	switch k := fileKind(strings.ToLower(strings.TrimSpace(s))); k {
	case "", fileKindAuto:
		return fileKindAuto, nil
	case fileKindDocument, fileKindPhoto, fileKindVideo, fileKindGIF,
		fileKindAudio, fileKindVoice, fileKindVideoNote, fileKindSticker:
		return k, nil
	default:
		return "", errors.Errorf("unknown kind %q, want auto, document, photo, video, gif, audio, voice, video_note or sticker", s)
	}
}

// resolve turns [fileKindAuto] into a concrete kind, from the MIME type.
//
// It never guesses voice, video_note or sticker: those are not properties of
// the bytes but of how the sender means them, and a voice message that should
// have been a music track is not something the caller can undo.
func (k fileKind) resolve(mimeType string) fileKind {
	if k != fileKindAuto {
		return k
	}

	base, _, _ := strings.Cut(mimeType, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	switch {
	case base == "image/gif":
		return fileKindGIF
	case strings.HasPrefix(base, "image/"):
		return fileKindPhoto
	case strings.HasPrefix(base, "video/"):
		return fileKindVideo
	case strings.HasPrefix(base, "audio/"):
		return fileKindAudio
	default:
		return fileKindDocument
	}
}

// named reports whether a file name belongs on this kind.
func (k fileKind) named() bool {
	switch k {
	case fileKindVoice, fileKindVideoNote, fileKindSticker:
		return false
	default:
		return true
	}
}

// mediaOption builds the media to send for a kind.
//
// The filename and MIME type are always set: gotd sets neither, and Telegram
// shows a document with no filename as its numeric id.
func (k fileKind) mediaOption(
	f tg.InputFileClass,
	thumb tg.InputFileClass,
	src uploadSource,
	in sendFileInput,
	caption []styling.StyledTextOption,
) message.MediaOption {
	if k == fileKindPhoto {
		return message.UploadedPhoto(f, caption...)
	}

	doc := message.UploadedDocument(f, caption...)
	if thumb != nil {
		// Telegram will not classify a document as an animation without one,
		// which is the flag every real client sends and none of the schema
		// documents as required.
		doc = doc.Thumb(thumb)
	}
	if src.mimeType != "" {
		doc = doc.MIME(src.mimeType)
	}
	// A voice message, a video note and a sticker carry no file name in any
	// real client, and Telegram quietly downgrades one that does back to a
	// plain document.
	if k.named() {
		doc = doc.Filename(src.name)
	}

	switch k {
	case fileKindVideo:
		// nosound_video is what keeps a silent mp4 a video: without it the
		// server promotes one to an animation, which is not what was asked.
		return video(doc.NosoundVideo(true).Video(), in).SupportsStreaming()
	case fileKindVideoNote:
		// nosound_video is what tdlib sets here, and without it Telegram
		// downgrades the round message to an ordinary video.
		return video(doc.NosoundVideo(true).RoundVideo(), in).Round()
	case fileKindGIF:
		// An animation is a soundless mp4. nosound_video must stay unset: it
		// means "send as a video even without audio", and setting it is
		// documented to suppress the animated attribute outright.
		//
		// Not [message.UploadedDocumentBuilder.GIF] either: that forces the
		// MIME type to image/gif, under which an mp4 is unreadable — Telegram
		// stores it as a plain document that lenient clients still play.
		return video(doc.Attributes(&tg.DocumentAttributeAnimated{}).Video(), in)
	case fileKindAudio:
		return audio(doc.Audio(), in)
	case fileKindVoice:
		return audio(doc.Audio().Voice(), in)
	case fileKindSticker:
		return doc.UploadedSticker()
	default:
		// A plain attachment: forced, so Telegram does not decide for itself
		// that an mp4 is a video after all.
		return doc.ForceFile(true)
	}
}

func video(b *message.VideoDocumentBuilder, in sendFileInput) *message.VideoDocumentBuilder {
	if in.DurationSeconds > 0 {
		b = b.Duration(time.Duration(in.DurationSeconds) * time.Second)
	}
	if in.Width > 0 && in.Height > 0 {
		b = b.Resolution(in.Width, in.Height)
	}

	return b
}

func audio(b *message.AudioDocumentBuilder, in sendFileInput) *message.AudioDocumentBuilder {
	if in.DurationSeconds > 0 {
		b = b.Duration(time.Duration(in.DurationSeconds) * time.Second)
	}
	if in.Title != "" {
		b = b.Title(in.Title)
	}
	if in.Performer != "" {
		b = b.Performer(in.Performer)
	}

	return b
}
