package main

import (
	"reflect"
	"testing"

	"github.com/gotd/td/tg"
)

func TestReactionEmoji(t *testing.T) {
	tests := []struct {
		name string
		in   tg.ReactionClass
		want string
	}{
		{name: "emoji", in: &tg.ReactionEmoji{Emoticon: "👍"}, want: "👍"},
		{
			name: "custom",
			in:   &tg.ReactionCustomEmoji{DocumentID: 5350291836378307462},
			want: "custom:5350291836378307462",
		},
		{name: "paid", in: &tg.ReactionPaid{}, want: "paid"},
		{name: "empty", in: &tg.ReactionEmpty{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reactionEmoji(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReactionsFrom(t *testing.T) {
	chosen := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: "🔥"}, Count: 3}
	chosen.SetChosenOrder(0)

	got := reactionsFrom(tg.MessageReactions{Results: []tg.ReactionCount{
		{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 12},
		chosen,
		// Carries no reaction, so there is nothing to report for it.
		{Reaction: &tg.ReactionEmpty{}, Count: 1},
	}})

	want := []Reaction{
		{Emoji: "👍", Count: 12},
		{Emoji: "🔥", Count: 3, Chosen: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A message with no reactions must leave the field absent rather than empty,
// so that reading a quiet chat costs no tokens.
func TestReactionsFromEmpty(t *testing.T) {
	if got := reactionsFrom(tg.MessageReactions{}); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
