package main

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/tg"
)

type startStreamInput struct {
	Chat  string `json:"chat" jsonschema:"chat target: a channel or group where you may manage video chats"`
	Title string `json:"title,omitempty" jsonschema:"title shown to viewers"`
}

type startStreamOutput struct {
	OK  bool   `json:"ok" jsonschema:"true on success"`
	URL string `json:"url" jsonschema:"RTMP ingest URL; the ffmpeg or OBS target is this with key appended"`
	Key string `json:"key" jsonschema:"stream key: a long-lived secret belonging to the chat, not to this stream, which stays valid after stop_stream until revoke replaces it"`
	// Created separates "this call started it" from "one was already running",
	// which matters because the key comes back either way and reads identically.
	Created bool `json:"created" jsonschema:"true if this call started the stream; false if a call was already live and the key addresses that one"`
}

type revokeStreamKeyInput struct {
	Chat string `json:"chat" jsonschema:"chat target whose stream key should be replaced"`
}

type revokeStreamKeyOutput struct {
	OK bool `json:"ok" jsonschema:"true on success"`
}

type stopStreamInput struct {
	Chat string `json:"chat" jsonschema:"chat target whose live stream should end"`
}

type stopStreamOutput struct {
	OK      bool `json:"ok" jsonschema:"true on success"`
	Stopped bool `json:"stopped" jsonschema:"true if a call was ended; false if nothing was live"`
}

func (s *server) handleStartStream(ctx context.Context, _ *mcp.CallToolRequest, in startStreamInput) (*mcp.CallToolResult, startStreamOutput, error) {
	if in.Chat == "" {
		return nil, startStreamOutput{}, errors.New("chat is required")
	}

	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, startStreamOutput{}, err
	}

	_, live, err := s.activeGroupCall(ctx, p)
	if err != nil {
		return nil, startStreamOutput{}, err
	}

	var created bool
	if !live {
		if err := s.createStream(ctx, p, in.Title); err != nil {
			return nil, startStreamOutput{}, err
		}
		created = true
	}

	// Fetched after creating, so a failed create does not hand out a key for a
	// stream that never started.
	rtmp, err := s.api.PhoneGetGroupCallStreamRtmpURL(ctx, &tg.PhoneGetGroupCallStreamRtmpURLRequest{
		Peer: p,
	})
	if err != nil {
		return nil, startStreamOutput{}, errors.Wrap(err, "phone.getGroupCallStreamRtmpUrl")
	}

	return nil, startStreamOutput{OK: true, URL: rtmp.URL, Key: rtmp.Key, Created: created}, nil
}

// handleRevokeStreamKey replaces the chat's stream key and reports nothing but
// success.
//
// The same RPC returns the replacement, and start_stream used to hand it back.
// That made rotation self-defeating: every revocation published a fresh secret
// into the transcript that the next reader could use. Dropping the response is
// the whole point of the tool, so the caller has to fetch the new key
// deliberately, by starting a stream, rather than as a side effect of burning
// the old one.
func (s *server) handleRevokeStreamKey(ctx context.Context, _ *mcp.CallToolRequest, in revokeStreamKeyInput) (*mcp.CallToolResult, revokeStreamKeyOutput, error) {
	if in.Chat == "" {
		return nil, revokeStreamKeyOutput{}, errors.New("chat is required")
	}

	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, revokeStreamKeyOutput{}, err
	}

	if _, err := s.api.PhoneGetGroupCallStreamRtmpURL(ctx, &tg.PhoneGetGroupCallStreamRtmpURLRequest{
		Peer:   p,
		Revoke: true,
	}); err != nil {
		return nil, revokeStreamKeyOutput{}, errors.Wrap(err, "phone.getGroupCallStreamRtmpUrl")
	}

	return nil, revokeStreamKeyOutput{OK: true}, nil
}

func (s *server) handleStopStream(ctx context.Context, _ *mcp.CallToolRequest, in stopStreamInput) (*mcp.CallToolResult, stopStreamOutput, error) {
	if in.Chat == "" {
		return nil, stopStreamOutput{}, errors.New("chat is required")
	}

	p, err := s.resolvePeer(ctx, in.Chat)
	if err != nil {
		return nil, stopStreamOutput{}, err
	}

	call, live, err := s.activeGroupCall(ctx, p)
	if err != nil {
		return nil, stopStreamOutput{}, err
	}
	if !live {
		return nil, stopStreamOutput{OK: true}, nil
	}

	if _, err := s.api.PhoneDiscardGroupCall(ctx, call); err != nil {
		return nil, stopStreamOutput{}, errors.Wrap(err, "phone.discardGroupCall")
	}

	return nil, stopStreamOutput{OK: true, Stopped: true}, nil
}

// createStream opens an RTMP-fed group call on a chat. Telegram encodes and
// distributes the frames, so nothing here joins the call: the account
// broadcasts by pushing to the ingest URL and never speaks WebRTC.
func (s *server) createStream(ctx context.Context, p tg.InputPeerClass, title string) error {
	id, err := randomInt64()
	if err != nil {
		return err
	}

	if _, err := s.api.PhoneCreateGroupCall(ctx, newStreamRequest(p, title, id)); err != nil {
		return errors.Wrap(err, "phone.createGroupCall")
	}

	return nil
}

func newStreamRequest(p tg.InputPeerClass, title string, id int64) *tg.PhoneCreateGroupCallRequest {
	req := &tg.PhoneCreateGroupCallRequest{
		RtmpStream: true,
		Peer:       p,
		// random_id is an int32 on the wire, so the deduplication id is
		// narrowed rather than passed whole.
		RandomID: int(int32(id)),
	}
	if title != "" {
		req.SetTitle(title)
	}

	return req
}

// activeGroupCall reports the group call a chat is running, if any.
//
// A live stream and an ordinary video chat are the same object, distinguished
// only by a flag, so a hit here is not proof that a stream is what is running.
func (s *server) activeGroupCall(ctx context.Context, p tg.InputPeerClass) (tg.InputGroupCallClass, bool, error) {
	var full tg.ChatFullClass
	switch v := p.(type) {
	case *tg.InputPeerChannel:
		res, err := s.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{
			ChannelID:  v.ChannelID,
			AccessHash: v.AccessHash,
		})
		if err != nil {
			return nil, false, errors.Wrap(err, "channels.getFullChannel")
		}
		full = res.FullChat
	case *tg.InputPeerChat:
		res, err := s.api.MessagesGetFullChat(ctx, v.ChatID)
		if err != nil {
			return nil, false, errors.Wrap(err, "messages.getFullChat")
		}
		full = res.FullChat
	default:
		return nil, false, errors.New("a live stream needs a group or channel")
	}

	call, ok := callFromFull(full)

	return call, ok, nil
}

// callFromFull digs the group call out of a full chat. ChatFullClass has no
// GetCall, so the two implementations are unwrapped by hand.
func callFromFull(full tg.ChatFullClass) (tg.InputGroupCallClass, bool) {
	switch f := full.(type) {
	case *tg.ChannelFull:
		return f.GetCall()
	case *tg.ChatFull:
		return f.GetCall()
	default:
		return nil, false
	}
}
