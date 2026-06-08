package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-faster/errors"
	"github.com/mdp/qrterminal/v3"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// renderQR prints token as an ASCII QR code to stderr using Unicode
// half-block characters so that 2 QR rows fit in 1 terminal line.
func renderQR(token qrlogin.Token) error {
	qrterminal.Generate(token.URL(), qrterminal.L, os.Stderr)

	return nil
}

// runAuth performs the interactive QR login flow and stores the session so that
// the server can later run non-interactively.
func runAuth(ctx context.Context, cfg Config) error {
	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(&dispatcher)

	client, waiter, _, err := newClient(cfg, dispatcher)
	if err != nil {
		return err
	}

	return waiter.Run(ctx, func(ctx context.Context) error {
		return client.Run(ctx, func(ctx context.Context) error {
			show := func(ctx context.Context, token qrlogin.Token) error {
				fmt.Fprintln(os.Stderr, "\nScan this QR code with your Telegram app (Settings → Devices → Link Desktop Device):")

				if err := renderQR(token); err != nil {
					// Non-fatal: fall back to URL only.
					fmt.Fprintf(os.Stderr, "(QR render error: %v)\n", err)
				}

				fmt.Fprintf(os.Stderr, "Or open: %s\n\nWaiting for scan...\n", token.URL())

				return nil
			}

			if _, err := client.QR().Auth(ctx, loggedIn, show); err != nil {
				if !tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
					return errors.Wrap(err, "QR auth")
				}

				// 2FA cloud password required.
				if err := handle2FA(ctx, client.Auth()); err != nil {
					return err
				}
			}

			self, err := client.Self(ctx)
			if err != nil {
				return errors.Wrap(err, "self")
			}
			fmt.Fprintf(os.Stderr, "Logged in as %s (id %d). Session saved to %s\n",
				self.FirstName, self.ID, cfg.SessionDir)

			return nil
		})
	})
}

// handle2FA prompts for the 2FA cloud password and submits it, retrying on
// wrong password until the user gets it right or hits EOF.
func handle2FA(ctx context.Context, a *auth.Client) error {
	for {
		fmt.Fprint(os.Stderr, "Enter 2FA password: ")

		pwd, err := readLine()
		if err != nil {
			return errors.Wrap(err, "read 2FA password")
		}

		_, err = a.Password(ctx, pwd)
		if errors.Is(err, auth.ErrPasswordInvalid) {
			fmt.Fprintln(os.Stderr, "Wrong password, try again.")
			continue
		}
		if err != nil {
			return errors.Wrap(err, "2FA password")
		}

		return nil
	}
}

func readLine() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(line), nil
}
