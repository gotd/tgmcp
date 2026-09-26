package main

import (
	"testing"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

func testEntities() entities {
	return peer.NewEntities(
		map[int64]*tg.User{7: {ID: 7, FirstName: "Ada", LastName: "Lovelace"}},
		map[int64]*tg.Chat{9: {ID: 9, Title: "Legacy group"}},
		map[int64]*tg.Channel{11: {ID: 11, Title: "Durov's Channel"}},
	)
}

func TestForwardFrom(t *testing.T) {
	hidden := tg.MessageFwdHeader{Date: 1700000000}
	hidden.SetFromName("Someone")

	channel := tg.MessageFwdHeader{Date: 1700000000}
	channel.SetFromID(&tg.PeerChannel{ChannelID: 11})
	channel.SetChannelPost(312)
	channel.SetPostAuthor("Pavel")

	user := tg.MessageFwdHeader{Date: 1700000000}
	user.SetFromID(&tg.PeerUser{UserID: 7})

	unknown := tg.MessageFwdHeader{Date: 1700000000}
	unknown.SetFromID(&tg.PeerUser{UserID: 404})

	tests := []struct {
		name string
		in   tg.MessageFwdHeader
		want Forward
	}{
		{
			name: "user",
			in:   user,
			want: Forward{From: "Ada Lovelace", Date: "2023-11-14T22:13:20Z"},
		},
		{
			name: "channel post",
			in:   channel,
			want: Forward{
				From:      "Durov's Channel",
				Author:    "Pavel",
				MessageID: 312,
				Date:      "2023-11-14T22:13:20Z",
			},
		},
		{
			// The name is all there is: no account to resolve, so a reader
			// must not take it for an identity it can look up.
			name: "hidden sender",
			in:   hidden,
			want: Forward{From: "Someone", Hidden: true, Date: "2023-11-14T22:13:20Z"},
		},
		{
			// Naming a peer we cannot resolve would be worse than naming none.
			name: "sender not in entities",
			in:   unknown,
			want: Forward{Date: "2023-11-14T22:13:20Z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forwardFrom(testEntities(), tt.in)
			if got == nil || *got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// An anonymous admin and a signed channel post name a channel as their sender,
// which is not a user and used to read as no author at all.
func TestPeerName(t *testing.T) {
	tests := []struct {
		name string
		in   tg.PeerClass
		want string
	}{
		{name: "user", in: &tg.PeerUser{UserID: 7}, want: "Ada Lovelace"},
		{name: "chat", in: &tg.PeerChat{ChatID: 9}, want: "Legacy group"},
		{name: "channel", in: &tg.PeerChannel{ChannelID: 11}, want: "Durov's Channel"},
		{name: "unknown user", in: &tg.PeerUser{UserID: 404}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerName(testEntities(), tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
