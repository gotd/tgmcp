// Command tgmcp is a Model Context Protocol (MCP) server backed by the gotd
// Telegram client. It exposes tools to list channels with unread messages and
// to read those unread messages.
//
// Usage:
//
//	tgmcp auth     # one-time interactive login, stores the session
//	tgmcp serve    # run the MCP server over stdio
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/go-faster/errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
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
			Short: "Run the MCP server over stdio",
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
// protocol over stdio. It never prompts: if the session is missing or expired,
// it asks the user to run "tgmcp auth" first.
func runServe(ctx context.Context, cfg Config) error {
	client, lg, err := newClient(cfg)
	if err != nil {
		return err
	}

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return errors.Wrap(err, "auth status")
		}
		if !status.Authorized {
			return errors.New("not authorized: run `tgmcp auth` first to create a session")
		}
		lg.Info("Authorized, starting MCP server")

		srv := &server{api: client.API()}
		m := mcp.NewServer(&mcp.Implementation{
			Name:    "tgmcp",
			Version: "0.1.0",
		}, nil)
		srv.register(m)

		return m.Run(ctx, &mcp.StdioTransport{})
	})
}
