package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolsRegister verifies that the MCP tools register with valid schemas and
// are advertised over the protocol. It does not touch Telegram.
func TestToolsRegister(t *testing.T) {
	ctx := context.Background()

	srv := &server{api: nil}
	m := mcp.NewServer(&mcp.Implementation{Name: "tgmcp", Version: "test"}, nil)
	srv.register(m)

	serverTr, clientTr := mcp.NewInMemoryTransports()
	if _, err := m.Connect(ctx, serverTr, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	want := map[string]bool{
		"list_unread_channels": false,
		"read_channel_unread":  false,
	}
	for _, tool := range res.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has nil input schema", tool.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q was not advertised", name)
		}
	}
}
