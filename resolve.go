package main

import (
	"context"
	"strings"
	"sync"

	"github.com/go-faster/errors"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

// resolvedPeersMax bounds the map. An agent that resolves thousands of
// usernames should not grow it forever, and the entries are worth nothing once
// stale, so the whole map is dropped rather than evicted one by one.
const resolvedPeersMax = 512

// resolvedPeers remembers what came back with a peer resolution.
//
// Deliberately not the dialog cache: a resolved chat need not be a dialog, and
// list_chats promises dialogs. This only answers "what is this chat" about one
// a tool has already been pointed at.
type resolvedPeers struct {
	mu sync.RWMutex
	m  map[int64]UnreadChannel
}

func newResolvedPeers() *resolvedPeers {
	return &resolvedPeers{m: make(map[int64]UnreadChannel)}
}

func (r *resolvedPeers) get(id int64) (UnreadChannel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ch, ok := r.m[id]

	return ch, ok
}

func (r *resolvedPeers) put(ch UnreadChannel) {
	if ch.ID == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.m) >= resolvedPeersMax {
		clear(r.m)
	}
	r.m[ch.ID] = ch
}

// resolver resolves a peer by name and keeps the chat metadata that comes back
// with it.
//
// contacts.resolveUsername answers with the whole chat — its title, and whether
// it is a broadcast channel or a supergroup — and the default gotd resolver
// extracts the input peer and drops the rest. Every caller then has a peer it
// can address and no idea what it is, one field access away from knowing.
type resolver struct {
	// Resolver handles everything but a domain, notably a phone number.
	peer.Resolver

	api   *tg.Client
	peers *resolvedPeers
}

func newResolver(api *tg.Client, peers *resolvedPeers) resolver {
	return resolver{
		Resolver: peer.DefaultResolver(api),
		api:      api,
		peers:    peers,
	}
}

func (r resolver) ResolveDomain(ctx context.Context, domain string) (tg.InputPeerClass, error) {
	res, err := r.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: domain,
	})
	if err != nil {
		return nil, errors.Wrap(err, "resolve")
	}

	ent := peer.EntitiesFromResult(res)
	p, err := ent.ExtractPeer(res.Peer)
	if err != nil {
		return nil, errors.Wrap(err, "extract peer")
	}
	if ch, ok := channelFromEntities(ent, res.Peer); ok {
		r.peers.put(ch)
	}

	return p, nil
}

// channelFromEntities builds an UnreadChannel from a resolution result. The
// unread counts stay zero: a resolution says what a chat is, not how much of it
// is unread, and only a dialog knows that.
func channelFromEntities(ent entities, p tg.PeerClass) (UnreadChannel, bool) {
	switch v := p.(type) {
	case *tg.PeerChannel:
		c, ok := ent.Channel(v.ChannelID)
		if !ok {
			return UnreadChannel{}, false
		}

		return channelFromEntity(c), true
	case *tg.PeerChat:
		c, ok := ent.Chat(v.ChatID)
		if !ok {
			return UnreadChannel{}, false
		}

		return UnreadChannel{
			ID:    c.ID,
			Title: c.Title,
			Type:  typeGroup,
			peer:  &tg.InputPeerChat{ChatID: c.ID},
		}, true
	case *tg.PeerUser:
		u, ok := ent.User(v.UserID)
		if !ok {
			return UnreadChannel{}, false
		}
		username, _ := u.GetUsername()
		accessHash, _ := u.GetAccessHash()

		return UnreadChannel{
			ID:       u.ID,
			Title:    userTitle(u),
			Username: username,
			Type:     typePrivate,
			peer:     &tg.InputPeerUser{UserID: u.ID, AccessHash: accessHash},
		}, true
	default:
		return UnreadChannel{}, false
	}
}

// userTitle names a user the way a dialog list does, falling back to the
// username when the account has no name to show.
func userTitle(u *tg.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name, _ = u.GetUsername()
	}

	return name
}
