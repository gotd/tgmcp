package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

// TestToolsRegister verifies that the MCP tools register with valid schemas and
// are advertised over the protocol. It does not touch Telegram.
func TestToolsRegister(t *testing.T) {
	ctx := context.Background()

	srv := &server{api: nil, allowSend: true, allowProfileEdit: true}
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
		"list_unread_channels":         false,
		"read_channel_unread":          false,
		"mark_chat_read":               false,
		"mark_all_channels_read":       false,
		"list_chats":                   false,
		"search_chats":                 false,
		"get_me":                       false,
		"resolve_peer":                 false,
		"get_chat_messages":            false,
		"search_chat_messages":         false,
		"send_message":                 false,
		"send_file":                    false,
		"send_reaction":                false,
		"send_chat_action":             false,
		"send_screenshot_notification": false,
		"send_poll":                    false,
		"vote_poll":                    false,
		"list_inline_results":          false,
		"send_inline_result":           false,
		"get_file":                     false,
		"update_profile":               false,
		"update_profile_photo":         false,
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

// TestToolsRegisterGated verifies that disabling allowSend/allowProfileEdit
// hides the corresponding tools instead of just erroring inside the handler.
func TestToolsRegisterGated(t *testing.T) {
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

	gated := []string{"send_message", "send_file", "send_reaction", "send_chat_action", "send_screenshot_notification", "update_profile", "update_profile_photo"}
	for _, tool := range res.Tools {
		for _, name := range gated {
			if tool.Name == name {
				t.Errorf("tool %q was advertised despite being gated off", name)
			}
		}
	}
}

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "dir.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path    string
		wantErr bool
	}{
		{"ok.txt", false},
		{"sub/dir.txt", false},
		{filepath.Join(root, "ok.txt"), false},
		{"../escape", true},
		{"/abs/outside", true},
		{"link", true},
	}
	for _, c := range cases {
		_, err := safeJoin(root, c.path)
		if (err != nil) != c.wantErr {
			t.Errorf("safeJoin(%q): err=%v wantErr=%v", c.path, err, c.wantErr)
		}
	}
}

func TestRebuildPeerPersistence(t *testing.T) {
	// Roundtrip via storedDialog must preserve peer kind and access hash.
	ch := UnreadChannel{
		ID:    42,
		Title: "u",
		peer:  &tg.InputPeerUser{UserID: 42, AccessHash: 7},
	}
	sd := toStored(ch)
	back := sd.toChannel()
	if _, ok := back.peer.(*tg.InputPeerUser); !ok {
		t.Fatalf("peer kind lost: %T", back.peer)
	}
	if ch.peer.(*tg.InputPeerUser).AccessHash != back.peer.(*tg.InputPeerUser).AccessHash {
		t.Fatalf("access hash lost")
	}
	if back.Type != "private" {
		t.Fatalf("type lost: %q", back.Type)
	}
}

