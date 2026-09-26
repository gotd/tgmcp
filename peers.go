package main

import (
	"context"
	"strings"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gotd/td/tg"
)

// PeerInfo describes a resolved Telegram peer: a user, bot, group, supergroup
// or broadcast channel.
type PeerInfo struct {
	Type      string `json:"type" jsonschema:"peer type: private, bot, group, supergroup or channel"`
	ID        int64  `json:"id" jsonschema:"Telegram peer ID"`
	Title     string `json:"title" jsonschema:"display name: full name for users, title for chats"`
	Username  string `json:"username,omitempty" jsonschema:"public @username, if any"`
	FirstName string `json:"first_name,omitempty" jsonschema:"user first name"`
	LastName  string `json:"last_name,omitempty" jsonschema:"user last name"`
	About     string `json:"about,omitempty" jsonschema:"bio for users, description for chats"`

	Bot           bool `json:"bot,omitempty" jsonschema:"true if the user is a bot"`
	Premium       bool `json:"premium,omitempty" jsonschema:"true if the user has Telegram Premium"`
	Verified      bool `json:"verified,omitempty" jsonschema:"true if the peer is verified"`
	Scam          bool `json:"scam,omitempty" jsonschema:"true if the peer is flagged as scam"`
	Fake          bool `json:"fake,omitempty" jsonschema:"true if the peer is flagged as fake"`
	Deleted       bool `json:"deleted,omitempty" jsonschema:"true if the user account is deleted"`
	Contact       bool `json:"contact,omitempty" jsonschema:"true if the user is in our contacts"`
	MutualContact bool `json:"mutual_contact,omitempty" jsonschema:"true if the contact is mutual"`
	Blocked       bool `json:"blocked,omitempty" jsonschema:"true if we blocked the peer"`

	Broadcast         bool `json:"broadcast,omitempty" jsonschema:"true for broadcast channels"`
	Megagroup         bool `json:"megagroup,omitempty" jsonschema:"true for supergroups"`
	ParticipantsCount int  `json:"participants_count,omitempty" jsonschema:"number of participants, if known"`

	InDialogs   bool `json:"in_dialogs" jsonschema:"true if the peer is present in the cached dialog list"`
	UnreadCount int  `json:"unread_count,omitempty" jsonschema:"unread messages, when the peer is a cached dialog"`
}

// peerInfo fetches full information about a resolved peer.
func (s *server) peerInfo(ctx context.Context, p tg.InputPeerClass) (PeerInfo, error) {
	var (
		info PeerInfo
		err  error
	)
	switch v := p.(type) {
	case *tg.InputPeerSelf:
		info, err = s.userInfo(ctx, &tg.InputUserSelf{})
	case *tg.InputPeerUser:
		info, err = s.userInfo(ctx, &tg.InputUser{UserID: v.UserID, AccessHash: v.AccessHash})
	case *tg.InputPeerChat:
		info, err = s.chatInfo(ctx, v.ChatID)
	case *tg.InputPeerChannel:
		info, err = s.channelInfo(ctx, &tg.InputChannel{ChannelID: v.ChannelID, AccessHash: v.AccessHash})
	default:
		return PeerInfo{}, errors.Errorf("unsupported peer type %T", p)
	}
	if err != nil {
		return PeerInfo{}, err
	}

	s.applyDialog(&info)

	return info, nil
}

// applyDialog annotates info with the cached dialog state, if the peer is one.
func (s *server) applyDialog(info *PeerInfo) {
	if s.cache == nil {
		return
	}

	var key string
	switch info.Type {
	case "group":
		key = dialogKeyParts("chat", info.ID)
	case "supergroup", "channel":
		key = dialogKeyParts("channel", info.ID)
	default:
		key = dialogKeyParts("user", info.ID)
	}

	ch, ok := s.cache.getKey(key)
	if !ok {
		return
	}

	info.InDialogs = true
	info.UnreadCount = ch.UnreadCount
}

func (s *server) userInfo(ctx context.Context, id tg.InputUserClass) (PeerInfo, error) {
	full, err := s.api.UsersGetFullUser(ctx, id)
	if err != nil {
		return PeerInfo{}, errors.Wrap(err, "users.getFullUser")
	}

	u, ok := userByID(full.Users, full.FullUser.ID)
	if !ok {
		return PeerInfo{}, errors.Errorf("user %d not found in response", full.FullUser.ID)
	}

	info := infoFromUser(u)
	info.About, _ = full.FullUser.GetAbout()
	info.Blocked = full.FullUser.Blocked

	return info, nil
}

// infoFromUser builds peer info from a user entity, without the extra fields
// that only users.getFullUser provides.
func infoFromUser(u *tg.User) PeerInfo {
	info := PeerInfo{
		Type:          "private",
		ID:            u.ID,
		Title:         displayName(u),
		Username:      primaryUsername(u),
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Bot:           u.Bot,
		Premium:       u.Premium,
		Verified:      u.Verified,
		Scam:          u.Scam,
		Fake:          u.Fake,
		Deleted:       u.Deleted,
		Contact:       u.Contact,
		MutualContact: u.MutualContact,
	}
	if u.Bot {
		info.Type = "bot"
	}

	return info
}

// infoFromChannel builds peer info from a channel entity, without the extra
// fields that only channels.getFullChannel provides.
func infoFromChannel(c *tg.Channel) PeerInfo {
	username, _ := c.GetUsername()
	info := PeerInfo{
		Type:      "channel",
		ID:        c.ID,
		Title:     c.Title,
		Username:  username,
		Broadcast: c.Broadcast,
		Megagroup: c.Megagroup,
		Verified:  c.Verified,
		Scam:      c.Scam,
		Fake:      c.Fake,
	}
	if c.Megagroup {
		info.Type = "supergroup"
	}

	return info
}

