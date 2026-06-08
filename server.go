package main

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/tg"
)

// server holds the dependencies shared by the MCP tool handlers.
type server struct {
	api *tg.Client
}

// listChannelsInput has no parameters.
type listChannelsInput struct{}

type listChannelsOutput struct {
	Channels []UnreadChannel `json:"channels" jsonschema:"channels with unread messages"`
}

type readChannelInput struct {
	Channel string `json:"channel" jsonschema:"channel @username or numeric ID, as returned by list_unread_channels"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum number of messages to return (default 50)"`
}

type readChannelOutput struct {
	Channel  UnreadChannel `json:"channel" jsonschema:"the resolved channel"`
	Messages []Message     `json:"messages" jsonschema:"unread messages, newest first"`
}

// register wires the tools onto an MCP server.
func (s *server) register(m *mcp.Server) {
	mcp.AddTool(m, &mcp.Tool{
		Name:        "list_unread_channels",
		Description: "List Telegram channels and supergroups that currently have unread messages, with their unread counts.",
	}, s.handleListChannels)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "read_channel_unread",
		Description: "Read the unread messages of a Telegram channel, newest first. Reading does not mark them as read.",
	}, s.handleReadChannel)
}

func (s *server) handleListChannels(ctx context.Context, _ *mcp.CallToolRequest, _ listChannelsInput) (*mcp.CallToolResult, listChannelsOutput, error) {
	channels, err := listUnreadChannels(ctx, s.api)
	if err != nil {
		return nil, listChannelsOutput{}, err
	}
	return nil, listChannelsOutput{Channels: channels}, nil
}

func (s *server) handleReadChannel(ctx context.Context, _ *mcp.CallToolRequest, in readChannelInput) (*mcp.CallToolResult, readChannelOutput, error) {
	if in.Channel == "" {
		return nil, readChannelOutput{}, errors.New("channel is required")
	}
	ch, msgs, err := readUnread(ctx, s.api, in.Channel, in.Limit)
	if err != nil {
		return nil, readChannelOutput{}, err
	}
	return nil, readChannelOutput{Channel: ch, Messages: msgs}, nil
}