func TestListChatsQuery(t *testing.T) {
	cache := newDialogCache(nil, zap.NewNop())
	cache.replaceAll([]UnreadChannel{
		{ID: 1, Title: "Go Nuts", Username: "golang", Type: "supergroup", peer: &tg.InputPeerChannel{ChannelID: 1}},
		{ID: 2, Title: "Anna Smith", Type: "private", UnreadCount: 3, peer: &tg.InputPeerUser{UserID: 2}},
		{ID: 3, Title: "Daily News", Username: "news", Type: "channel", peer: &tg.InputPeerChannel{ChannelID: 3}},
	})
	srv := &server{cache: cache}

	cases := []struct {
		name string
		in   listChatsInput
		want []int64
	}{
		{"all", listChatsInput{}, []int64{1, 2, 3}},
		{"title substring", listChatsInput{Query: "anna"}, []int64{2}},
		{"case insensitive", listChatsInput{Query: "DAILY"}, []int64{3}},
		{"username", listChatsInput{Query: "@golang"}, []int64{1}},
		{"query and type", listChatsInput{Query: "n", Type: "private"}, []int64{2}},
		{"query and unread", listChatsInput{Query: "n", UnreadOnly: true}, []int64{2}},
		{"no match", listChatsInput{Query: "nothing here"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, out, err := srv.handleListChats(context.Background(), nil, c.in)
			if err != nil {
				t.Fatalf("handleListChats: %v", err)
			}
			var got []int64
			for _, ch := range out.Chats {
				got = append(got, ch.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
			if out.TotalMatched != len(c.want) {
				t.Errorf("total_matched = %d, want %d", out.TotalMatched, len(c.want))
			}
		})
	}
}

func TestMarkReadPeer(t *testing.T) {
	cache := newDialogCache(nil, zap.NewNop())
	cache.replaceAll([]UnreadChannel{
		{ID: 1, Title: "chan", UnreadCount: 5, Type: "channel", peer: &tg.InputPeerChannel{ChannelID: 1}},
		{ID: 2, Title: "user", UnreadCount: 7, UnreadMark: true, Type: "private", peer: &tg.InputPeerUser{UserID: 2}},
		{ID: 3, Title: "group", UnreadCount: 9, Type: "group", peer: &tg.InputPeerChat{ChatID: 3}},
	})

	// A user and a channel may share the same numeric ID: marking one read must
	// not clear the other.
	cache.markReadPeer(&tg.InputPeerUser{UserID: 2})
	if ch, _ := cache.getKey(dialogKeyParts("user", 2)); ch.UnreadCount != 0 || ch.UnreadMark {
		t.Errorf("user dialog not marked read: %+v", ch)
	}
	if ch, _ := cache.getKey(dialogKeyParts("channel", 1)); ch.UnreadCount != 5 {
		t.Errorf("channel unread changed: %d", ch.UnreadCount)
	}

	cache.markReadPeer(&tg.InputPeerChat{ChatID: 3})
	if ch, _ := cache.getKey(dialogKeyParts("chat", 3)); ch.UnreadCount != 0 {
		t.Errorf("group dialog not marked read: %+v", ch)
	}

	// Unknown and keyless peers are ignored, not panics.
	cache.markReadPeer(&tg.InputPeerUser{UserID: 404})
	cache.markReadPeer(&tg.InputPeerSelf{})
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name string
		user tg.User
		want string
	}{
		{"full name", tg.User{FirstName: "Anna", LastName: "Smith"}, "Anna Smith"},
		{"first only", tg.User{FirstName: "Anna"}, "Anna"},
		{"deleted falls back to username", tg.User{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := displayName(&c.user); got != c.want {
				t.Errorf("displayName = %q, want %q", got, c.want)
			}
		})
	}

	u := tg.User{}
	u.SetUsername("anna")
	if got := displayName(&u); got != "anna" {
		t.Errorf("displayName = %q, want %q", got, "anna")
	}

	withCollectible := tg.User{}
	withCollectible.SetUsernames([]tg.Username{{Username: "inactive"}, {Active: true, Username: "active"}})
	if got := primaryUsername(&withCollectible); got != "active" {
		t.Errorf("primaryUsername = %q, want %q", got, "active")
	}
}

func TestActionName(t *testing.T) {
	cases := []struct {
		action tg.MessageActionClass
		want   string
	}{
		{&tg.MessageActionScreenshotTaken{}, "screenshot_taken"},
		{&tg.MessageActionPinMessage{}, "pin_message"},
		{&tg.MessageActionChatAddUser{}, "chat_add_user"},
		{&tg.MessageActionContactSignUp{}, "contact_sign_up"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := actionName(c.action); got != c.want {
			t.Errorf("actionName(%T) = %q, want %q", c.action, got, c.want)
		}
	}
}

func TestMessageFromClass(t *testing.T) {
	ent := peer.NewEntities(
		map[int64]*tg.User{7: {ID: 7, FirstName: "Anna"}},
		nil,
		nil,
	)

	svc := &tg.MessageService{ID: 11, Date: 1700000000, Action: &tg.MessageActionScreenshotTaken{}}
	svc.SetFromID(&tg.PeerUser{UserID: 7})
	got, ok := messageFromClass(svc, ent)
	if !ok {
		t.Fatal("service message dropped")
	}
	if !got.Service || got.Action != "screenshot_taken" {
		t.Errorf("service=%v action=%q", got.Service, got.Action)
	}
	if got.ID != 11 || got.Author != "Anna" {
		t.Errorf("id=%d author=%q", got.ID, got.Author)
	}

	plain := &tg.Message{ID: 12, Date: 1700000000, Message: "hi"}
	plain.SetFromID(&tg.PeerUser{UserID: 7})
	got, ok = messageFromClass(plain, ent)
	if !ok {
		t.Fatal("plain message dropped")
	}
	if got.Service || got.Action != "" || got.Text != "hi" {
		t.Errorf("unexpected plain message: %+v", got)
	}
}