func (s *server) chatInfo(ctx context.Context, chatID int64) (PeerInfo, error) {
	full, err := s.api.MessagesGetFullChat(ctx, chatID)
	if err != nil {
		return PeerInfo{}, errors.Wrap(err, "messages.getFullChat")
	}

	info := PeerInfo{Type: "group", ID: chatID}
	if fc, ok := full.FullChat.(*tg.ChatFull); ok {
		info.About = fc.About
	}
	for _, c := range full.Chats {
		if chat, ok := c.(*tg.Chat); ok && chat.ID == chatID {
			info.Title = chat.Title
			info.ParticipantsCount = chat.ParticipantsCount

			break
		}
	}

	return info, nil
}

func (s *server) channelInfo(ctx context.Context, id *tg.InputChannel) (PeerInfo, error) {
	full, err := s.api.ChannelsGetFullChannel(ctx, id)
	if err != nil {
		return PeerInfo{}, errors.Wrap(err, "channels.getFullChannel")
	}

	info := PeerInfo{Type: "channel", ID: id.ChannelID}
	for _, c := range full.Chats {
		ch, ok := c.(*tg.Channel)
		if !ok || ch.ID != id.ChannelID {
			continue
		}
		info = infoFromChannel(ch)

		break
	}
	if fc, ok := full.FullChat.(*tg.ChannelFull); ok {
		info.About = fc.About
		info.ParticipantsCount = fc.ParticipantsCount
	}

	return info, nil
}

func userByID(users []tg.UserClass, id int64) (*tg.User, bool) {
	for _, uc := range users {
		if u, ok := uc.(*tg.User); ok && u.ID == id {
			return u, true
		}
	}

	return nil, false
}

// displayName is the human-readable name of a user: full name, falling back to
// the username.
func displayName(u *tg.User) string {
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return name
	}

	return primaryUsername(u)
}

// primaryUsername returns the main username of a user, considering both the
// legacy field and the collectible username list.
func primaryUsername(u *tg.User) string {
	if username, ok := u.GetUsername(); ok && username != "" {
		return username
	}
	usernames, _ := u.GetUsernames()
	for _, un := range usernames {
		if un.Active {
			return un.Username
		}
	}

	return ""
}

func (s *server) handleGetMe(ctx context.Context, _ *mcp.CallToolRequest, _ getMeInput) (*mcp.CallToolResult, getMeOutput, error) {
	info, err := s.peerInfo(ctx, &tg.InputPeerSelf{})
	if err != nil {
		return nil, getMeOutput{}, err
	}

	return nil, getMeOutput{User: info}, nil
}

func (s *server) handleSearchChats(ctx context.Context, _ *mcp.CallToolRequest, in searchChatsInput) (*mcp.CallToolResult, searchChatsOutput, error) {
	q := strings.TrimPrefix(strings.TrimSpace(in.Query), "@")
	if q == "" {
		return nil, searchChatsOutput{}, errors.New("query is required")
	}
	lim := in.Limit
	if lim <= 0 {
		lim = 20
	}
	if lim > 100 {
		lim = 100
	}

	found, err := s.api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: q, Limit: lim})
	if err != nil {
		return nil, searchChatsOutput{}, errors.Wrap(err, "contacts.search")
	}

	users := make(map[int64]*tg.User, len(found.Users))
	for _, uc := range found.Users {
		if u, ok := uc.(*tg.User); ok {
			users[u.ID] = u
		}
	}
	channels := make(map[int64]*tg.Channel, len(found.Chats))
	chats := make(map[int64]*tg.Chat, len(found.Chats))
	for _, cc := range found.Chats {
		switch c := cc.(type) {
		case *tg.Channel:
			channels[c.ID] = c
		case *tg.Chat:
			chats[c.ID] = c
		}
	}

	resolve := func(peers []tg.PeerClass) []PeerInfo {
		out := make([]PeerInfo, 0, len(peers))
		for _, p := range peers {
			var info PeerInfo
			switch v := p.(type) {
			case *tg.PeerUser:
				u, ok := users[v.UserID]
				if !ok {
					continue
				}
				info = infoFromUser(u)
			case *tg.PeerChannel:
				c, ok := channels[v.ChannelID]
				if !ok {
					continue
				}
				info = infoFromChannel(c)
			case *tg.PeerChat:
				c, ok := chats[v.ChatID]
				if !ok {
					continue
				}
				info = PeerInfo{
					Type:              "group",
					ID:                c.ID,
					Title:             c.Title,
					ParticipantsCount: c.ParticipantsCount,
				}
			default:
				continue
			}
			s.applyDialog(&info)
			out = append(out, info)
		}

		return out
	}

	return nil, searchChatsOutput{
		MyResults:     resolve(found.MyResults),
		GlobalResults: resolve(found.Results),
		Limit:         lim,
	}, nil
}

func (s *server) handleResolvePeer(ctx context.Context, _ *mcp.CallToolRequest, in resolvePeerInput) (*mcp.CallToolResult, resolvePeerOutput, error) {
	if in.Target == "" {
		return nil, resolvePeerOutput{}, errors.New("target is required")
	}
	p, err := s.resolvePeer(ctx, in.Target)
	if err != nil {
		return nil, resolvePeerOutput{}, err
	}
	info, err := s.peerInfo(ctx, p)
	if err != nil {
		return nil, resolvePeerOutput{}, err
	}

	return nil, resolvePeerOutput{Peer: info}, nil
}
