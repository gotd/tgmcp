package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-faster/errors"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// terminalAuth implements auth.UserAuthenticator by reading the login code (and
// 2FA password, if any) from stdin. It is only used by the "auth" subcommand,
// which is interactive; the "serve" subcommand never prompts.
type terminalAuth struct {
	phone string
}

func (a terminalAuth) Phone(_ context.Context) (string, error) { return a.phone, nil }

func (a terminalAuth) Password(_ context.Context) (string, error) {
	fmt.Fprint(os.Stderr, "Enter 2FA password: ")
	return readLine()
}

func (a terminalAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Fprint(os.Stderr, "Enter login code: ")
	return readLine()
}

func (a terminalAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	fmt.Fprintln(os.Stderr, "Accepting terms of service:", tos.Text)
	return nil
}

func (a terminalAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("sign up is not supported; register the account in an official Telegram client first")
}

func readLine() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// runAuth performs the interactive login flow and stores the session so that
// the server can later run non-interactively.
func runAuth(ctx context.Context, cfg Config) error {
	client, _, err := newClient(cfg)
	if err != nil {
		return err
	}

	return client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(terminalAuth{phone: cfg.Phone}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return errors.Wrap(err, "auth")
		}

		self, err := client.Self(ctx)
		if err != nil {
			return errors.Wrap(err, "self")
		}
		fmt.Fprintf(os.Stderr, "Logged in as %s (id %d). Session saved to %s\n",
			self.FirstName, self.ID, cfg.SessionDir)
		return nil
	})
}
