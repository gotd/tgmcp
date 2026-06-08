package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/time/rate"

	"github.com/gotd/td/telegram"
)

// newClient builds a Telegram client using the given configuration.
//
// handler is optional: when non-nil it is used as the update handler so that
// callers (e.g. the QR auth flow) can wire in a dispatcher. Pass nil for the
// default no-op handler.
//
// The returned waiter must wrap client.Run:
//
//	return waiter.Run(ctx, func(ctx context.Context) error {
//	    return client.Run(ctx, handler)
//	})
//
// Logs are written as JSON to stderr so that journald (or any supervisor) captures them.
func newClient(cfg Config, handler telegram.UpdateHandler) (*telegram.Client, *floodwait.Waiter, *zap.Logger, error) {
	if err := os.MkdirAll(cfg.SessionDir, 0o700); err != nil {
		return nil, nil, nil, errors.Wrap(err, "create session dir")
	}

	logCfg := zap.NewProductionConfig()
	logCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	lg, err := logCfg.Build()
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "build logger")
	}

	waiter := floodwait.NewWaiter().WithCallback(func(ctx context.Context, wait floodwait.FloodWait) {
		lg.Warn("Flood wait", zap.Duration("wait", wait.Duration))
	})

	client := telegram.NewClient(cfg.AppID, cfg.AppHash, telegram.Options{
		Logger: lg,
		SessionStorage: &telegram.FileSessionStorage{
			Path: filepath.Join(cfg.SessionDir, "session.json"),
		},
		UpdateHandler: handler,
		Middlewares: []telegram.Middleware{
			waiter,
			ratelimit.New(rate.Every(time.Millisecond*100), 5),
		},
	})

	return client, waiter, lg, nil
}
