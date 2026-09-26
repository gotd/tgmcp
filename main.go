// Command tgmcp is a Model Context Protocol (MCP) server backed by the gotd
// Telegram client. It exposes tools to list channels with unread messages and
// to read those unread messages.
//
// Usage:
//
//	tgmcp auth     # one-time interactive login, stores the session
//	tgmcp serve    # run the MCP server over HTTP
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/gotd/contrib/bbolt"
	"github.com/gotd/log/logzap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	slogzap "github.com/samber/slog-zap/v2"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/go-faster/gooners/mcpauth"
	"github.com/go-faster/gooners/mcpcmd"
	_ "github.com/go-faster/gooners/tunnel/cloudflared"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// modulePath is reported as the version of this build by [app.Run].
const modulePath = "github.com/gotd/tgmcp"

func main() {
	if err := rootCmd().ExecuteContext(context.Background()); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %+v\n", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "tgmcp",
		Short:         "MCP server for reading unread Telegram channel messages",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		&cobra.Command{
			Use:   "auth",
			Short: "Interactively log in and store the Telegram session",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, err := LoadConfig()
				if err != nil {
					return err
				}

				// Interactive command: no telemetry, own signal handling.
				ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
				defer cancel()

				return runAuth(ctx, cfg)
			},
		},
		&cobra.Command{
			Use:   "echobot",
			Short: "Run only the inline echo bot used for agent attribution",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, err := LoadConfig()
				if err != nil {
					return err
				}

				logCfg, err := zapConfig(cfg)
				if err != nil {
					return err
				}

				app.Run(func(ctx context.Context, lg *zap.Logger, _ *app.Telemetry) error {
					return runEchoBot(ctx, cfg, lg)
				},
					app.WithContext(cmd.Context()),
					app.WithServiceName("tgmcp-echobot"),
					app.WithModulePath(modulePath),
					app.WithZapConfig(logCfg),
				)

				return nil
			},
		},
		&cobra.Command{
			Use:   "serve",
			Short: "Run the MCP server over HTTP",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				cfg, err := LoadConfig()
				if err != nil {
					return err
				}

				logCfg, err := zapConfig(cfg)
				if err != nil {
					return err
				}

				// app.Run owns signal handling, graceful shutdown and process
				// exit, so it never returns.
				app.Run(func(ctx context.Context, lg *zap.Logger, t *app.Telemetry) error {
					return runServe(ctx, cfg, lg, t)
				},
					app.WithContext(cmd.Context()),
					app.WithServiceName("tgmcp"),
					app.WithModulePath(modulePath),
					app.WithZapConfig(logCfg),
				)

				return nil
			},
		},
	)

	return root
}

