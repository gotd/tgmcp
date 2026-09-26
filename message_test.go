package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

// TestMessageFromTGMetadata covers what a reader cannot recover from the text:
// that a message was edited, forwarded, reacted to, or is one part of an album.
func TestMessageFromTGMetadata(t *testing.T) {
	fwd := tg.MessageFwdHeader{Date: 1700000000}
	fwd.SetFromID(&tg.PeerChannel{ChannelID: 11})

	msg := &tg.Message{ID: 42, Date: 1700000100, Message: "quoted"}
	msg.SetFromID(&tg.PeerUser{UserID: 7})
	msg.SetEditDate(1700000200)
	msg.SetFwdFrom(fwd)
	msg.SetGroupedID(13968203543465136)
	msg.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2},
	}})

	m := messageFromTG(msg, testEntities())

	if m.EditDate != "2023-11-14T22:16:40Z" {
		t.Errorf("edit_date %q, want the edit time", m.EditDate)
	}
	// A JSON number would keep only the top 53 bits of the id, silently
	// merging albums that are not the same album.
	if m.AlbumID != "13968203543465136" {
		t.Errorf("album_id %q, want the id verbatim", m.AlbumID)
	}
	if m.Forward == nil || m.Forward.From != "Durov's Channel" {
		t.Errorf("forward %+v, want the source channel", m.Forward)
	}
	if len(m.Reactions) != 1 || m.Reactions[0].Emoji != "👍" {
		t.Errorf("reactions %+v, want one 👍", m.Reactions)
	}
	if m.Author != "Ada Lovelace" {
		t.Errorf("author %q, want the forwarder, not the source", m.Author)
	}
}

// A plain message must carry none of it, so that reading a chat of ordinary
// messages returns no more fields than it did before.
func TestMessageFromTGPlain(t *testing.T) {
	m := messageFromTG(&tg.Message{ID: 1, Date: 1700000000, Message: "hi"}, testEntities())

	if m.EditDate != "" || m.AlbumID != "" || m.Forward != nil || m.Reactions != nil {
		t.Errorf("got %+v, want no metadata", m)
	}
}
