package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

// TestPeerInfoHasNoSecrets guards against a phone number or an access hash
// reaching a tool result.
//
// It is the caller's own number in get_me and someone else's in resolve_peer
// and search_chats, and a tool result is read by a model and kept in a
// transcript. Nothing in the tool surface needs either back: a phone is an
// input to resolve_peer, never an output, and no tool takes an access hash at
// all — callers pass ids, usernames or "me".
//
// The check is on the serialized form because the leak would come back the way
// it arrived — by copying every field of [tg.User] into PeerInfo.
func TestPeerInfoHasNoSecrets(t *testing.T) {
	const phone = "79001234567"

	const accessHash = int64(1234567890123456789)

	info := infoFromUser(&tg.User{
		ID:         1,
		FirstName:  "a",
		Username:   "b",
		Phone:      phone,
		AccessHash: accessHash,
	})

	out, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), phone) {
		t.Errorf("PeerInfo carries the phone number: %s", out)
	}
	if strings.Contains(strings.ToLower(string(out)), "phone") {
		t.Errorf("PeerInfo has a phone field: %s", out)
	}
	if strings.Contains(string(out), strconv.FormatInt(accessHash, 10)) {
		t.Errorf("PeerInfo carries the access hash: %s", out)
	}
}
