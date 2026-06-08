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
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/contrib/bbolt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := rootCmd().ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
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
				return runAuth(cmd.Context(), cfg)
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
				return runServe(cmd.Context(), cfg)
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
func runServe(ctx context.Context, cfg Config) error {
	lg, err := newLogger(cfg)
	if err != nil {
		return err
	}

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
	registerCacheHandlers(&dispatcher, cache, msgs)

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
		Logger: lg.Named("updates"),
	})

	client, waiter, err := newClient(cfg, mgr, lg)
	if err != nil {
		return err
	}

	return waiter.Run(ctx, func(ctx context.Context) error {
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

			srv := &server{api: client.API(), cache: cache, msgs: msgs, lg: lg}
			m := mcp.NewServer(&mcp.Implementation{
				Name:    "tgmcp",
				Version: "0.1.0",
			}, nil)
			srv.register(m)

			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
				return m
			}, nil)
			httpSrv := &http.Server{
				Addr:              cfg.HTTPAddr,
				Handler:           logHTTP(lg, handler),
				ReadHeaderTimeout: 10 * time.Second,
			}

			go func() {
				<-ctx.Done()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := httpSrv.Shutdown(shutdownCtx); err != nil {
					lg.Error("Shutdown MCP HTTP server", zap.Error(err))
				}
			}()

			g, ctx := errgroup.WithContext(ctx)

			// Run the updates manager: it loads the persisted state, seeds the
			// dialog cache once via OnStart, then keeps it live, recovering gaps
			// with getDifference.
			g.Go(func() error {
				return mgr.Run(ctx, client.API(), self.ID, updates.AuthOptions{
					OnStart: func(ctx context.Context) {
						// Seed the cache from persistent storage. Only fetch the
						// full dialog list when nothing is persisted (first run);
						// on later starts the updates manager reconciles the
						// persisted cache via getDifference.
						n, err := cache.loadFromStore()
						if err != nil {
							lg.Error("Load persisted dialogs", zap.Error(err))
						}
						if n == 0 {
							if err := bootstrapDialogs(ctx, client.API(), cache); err != nil {
								lg.Error("Bootstrap dialogs", zap.Error(err))
								return
							}
						} else {
							lg.Info("Loaded persisted dialogs", zap.Int("count", n))
						}
						lg.Info("Authorized, serving MCP over HTTP", zap.String("addr", cfg.HTTPAddr))
					},
				})
			})

			g.Go(func() error {
				if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					return errors.Wrap(err, "http serve")
				}

				return nil
			})

			return g.Wait()
		})
	})
}

// statusRecorder captures the HTTP status code written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logHTTP wraps an http.Handler and logs every request at debug level with its
// method, path, MCP session id, status, and duration.
func logHTTP(lg *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		lg.Debug("HTTP request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("session", r.Header.Get("Mcp-Session-Id")),
			zap.Int("status", rec.status),
			zap.Duration("took", time.Since(start)),
			zap.String("remote", r.RemoteAddr),
		)
	})
}
