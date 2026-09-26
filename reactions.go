package main

import (
	"strconv"

	"github.com/gotd/td/tg"
)

// Reaction is one bucket of a message's reaction tally: which reaction, how
// many people used it, and whether this account is among them.
type Reaction struct {
	Emoji  string `json:"emoji" jsonschema:"the reaction: an emoji, custom:<document_id> for a custom emoji, or paid for a star reaction"`
	Count  int    `json:"count" jsonschema:"how many reacted with it"`
	Chosen bool   `json:"chosen,omitempty" jsonschema:"true if this account reacted with it, see send_reaction"`
}

// reactionsFrom converts a message's reaction tally, keeping the server's
// order: it is by count, which is the order every client shows.
//
// Chosen is only filled where the server sent a complete tally. For a message
// seen through a channel it may be false on a reaction this account did leave,
// so treat it as "known to be chosen" rather than as its negation.
func reactionsFrom(r tg.MessageReactions) []Reaction {
	out := make([]Reaction, 0, len(r.Results))
	for _, c := range r.Results {
		emoji := reactionEmoji(c.Reaction)
		if emoji == "" {
			continue
		}
		_, chosen := c.GetChosenOrder()
		out = append(out, Reaction{
			Emoji:  emoji,
			Count:  c.Count,
			Chosen: chosen,
		})
	}
	if len(out) == 0 {
		return nil
	}

	return out
}

// reactionEmoji names a reaction. Returns empty for one with no content,
// which the caller drops.
func reactionEmoji(r tg.ReactionClass) string {
	switch v := r.(type) {
	case *tg.ReactionEmoji:
		return v.Emoticon
	case *tg.ReactionCustomEmoji:
		// A custom emoji has no Unicode form, and only the document it renders
		// from. Naming that keeps the count honest; dropping the bucket would
		// silently understate how much a message was reacted to.
		return "custom:" + strconv.FormatInt(v.DocumentID, 10)
	case *tg.ReactionPaid:
		return "paid"
	default:
		return ""
	}
}
