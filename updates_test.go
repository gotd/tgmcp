package main

import (
	"testing"

	"go.uber.org/zap"

	"github.com/gotd/td/tg"
)

// updateEntities mirrors what the update stream carries alongside a message in
// a private chat or a legacy group.
func updateEntities() tg.Entities {
	return tg.Entities{
		Users: map[int64]*tg.User{7: {ID: 7, FirstName: "Ada", AccessHash: 70}},
		Chats: map[int64]*tg.Chat{9: {ID: 9, Title: "Legacy group"}},
	}
}

func dm(id int, out bool) *tg.Message {
	m := &tg.Message{ID: id, Message: "hi", PeerID: &tg.PeerUser{UserID: 7}}
	m.SetOut(out)

	return m
}

// The bug: nothing incremented the unread count of a private chat or a legacy
// group, so one could only ever go down after the initial dialog fetch.
func TestObserveNewMessageCountsUnread(t *testing.T) {
	cache := newDialogCache(nil, zap.NewNop())
	lg := zap.NewNop()

	observeNewMessage(cache, lg, updateEntities(), dm(1, false))
	observeNewMessage(cache, lg, updateEntities(), dm(2, false))

	ch, ok := cache.get(7)
	if !ok {
		t.Fatal("private chat was not cached")
	}
	if ch.UnreadCount != 2 {
		t.Errorf("unread %d, want 2", ch.UnreadCount)
	}
	// The dialog must arrive usable, not as a bare id.
	if ch.Title != "Ada" || ch.Type != typePrivate {
		t.Errorf("got %+v, want a named private chat", ch)
	}
}

func TestObserveNewMessageIgnores(t *testing.T) {
	group := &tg.Message{ID: 1, Message: "hi", PeerID: &tg.PeerChat{ChatID: 9}}
	channel := &tg.Message{ID: 1, Message: "hi", PeerID: &tg.PeerChannel{ChannelID: 11}}

	tests := []struct {
		name string
		msg  tg.MessageClass
		id   int64
		want int
	}{
		{name: "legacy group counts", msg: group, id: 9, want: 1},
		// Own messages are not unread.
		{name: "own message", msg: dm(1, true), id: 7, want: 0},
		// A channel arrives as updateNewChannelMessage; counting it here too
		// would double it.
		{name: "channel", msg: channel, id: 11, want: 0},
		{name: "empty message", msg: &tg.MessageEmpty{ID: 1}, id: 7, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newDialogCache(nil, zap.NewNop())
			observeNewMessage(cache, zap.NewNop(), updateEntities(), tt.msg)

			ch, ok := cache.get(tt.id)
			if !ok {
				if tt.want != 0 {
					t.Fatalf("dialog %d was not cached", tt.id)
				}

				return
			}
			if ch.UnreadCount != tt.want {
				t.Errorf("unread %d, want %d", ch.UnreadCount, tt.want)
			}
		})
	}
}

// Reading a chat on another device must clear the count here too, which is what
// updateReadHistoryInbox reports for a private chat or group.
func TestSetReadByPeer(t *testing.T) {
	cache := newDialogCache(nil, zap.NewNop())
	observeNewMessage(cache, zap.NewNop(), updateEntities(), dm(1, false))

	cache.setRead(&tg.PeerUser{UserID: 7}, 1, 0)

	ch, _ := cache.get(7)
	if ch.UnreadCount != 0 || ch.readInboxMaxID != 1 {
		t.Errorf("got unread=%d max=%d, want 0 and 1", ch.UnreadCount, ch.readInboxMaxID)
	}
}

// The three peer kinds must land in distinct cache slots: user, chat and
// channel ids are separate namespaces and do collide.
func TestPeerKeyNamespaces(t *testing.T) {
	seen := make(map[string]string)
	peers := map[string]tg.PeerClass{
		"user":    &tg.PeerUser{UserID: 1},
		"chat":    &tg.PeerChat{ChatID: 1},
		"channel": &tg.PeerChannel{ChannelID: 1},
	}
	for name, p := range peers {
		key, ok := peerKey(p)
		if !ok {
			t.Fatalf("%s: no key", name)
		}
		if other, dup := seen[key]; dup {
			t.Errorf("%s collides with %s on key %q", name, other, key)
		}
		seen[key] = name
	}

	if _, ok := peerKey(nil); ok {
		t.Error("nil peer produced a key")
	}
}