// runServe connects to Telegram using the stored session and serves the MCP
// protocol over HTTP using the streamable transport. It never prompts: if the
// session is missing or expired, it asks the user to run "tgmcp auth" first.
//
// The dialog list is loaded once at startup into an in-memory cache, which is
// then kept live by the gotd updates manager (gap-safe via getDifference). This
// avoids re-fetching the dialog list on every tool call, which caused
// FLOOD_WAIT.
func runServe(ctx context.Context, cfg Config, lg *zap.Logger, t *app.Telemetry) error {
	db, err := openStateDB(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			lg.Error("Close state database", zap.Error(err))
		}
	}()

	cache := newDialogCache(&dialogStore{db: db}, lg)
	msgs := &messageStore{db: db, cap: messageBufferCap}

	dispatcher := tg.NewUpdateDispatcher()
	registerCacheHandlers(&dispatcher, cache, msgs, lg.Named("update"))

	// api is set once the client is connected; the OnChannelTooLong callback
	// (invoked later, from the updates manager) refetches the affected channel.
	var api *tg.Client
	mgr := updates.New(updates.Config{
		Handler:      dispatcher,
		Storage:      bbolt.NewStateStorage(db),
		AccessHasher: accessHasher{db: db},
		OnChannelTooLong: func(channelID int64) {
			if api == nil {
				return
			}
			// Refresh asynchronously so we do not block update processing.
			go func() {
				if err := refreshChannel(ctx, api, cache, channelID); err != nil {
					lg.Warn("Refresh channel after difference too long",
						zap.Int64("channel_id", channelID), zap.Error(err))
				}
			}()
		},
		OnChannelInaccessible: func(channelID int64) {
			// The updates manager lost access to the channel (CHANNEL_PRIVATE)
			// and dropped it from tracking; drop it from the dialog cache too.
			cache.remove(channelID)
			lg.Info("Dropped inaccessible channel from cache",
				zap.Int64("channel_id", channelID))
		},
		OnTooLong: func() {
			if api == nil {
				return
			}
			// Account-wide gap too long to recover: re-load the whole list.
			go func() {
				if err := bootstrapDialogs(ctx, api, cache); err != nil {
					lg.Warn("Re-bootstrap dialogs after difference too long", zap.Error(err))
					return
				}
				lg.Info("Re-bootstrapped dialogs after difference too long")
			}()
		},
		Logger: logzap.New(lg.Named("updates")),
	})

	client, err := newClient(cfg, mgr, lg, t)
	if err != nil {
		return err
	}

	instrument, err := newMCPInstrument(t, cfg.LogPayloads)
	if err != nil {
		return err
	}

	blobs, err := newBlobStore(ctx, cfg, slog.New(slogzap.Option{Logger: lg.Named("blob")}.NewZapHandler()))
	if err != nil {
		return err
	}
	if r, ok := blobs.(interface{ Run(context.Context) }); ok {
		// Sweeps expired objects, and drops everything on shutdown.
		go r.Run(ctx)
	}

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return errors.Wrap(err, "auth status")
		}
		if !status.Authorized {
			return errors.New("not authorized: run `tgmcp auth` first to create a session")
		}

		self, err := client.Self(ctx)
		if err != nil {
			return errors.Wrap(err, "self")
		}

		// Publish the API for the OnChannelTooLong callback. Set before the
		// updates manager starts, so it is visible by the time updates flow.
		api = client.API()

		srv := &server{
			api:              client.API(),
			cache:            cache,
			resolved:         newResolvedPeers(),
			msgs:             msgs,
			lg:               lg,
			fileRootVal:      cfg.FileRoot,
			allowSend:        cfg.AllowSend,
			allowProfileEdit: cfg.AllowProfileEdit,
			allowInlineMedia: cfg.AllowInlineMedia,
			blobs:            blobs,
			attribution:      cfg.Attribution,
			strict:           cfg.Strict,
			footer:           cfg.AgentFooter,
			botToken:         cfg.BotToken,
			botUsername:      cfg.BotUsername,
			sessionDir:       cfg.SessionDir,
			spool:            newInlineSpool(cfg.SessionDir),
			selfID:           self.ID,
		}
		m := mcp.NewServer(&mcp.Implementation{
			Name:    "tgmcp",
			Version: "0.1.0",
		}, nil)
		srv.register(m)
		m.AddReceivingMiddleware(instrument.Middleware())

		// Serve stored media alongside MCP, so a client can fetch a file the
		// tool result only linked to. It is deliberately outside the auth
		// middleware: the URL is the capability, and a browser opening it
		// carries no MCP credential.
		routes := map[string]http.Handler{}
		if h, ok := blobs.(http.Handler); ok {
			path, err := blobMountPath(cfg.BlobBaseURL)
			if err != nil {
				return err
			}
			routes[path] = h
			lg.Info("Serving blobs", zap.String("path", path), zap.String("base_url", cfg.BlobBaseURL))
		}

		auth := mcpauth.Middleware(cfg.Auth, mcpauth.Options{Name: "tgmcp"})
		if auth == nil {
			lg.Warn("MCP endpoint is unauthenticated, anyone who reaches it acts as this account",
				zap.String("addr", cfg.HTTPAddr))
		} else {
			lg.Info("Requiring credential on MCP endpoint", zap.String("header", cfg.Auth.Header))
		}

		// Telemetry wraps auth rather than the other way round, so a rejected
		// request is still counted and logged. Neither wraps the health probes.
		middleware := func(next http.Handler) http.Handler {
			if auth != nil {
				next = auth(next)
			}

			return instrumentHTTP(lg, t, next)
		}

		// The listener comes up before the dialog cache is seeded, so a tool
		// call can arrive against an empty one. /readyz is what keeps a proxy
		// from routing to it that early; the process is healthy either way.
		var seeded atomic.Bool

		g, ctx := errgroup.WithContext(ctx)

		// The echo bot is a second Telegram account in the same process: it
		// logs in with the bot token while everything else uses the user
		// session. Running it here rather than beside the server keeps the two
		// on one lifecycle, which is what stopped them drifting apart.
		//
		// A username without a token means the bot is somebody else's to run.
		if cfg.Attribution == attributionBot && cfg.BotToken != "" {
			g.Go(func() error {
				superviseEchoBot(ctx, cfg, lg.Named("echobot"))

				return nil
			})
		}

		// Run the updates manager: it loads the persisted state, seeds the
		// dialog cache once via OnStart, then keeps it live, recovering gaps
		// with getDifference.
		g.Go(func() error {
			return mgr.Run(ctx, client.API(), self.ID, updates.AuthOptions{
				OnStart: func(ctx context.Context) {
					// Seed from persistent storage first, so the cache is
					// usable even if the refetch below fails.
					n, err := cache.loadFromStore()
					if err != nil {
						lg.Error("Load persisted dialogs", zap.Error(err))
					}
					if n > 0 {
						lg.Info("Loaded persisted dialogs", zap.Int("count", n))
					}

					// Then refetch, on every start rather than only the first.
					// getDifference reports activity on dialogs already known;
					// it never enumerates one joined since, and the update
					// handlers drop the traffic that would introduce it
					// anyway: own messages, service messages and supergroups.
					// Without this the cache is a snapshot of whenever the
					// store was first written.
					if err := bootstrapDialogs(ctx, client.API(), cache); err != nil {
						lg.Error("Bootstrap dialogs", zap.Error(err))
						if n == 0 {
							return
						}
					} else {
						lg.Info("Refreshed dialogs", zap.Int("count", cache.len()))
					}
					seeded.Store(true)
					lg.Info("Authorized, serving MCP over HTTP", zap.String("addr", cfg.HTTPAddr))
				},
			})
		})

		g.Go(func() error {
			if err := cfg.Transport.Run(ctx, mcpcmd.RunOptions{
				Name:       "tgmcp",
				Handler:    func(*http.Request) *mcp.Server { return m },
				Middleware: middleware,
				Routes:     routes,
				Ready: func() error {
					if !seeded.Load() {
						return errors.New("dialog cache is not seeded yet")
					}

					return nil
				},
				Logger: slog.New(slogzap.Option{Logger: lg.Named("http")}.NewZapHandler()),
			}); err != nil {
				return errors.Wrap(err, "http serve")
			}

			return nil
		})

		return g.Wait()
	})
}
