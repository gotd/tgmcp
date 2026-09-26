package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/zctx"
	"go.uber.org/zap"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// attributionMode selects how a message is marked as sent by an agent.
type attributionMode string

const (
	// attributionOff sends messages unmarked, as the plain account.
	attributionOff attributionMode = "off"
	// attributionFooter appends an italic footer to the message text.
	attributionFooter attributionMode = "footer"
	// attributionBot routes the message through an inline echo bot, so that
	// Telegram itself renders a "via @bot" header on the message.
	attributionBot attributionMode = "bot"
)

// defaultAgentFooter is used when TG_AGENT_FOOTER is not set.
const defaultAgentFooter = "sent by an agent"

func parseAttributionMode(s string) (attributionMode, error) {
	switch m := attributionMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", attributionOff:
		return attributionOff, nil
	case attributionFooter, attributionBot:
		return m, nil
	default:
		return "", errors.Errorf("unknown TG_ATTRIBUTION %q, want %q, %q or %q",
			s, attributionOff, attributionFooter, attributionBot)
	}
}

// footerOptions returns the styled options that append the italic agent footer
// after body, or body alone when footer is empty.
func footerOptions(body styling.StyledTextOption, footer string) []styling.StyledTextOption {
	if footer == "" {
		return []styling.StyledTextOption{body}
	}

	return []styling.StyledTextOption{
		body,
		styling.Plain("\n\n"),
		styling.Italic(footer),
	}
}

// messageOptions renders a message body and reports which attribution it
// carries. Only [attributionFooter] changes the text: under [attributionBot]
// the mark is the "via @bot" header Telegram renders on the message itself.
func (s *server) messageOptions(text, mode string) ([]styling.StyledTextOption, attributionMode, error) {
	body, err := s.styledText(text, mode)
	if err != nil {
		return nil, "", err
	}
	switch s.attribution {
	case attributionFooter:
		return footerOptions(body, s.footer), attributionFooter, nil
	case attributionBot:
		return []styling.StyledTextOption{body}, attributionBot, nil
	default:
		return []styling.StyledTextOption{body}, attributionOff, nil
	}
}

// styledCaption renders a media caption and reports which attribution was
// applied. Uploads cannot go through the echo bot, since the inline result
// would have to carry the file, so [attributionBot] degrades to the footer
// here regardless of strictness.
func (s *server) styledCaption(text, mode string) ([]styling.StyledTextOption, attributionMode, error) {
	body, err := s.styledText(text, mode)
	if err != nil {
		return nil, "", err
	}
	if s.attribution == attributionOff {
		return []styling.StyledTextOption{body}, attributionOff, nil
	}

	return footerOptions(body, s.footer), attributionFooter, nil
}

// sendText sends in.Text to p and reports which attribution was applied.
//
// In [attributionBot] mode the message goes through the inline echo bot so
// that Telegram renders a "via @bot" header. That path depends on the bot
// process being reachable: when it is not, a strict configuration fails the
// call and a lenient one falls back to the footer.
func (s *server) sendText(ctx context.Context, p tg.InputPeerClass, in sendMessageInput) (tg.UpdatesClass, attributionMode, error) {
	if isRichMode(in.ParseMode) {
		return s.sendRich(ctx, p, in)
	}
	if s.attribution == attributionBot {
		upd, err := s.sendViaBot(ctx, p, in)
		if err == nil {
			return upd, attributionBot, nil
		}
		if s.strict {
			return nil, "", errors.Wrap(err, "via-bot attribution")
		}
		zctx.From(ctx).Warn("Falling back to footer attribution", zap.Error(err))
	}

	body, err := s.styledText(in.Text, in.ParseMode)
	if err != nil {
		return nil, "", err
	}

	b := message.NewSender(s.api).To(p)
	if in.Silent {
		b.Silent()
	}
	if in.NoWebpage {
		b.NoWebpage()
	}
	if in.ReplyToMessageID > 0 {
		b.Reply(in.ReplyToMessageID)
	}

	if s.attribution == attributionOff {
		upd, err := b.StyledText(ctx, body)

		return upd, attributionOff, err
	}

	upd, err := b.StyledText(ctx, footerOptions(body, s.footer)...)

	return upd, attributionFooter, err
}

// sendViaBot asks the echo bot for an inline result carrying the message and
// sends it, which sets via_bot_id on the resulting message.
func (s *server) sendViaBot(ctx context.Context, p tg.InputPeerClass, in sendMessageInput) (tg.UpdatesClass, error) {
	bot, err := s.botUser(ctx)
	if err != nil {
		return nil, err
	}

	key, err := s.spool.put(inlinePayload{
		Text:      in.Text,
		ParseMode: in.ParseMode,
		Owner:     s.selfID,
	})
	if err != nil {
		return nil, err
	}

	res, err := s.api.MessagesGetInlineBotResults(ctx, &tg.MessagesGetInlineBotResultsRequest{
		Bot:   bot,
		Peer:  p,
		Query: key,
	})
	if err != nil {
		s.spool.drop(key)

		return nil, errors.Wrap(err, "get inline bot results")
	}
	if len(res.Results) == 0 {
		s.spool.drop(key)

		return nil, errors.New("echo bot returned no inline results")
	}

	randomID, err := randomInt64()
	if err != nil {
		return nil, err
	}

	req := &tg.MessagesSendInlineBotResultRequest{
		Peer:       p,
		RandomID:   randomID,
		QueryID:    res.QueryID,
		ID:         res.Results[0].GetID(),
		Silent:     in.Silent,
		ClearDraft: true,
	}
	if in.ReplyToMessageID > 0 {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: in.ReplyToMessageID})
	}

	upd, err := s.api.MessagesSendInlineBotResult(ctx, req)
	if err != nil {
		return nil, errors.Wrap(err, "send inline bot result")
	}

	return upd, nil
}

// botUser resolves the echo bot, preferring the configured username and
// otherwise the identity the running bot published. The result is cached: it
// does not change for the lifetime of the process.
//
// The bot token is not enough on its own. A bot that is not already a dialog
// cannot be looked up by the numeric ID inside the token: the generic resolver
// reads bare digits as a phone number.
func (s *server) botUser(ctx context.Context) (tg.InputUserClass, error) {
	s.botMu.Lock()
	defer s.botMu.Unlock()

	if s.bot != nil {
		return s.bot, nil
	}

	username := s.botUsername
	if username == "" {
		id, err := readEchoBotIdentity(s.sessionDir)
		if err != nil {
			return nil, errors.Wrap(err, "echo bot identity unavailable, is `tgmcp echobot` running? set TG_BOT_USERNAME to skip this lookup")
		}
		username = id.Username
	}

	target := "@" + username
	p, err := s.resolvePeer(ctx, target)
	if err != nil {
		return nil, errors.Wrapf(err, "resolve echo bot %q", target)
	}
	user, ok := p.(*tg.InputPeerUser)
	if !ok {
		return nil, errors.Errorf("echo bot %q is not a user", target)
	}

	s.bot = &tg.InputUser{UserID: user.UserID, AccessHash: user.AccessHash}

	return s.bot, nil
}

// botIDFromToken extracts the numeric user ID from a bot token, which has the
// form "<id>:<secret>".
func botIDFromToken(token string) (int64, error) {
	id, _, ok := strings.Cut(token, ":")
	if !ok {
		return 0, errors.New("malformed bot token")
	}

	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, errors.Wrap(err, "parse bot id")
	}

	return n, nil
}
