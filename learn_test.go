package main

import (
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/gotd/td/tg"
)

// Username and AccessHash are conditional fields: assigning the struct member
// leaves the flag clear, and channelFromEntity reads them through the getters.
func learnEntity(title string, hash int64) UnreadChannel {
	c := &tg.Channel{ID: 11, Title: title}
	c.SetUsername("news")
	c.SetAccessHash(hash)

	return channelFromEntity(c)
}

func TestCacheLearn(t *testing.T) {
	cache := newDialogCache(nil, zap.NewNop())

	if !cache.learn(learnEntity("Daily News", 110)) {
		t.Fatal("a channel the cache lacks reported no change")
	}
	ch, ok := cache.get(11)
	if !ok || ch.Title != "Daily News" {
		t.Fatalf("got %+v, want the learned channel", ch)
	}

	// updateChannel fires constantly. An update carrying nothing new must not
	// report a change, or every one of them writes a dialog to disk.
	if cache.learn(learnEntity("Daily News", 110)) {
		t.Error("an unchanged update reported a change")
	}

	// A rename is a change even though the id is the same.
	if !cache.learn(learnEntity("Nightly News", 110)) {
		t.Error("a rename reported no change")
	}
	// So is a rotated access hash behind identical visible metadata, since it
	// is what addresses the chat.
	if !cache.learn(learnEntity("Nightly News", 999)) {
		t.Error("a rotated access hash reported no change")
	}
}

// Metadata must not carry the counters with it: the unread count is maintained
// by other handlers, and a rename says nothing about it.
func TestCacheLearnKeepsUnread(t *testing.T) {
	cache := newDialogCache(nil, zap.NewNop())
	cache.learn(learnEntity("Daily News", 110))
	cache.observeIncoming(&tg.PeerChannel{ChannelID: 11}, func() (UnreadChannel, bool) {
		return learnEntity("Daily News", 110), true
	})

	cache.learn(learnEntity("Nightly News", 110))

	ch, _ := cache.get(11)
	if ch.UnreadCount != 1 {
		t.Errorf("unread %d, want 1 kept across the rename", ch.UnreadCount)
	}
	if ch.Title != "Nightly News" {
		t.Errorf("title %q, want the renamed one", ch.Title)
	}
}

// Run with -race: learn compares and writes under one lock, so a concurrent
// unread increment must not be lost to a metadata update.
func TestCacheLearnConcurrent(t *testing.T) {
	cache := newDialogCache(nil, zap.NewNop())
	cache.learn(learnEntity("Daily News", 110))

	const n = 200

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range n {
			cache.learn(learnEntity("Daily News", int64(i)))
		}
	}()
	go func() {
		defer wg.Done()
		for range n {
			cache.observeIncoming(&tg.PeerChannel{ChannelID: 11}, func() (UnreadChannel, bool) {
				return learnEntity("Daily News", 110), true
			})
		}
	}()
	wg.Wait()

	ch, _ := cache.get(11)
	if ch.UnreadCount != n {
		t.Errorf("unread %d, want %d: a metadata update dropped an increment", ch.UnreadCount, n)
	}
}
