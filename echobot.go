package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/go-faster/errors"
	"go.uber.org/zap"

	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// echoBotSessionDir is the sub-directory of the session directory holding the
// bot session, kept apart from the user session it accompanies.
const echoBotSessionDir = "bot"

// inlineCacheTime is how long Telegram may cache an inline answer. Every query
// carries a fresh single-use key, so caching would only ever serve a stale
// message.
const inlineCacheTime = 0

// answerNothing returns an empty result set, which clients render as "no
// results" instead of waiting for an answer that never comes.
func answerNothing(ctx context.Context, api *tg.Client, queryID int64) error {
	if _, err := api.MessagesSetInlineBotResults(ctx, &tg.MessagesSetInlineBotResultsRequest{
		Private:   true,
		QueryID:   queryID,
		CacheTime: inlineCacheTime,
		Results:   []tg.InputBotInlineResultClass{},
	}); err != nil {
		return errors.Wrap(err, "answer with no results")
	}

	return nil
}

// inlineQueryAllowed reports whether user may claim a payload spooled by
// owner.
//
// The owner is always allowed: an inline query is issued by the same account
// that spooled the payload, so the default needs no access list. Entries in
// allowed extend that to other accounts, for setups where a second account
// drives the same echo bot.
func inlineQueryAllowed(allowed map[int64]bool, owner, user int64) bool {
	if owner != 0 && user == owner {
		return true
	}

	return allowed[user]
}

// echoBotIdentityFile is where the echo bot publishes who it is, so that the
// MCP server can address it without being told its username separately.
const echoBotIdentityFile = "echobot.json"

// echoBotIdentity is the published identity of the echo bot.
type echoBotIdentity struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func echoBotIdentityPath(sessionDir string) string {
	return filepath.Join(sessionDir, echoBotIdentityFile)
}

// publishEchoBotIdentity records the bot identity for the MCP server to read.
//
// Resolving the bot from the token alone does not work: a bot that is not
// already a dialog cannot be looked up by numeric ID, and the generic resolver
// reads bare digits as a phone number.
func publishEchoBotIdentity(sessionDir string, id echoBotIdentity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return errors.Wrap(err, "marshal identity")
	}
	if err := os.WriteFile(echoBotIdentityPath(sessionDir), data, 0o600); err != nil {
		return errors.Wrap(err, "write identity")
	}

	return nil
}

// readEchoBotIdentity loads the identity published by [runEchoBot].
func readEchoBotIdentity(sessionDir string) (echoBotIdentity, error) {
	data, err := os.ReadFile(echoBotIdentityPath(sessionDir))
	if err != nil {
		return echoBotIdentity{}, errors.Wrap(err, "read identity")
	}

	var id echoBotIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return echoBotIdentity{}, errors.Wrap(err, "unmarshal identity")
	}
	if id.Username == "" {
		return echoBotIdentity{}, errors.New("published identity has no username")
	}

	return id, nil
}

// Bounds on how long the supervisor waits before restarting the bot. The first
// retry is quick because the usual cause is a connection that dropped; the cap
// keeps a bot that cannot authenticate at all from reconnecting in a loop.
const (
	echoBotMinRetry = time.Second
	echoBotMaxRetry = time.Minute
)

// superviseEchoBot runs the echo bot until ctx ends, restarting it whenever it
// stops, and never reports an error.
//
// It is what lets the bot share a process with the MCP server. Attribution is
// best effort by design, with the footer as the fallback, so a bot that cannot
// start must degrade to that rather than take the server down with it. Run as
// its own command instead, a failure is the operator's to see and the exit code
// says so.
func superviseEchoBot(ctx context.Context, cfg Config, lg *zap.Logger) {
	supervise(ctx, lg, func(ctx context.Context) error {
		return runEchoBot(ctx, cfg, lg)
	})
}

// supervise restarts run with a backoff that doubles up to [echoBotMaxRetry],
// returning only when ctx ends.
func supervise(ctx context.Context, lg *zap.Logger, run func(context.Context) error) {
	delay := echoBotMinRetry
	for {
		start := time.Now()
		err := run(ctx)
		if ctx.Err() != nil {
			return
		}

		// A run that stayed up was not part of a crash loop, so the next
		// failure starts over from the shortest delay instead of inheriting a
		// backoff earned hours ago.
		if time.Since(start) >= echoBotMaxRetry {
			delay = echoBotMinRetry
		}

		lg.Warn("Echo bot stopped, restarting",
			zap.Duration("in", delay),
			zap.Error(err),
		)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}

		delay = min(delay*2, echoBotMaxRetry)
	}
}

