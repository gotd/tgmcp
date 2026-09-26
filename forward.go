package main

import (
	"time"

	"github.com/gotd/td/tg"
)

// Forward describes where a forwarded message came from: the sender re-sent
// somebody else's message here, rather than writing it.
//
// Most of what appears in a busy chat was written somewhere else, and without
// this a forward reads as if the sender wrote it — the one attribution error
// worth spending fields on.
type Forward struct {
	From      string `json:"from,omitempty" jsonschema:"original sender: a user's name, or a channel or group title; empty when it is not known"`
	Author    string `json:"author,omitempty" jsonschema:"signature on the original post, for channels that sign posts"`
	Date      string `json:"date,omitempty" jsonschema:"original send time in RFC3339"`
	MessageID int    `json:"message_id,omitempty" jsonschema:"id of the original post in the source channel, when it has one"`
	Hidden    bool   `json:"hidden,omitempty" jsonschema:"true if the original sender forbids linking back: from is then only the name they show, and refers to no reachable account"`
	Imported  bool   `json:"imported,omitempty" jsonschema:"true if the message was imported from another messenger rather than forwarded"`
}

// forwardFrom converts a forward header, resolving the source from the
// entities that came with the message.
func forwardFrom(ent entities, h tg.MessageFwdHeader) *Forward {
	f := &Forward{
		Author:    h.PostAuthor,
		MessageID: h.ChannelPost,
		Imported:  h.Imported,
	}
	if h.Date > 0 {
		f.Date = time.Unix(int64(h.Date), 0).UTC().Format(time.RFC3339)
	}
	if from, ok := h.GetFromID(); ok {
		f.From = peerName(ent, from)
	}
	// A sender who forbids being linked to is sent as a bare name with no peer,
	// so there is no account to look up. Say so: the name is otherwise
	// indistinguishable from one that identifies a reachable account.
	if name, ok := h.GetFromName(); ok && name != "" {
		f.Hidden = true
		if f.From == "" {
			f.From = name
		}
	}

	return f
}
