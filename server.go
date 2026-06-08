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

type markChannelReadInput struct {
	Channel string `json:"channel" jsonschema:"channel @username or numeric ID, as returned by list_unread_channels"`
}

type markChannelReadOutput struct {
	Channel UnreadChannel `json:"channel" jsonschema:"the channel that was marked as read"`
}

// markAllChannelsReadInput has no parameters.
type markAllChannelsReadInput struct{}

type markAllChannelsReadOutput struct {
	MarkedCount int `json:"marked_count" jsonschema:"number of channels marked as read"`
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

	mcp.AddTool(m, &mcp.Tool{
		Name:        "mark_channel_read",
		Description: "Mark all messages in a specific Telegram channel or supergroup as read.",
	}, s.handleMarkChannelRead)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "mark_all_channels_read",
		Description: "Mark all unread Telegram channels and supergroups as read in one call.",
	}, s.handleMarkAllChannelsRead)
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

func (s *server) handleMarkChannelRead(ctx context.Context, _ *mcp.CallToolRequest, in markChannelReadInput) (*mcp.CallToolResult, markChannelReadOutput, error) {
	if in.Channel == "" {
		return nil, markChannelReadOutput{}, errors.New("channel is required")
	}
	ch, err := findChannel(ctx, s.api, in.Channel)
	if err != nil {
		return nil, markChannelReadOutput{}, err
	}
	if err := markChannelRead(ctx, s.api, ch); err != nil {
		return nil, markChannelReadOutput{}, err
	}
	return nil, markChannelReadOutput{Channel: ch}, nil
}

func (s *server) handleMarkAllChannelsRead(ctx context.Context, _ *mcp.CallToolRequest, _ markAllChannelsReadInput) (*mcp.CallToolResult, markAllChannelsReadOutput, error) {
	n, err := markAllChannelsRead(ctx, s.api)
	if err != nil {
		return nil, markAllChannelsReadOutput{}, err
	}
	return nil, markAllChannelsReadOutput{MarkedCount: n}, nil
}