// runEchoBot logs in with the bot token and answers inline queries with the
// message the query key stands for.
//
// It exists so that messages sent by the agent carry a "via @bot" header:
// Telegram sets via_bot_id only on messages that came from an inline result,
// so the text has to make a round trip through a bot the account can query.
func runEchoBot(ctx context.Context, cfg Config, lg *zap.Logger) error {
	if cfg.BotToken == "" {
		return errors.New("TG_BOT_TOKEN is not set")
	}

	spool := newInlineSpool(cfg.SessionDir)

	allowed := make(map[int64]bool, len(cfg.BotAllowedUsers))
	for _, id := range cfg.BotAllowedUsers {
		allowed[id] = true
	}

	// The bot needs a session of its own: the user session in the parent
	// directory belongs to a different account.
	botCfg := cfg
	botCfg.SessionDir = filepath.Join(cfg.SessionDir, echoBotSessionDir)

	dispatcher := tg.NewUpdateDispatcher()
	client, err := newClient(botCfg, dispatcher, lg, nil)
	if err != nil {
		return err
	}

	api := client.API()
	dispatcher.OnBotInlineQuery(func(ctx context.Context, _ tg.Entities, u *tg.UpdateBotInlineQuery) error {
		// Answering is best effort: a failure here must not stop the bot, and
		// the sender falls back to footer attribution.
		switch err := answerInlineQuery(ctx, api, spool, allowed, u); {
		case err == nil:
		case errors.Is(err, errNotOurQuery):
			// Anyone can open the bot and type into it. Expected, not a fault.
			lg.Debug("Ignored inline query",
				zap.Int64("query_id", u.QueryID),
				zap.Error(err),
			)
		default:
			lg.Warn("Answer inline query",
				zap.Int64("query_id", u.QueryID),
				zap.Error(err),
			)
		}

		return nil
	})

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return errors.Wrap(err, "auth status")
		}
		if !status.Authorized {
			if _, err := client.Auth().Bot(ctx, cfg.BotToken); err != nil {
				return errors.Wrap(err, "bot auth")
			}
		}

		self, err := client.Self(ctx)
		if err != nil {
			return errors.Wrap(err, "self")
		}
		// Catches a session left behind by a different bot: the stored
		// session wins over the token, so the ID would silently disagree.
		if id, err := botIDFromToken(cfg.BotToken); err == nil && id != self.ID {
			return errors.Errorf("session belongs to bot %d but TG_BOT_TOKEN is for %d, remove %s to re-authenticate",
				self.ID, id, botCfg.SessionDir)
		}

		if err := publishEchoBotIdentity(cfg.SessionDir, echoBotIdentity{
			ID:       self.ID,
			Username: self.Username,
		}); err != nil {
			return err
		}

		lg.Info("Echo bot ready",
			zap.String("username", self.Username),
			zap.Int64("id", self.ID),
		)

		<-ctx.Done()

		return ctx.Err()
	})
}

// answerInlineQuery resolves the query key and returns a single result holding
// the spooled message.
//
// allowed is the configured access list. The account that spooled the payload
// is always permitted, so the common case needs no configuration.
func answerInlineQuery(
	ctx context.Context,
	api *tg.Client,
	spool *inlineSpool,
	allowed map[int64]bool,
	u *tg.UpdateBotInlineQuery,
) error {
	payload, err := spool.read(u.Query)
	if err != nil {
		// Still answer, or the querying client shows a spinner until it times
		// out. An empty result set is also what a stranger should see.
		if aerr := answerNothing(ctx, api, u.QueryID); aerr != nil {
			return aerr
		}

		return err
	}

	// Authorize before consuming: otherwise anyone holding a key could destroy
	// a pending message without being able to send it.
	if !inlineQueryAllowed(allowed, payload.Owner, u.UserID) {
		if aerr := answerNothing(ctx, api, u.QueryID); aerr != nil {
			return aerr
		}

		return errors.Errorf("user %d may not claim inline payloads", u.UserID)
	}
	spool.drop(u.Query)

	opt, err := styledText(payload.Text, payload.ParseMode, nil)
	if err != nil {
		return err
	}

	var b entity.Builder
	if err := styling.Perform(&b, opt); err != nil {
		return errors.Wrap(err, "render payload")
	}
	text, entities := b.Complete()

	if _, err := api.MessagesSetInlineBotResults(ctx, &tg.MessagesSetInlineBotResultsRequest{
		// Private keeps the answer scoped to the querying account rather than
		// being served to anyone who types the same key.
		Private:   true,
		QueryID:   u.QueryID,
		CacheTime: inlineCacheTime,
		Results: []tg.InputBotInlineResultClass{
			&tg.InputBotInlineResult{
				ID:    u.Query,
				Type:  "article",
				Title: "Send",
				SendMessage: &tg.InputBotInlineMessageText{
					Message:  text,
					Entities: entities,
				},
			},
		},
	}); err != nil {
		return errors.Wrap(err, "set inline bot results")
	}

	return nil
}
