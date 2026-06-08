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
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
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
func runServe(ctx context.Context, cfg Config) error {
	client, waiter, lg, err := newClient(cfg, nil)
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

			srv := &server{api: client.API(), lg: lg}
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

			lg.Info("Authorized, serving MCP over HTTP", zap.String("addr", cfg.HTTPAddr))

			if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return errors.Wrap(err, "http serve")
			}

			return nil
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
