package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestCallFromFull(t *testing.T) {
	channelLive := &tg.ChannelFull{}
	channelLive.SetCall(&tg.InputGroupCall{ID: 1, AccessHash: 10})

	chatLive := &tg.ChatFull{}
	chatLive.SetCall(&tg.InputGroupCall{ID: 2, AccessHash: 20})

	tests := []struct {
		name   string
		in     tg.ChatFullClass
		wantID int64
		wantOK bool
	}{
		{name: "channel streaming", in: channelLive, wantID: 1, wantOK: true},
		{name: "legacy group streaming", in: chatLive, wantID: 2, wantOK: true},
		// Nothing live is the common case and must not read as a call with a
		// zero id, which discardGroupCall would happily send.
		{name: "channel idle", in: &tg.ChannelFull{}},
		{name: "group idle", in: &tg.ChatFull{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, ok := callFromFull(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			c, isCall := call.(*tg.InputGroupCall)
			if !isCall {
				t.Fatalf("got %T, want *tg.InputGroupCall", call)
			}
			if c.ID != tt.wantID {
				t.Errorf("call id %d, want %d", c.ID, tt.wantID)
			}
		})
	}
}

// The stream must be flagged rtmp_stream, or the call comes up as an ordinary
// video chat that nothing can push frames to.
func TestNewStreamRequest(t *testing.T) {
	peer := &tg.InputPeerChannel{ChannelID: 11, AccessHash: 110}

	req := newStreamRequest(peer, "Deploy log", 1)
	if !req.RtmpStream {
		t.Error("rtmp_stream is not set, so the call is an ordinary video chat")
	}
	title, ok := req.GetTitle()
	if !ok || title != "Deploy log" {
		t.Errorf("title %q (%v), want Deploy log", title, ok)
	}

	// An untitled stream must leave the field unset rather than send an empty
	// one, which Telegram shows as a blank title.
	if _, ok := newStreamRequest(peer, "", 1).GetTitle(); ok {
		t.Error("empty title was sent")
	}

	// random_id is an int32 on the wire. A full-width int64 has to narrow
	// without overflowing the field.
	if id := newStreamRequest(peer, "", 1<<40|7).RandomID; id != 7 {
		t.Errorf("random id %d, want 7", id)
	}
}
