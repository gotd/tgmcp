package main

import (
	"testing"

	"go.uber.org/zap"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

func resolveEntities() entities {
	return peer.NewEntities(
		map[int64]*tg.User{7: {ID: 7, FirstName: "Ada", LastName: "Lovelace", AccessHash: 70}},
		map[int64]*tg.Chat{9: {ID: 9, Title: "Legacy group"}},
		map[int64]*tg.Channel{
			11: {ID: 11, Title: "Daily News", AccessHash: 110},
			12: {ID: 12, Title: "Go Nuts", Megagroup: true, AccessHash: 120},
		},
	)
}

// A resolution knows what the chat is. Reporting an empty type for a channel
// the server has just described is the bug this path exists to close.
func TestChannelFromEntities(t *testing.T) {
	tests := []struct {
		name     string
		in       tg.PeerClass
		wantType string
		wantName string
	}{
		{name: "broadcast", in: &tg.PeerChannel{ChannelID: 11}, wantType: typeChannel, wantName: "Daily News"},
		{name: "supergroup", in: &tg.PeerChannel{ChannelID: 12}, wantType: typeSupergroup, wantName: "Go Nuts"},
		{name: "user", in: &tg.PeerUser{UserID: 7}, wantType: typePrivate, wantName: "Ada Lovelace"},
		{name: "legacy group", in: &tg.PeerChat{ChatID: 9}, wantType: typeGroup, wantName: "Legacy group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, ok := channelFromEntities(resolveEntities(), tt.in)
			if !ok {
				t.Fatal("not resolved")
			}
			if ch.Type != tt.wantType {
				t.Errorf("type %q, want %q", ch.Type, tt.wantType)
			}
			if ch.Title != tt.wantName {
				t.Errorf("title %q, want %q", ch.Title, tt.wantName)
			}
			// Without an input peer the entry cannot address the chat, which
			// makes it useless to every caller that reads it.
			if ch.peer == nil {
				t.Error("no input peer")
			}
		})
	}
}

func TestChannelFromEntitiesUnknown(t *testing.T) {
	if _, ok := channelFromEntities(resolveEntities(), &tg.PeerChannel{ChannelID: 404}); ok {
		t.Error("resolved a peer that is not in the entities")
	}
}

// peerToChannel must fall back to what resolving the target revealed, which is
// the whole point of keeping it: the dialog cache holds dialogs, and a chat
// reached by @username need not be one.
func TestPeerToChannelUsesResolved(t *testing.T) {
	srv := &server{cache: newDialogCache(nil, zap.NewNop()), resolved: newResolvedPeers()}
	ch, _ := channelFromEntities(resolveEntities(), &tg.PeerChannel{ChannelID: 11})
	srv.resolved.put(ch)

	got := srv.peerToChannel(&tg.InputPeerChannel{ChannelID: 11, AccessHash: 110})
	if got.Type != typeChannel || got.Title != "Daily News" {
		t.Errorf("got %+v, want the resolved channel", got)
	}

	// Still unknown for a channel nothing has resolved. Empty reads as unknown;
	// guessing a type would read as fact.
	if unknown := srv.peerToChannel(&tg.InputPeerChannel{ChannelID: 99}); unknown.Type != "" {
		t.Errorf("type %q, want empty for an unknown channel", unknown.Type)
	}
}

func TestResolvedPeersBounded(t *testing.T) {
	p := newResolvedPeers()
	for i := 1; i <= resolvedPeersMax+1; i++ {
		p.put(UnreadChannel{ID: int64(i), Type: typeChannel})
	}
	if n := len(p.m); n > resolvedPeersMax {
		t.Errorf("holds %d entries, want at most %d", n, resolvedPeersMax)
	}
	// The most recent put must survive the drop, or a resolve immediately
	// followed by a read would miss.
	if _, ok := p.get(resolvedPeersMax + 1); !ok {
		t.Error("newest entry was dropped")
	}
}
