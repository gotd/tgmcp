package main

import (
	"context"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/gotd/td/tg"
)

// openTestDB opens a bbolt database in a temporary directory, closed on cleanup.
func openTestDB(t *testing.T) *bolt.DB {
	t.Helper()

	db, err := openStateDB(Config{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func channel(id int64, accessHash int64, unread int) UnreadChannel {
	return UnreadChannel{
		ID:             id,
		Title:          "Channel",
		Username:       "chan",
		UnreadCount:    unread,
		Broadcast:      true,
		readInboxMaxID: 1000 + int(id),
		peer:           &tg.InputPeerChannel{ChannelID: id, AccessHash: accessHash},
	}
}

func byID(chs []UnreadChannel) map[int64]UnreadChannel {
	m := make(map[int64]UnreadChannel, len(chs))
	for _, ch := range chs {
		m[ch.ID] = ch
	}

	return m
}

func TestDialogStore(t *testing.T) {
	store := &dialogStore{db: openTestDB(t)}

	// Empty store must return no dialogs and no error: this guards the nil-wrap
	// regression where load reported a spurious error and forced a re-fetch.
	got, err := store.load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("load empty: got %d dialogs, want 0", len(got))
	}

	want := channel(42, -7777, 3)
	if err := store.put(want); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err = store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("load: got %d dialogs, want 1", len(got))
	}

	roundTrip := got[0]
	if roundTrip.ID != want.ID || roundTrip.Title != want.Title || roundTrip.Username != want.Username ||
		roundTrip.UnreadCount != want.UnreadCount || roundTrip.Broadcast != want.Broadcast ||
		roundTrip.readInboxMaxID != want.readInboxMaxID {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", roundTrip, want)
	}

	// The input peer (with access hash) must survive the round trip.
	ipc, ok := roundTrip.peer.(*tg.InputPeerChannel)
	if !ok {
		t.Fatalf("peer: got %T, want *tg.InputPeerChannel", roundTrip.peer)
	}
	if ipc.ChannelID != want.ID || ipc.AccessHash != -7777 {
		t.Fatalf("peer: got %+v, want channel=%d hash=-7777", ipc, want.ID)
	}
}

func TestDialogStorePutAllReplaces(t *testing.T) {
	store := &dialogStore{db: openTestDB(t)}

	if err := store.putAll([]UnreadChannel{channel(1, 11, 1), channel(2, 22, 2)}); err != nil {
		t.Fatalf("putAll first: %v", err)
	}

	// A second putAll without channel 2 must drop it.
	if err := store.putAll([]UnreadChannel{channel(1, 11, 5), channel(3, 33, 3)}); err != nil {
		t.Fatalf("putAll second: %v", err)
	}

	got, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	m := byID(got)
	if len(m) != 2 {
		t.Fatalf("load: got %d dialogs, want 2 (%v)", len(m), m)
	}
	if _, ok := m[2]; ok {
		t.Fatalf("channel 2 should have been dropped by putAll")
	}
	if m[1].UnreadCount != 5 {
		t.Fatalf("channel 1 unread: got %d, want 5", m[1].UnreadCount)
	}
	if _, ok := m[3]; !ok {
		t.Fatalf("channel 3 should be present")
	}
}

func TestAccessHasher(t *testing.T) {
	h := accessHasher{db: openTestDB(t)}
	ctx := context.Background()

	// A miss must return found=false and no error (the nil-wrap regression).
	hash, found, err := h.GetChannelAccessHash(ctx, 0, 99)
	if err != nil {
		t.Fatalf("get miss: %v", err)
	}
	if found {
		t.Fatalf("get miss: found=true, want false (hash=%d)", hash)
	}

	if err := h.SetChannelAccessHash(ctx, 0, 99, 123456); err != nil {
		t.Fatalf("set: %v", err)
	}

	hash, found, err = h.GetChannelAccessHash(ctx, 0, 99)
	if err != nil {
		t.Fatalf("get hit: %v", err)
	}
	if !found || hash != 123456 {
		t.Fatalf("get hit: got found=%v hash=%d, want found=true hash=123456", found, hash)
	}
}
