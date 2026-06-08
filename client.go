package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-faster/errors"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/time/rate"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// newLogger builds the JSON logger written to stderr so that journald (or any
// supervisor) captures it.
func newLogger(cfg Config) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(cfg.LogLevel)
	if err != nil {
		return nil, errors.Wrapf(err, "parse log level %q", cfg.LogLevel)
	}

	logCfg := zap.NewProductionConfig()
	logCfg.Level = zap.NewAtomicLevelAt(level)
	logCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	lg, err := logCfg.Build()
	if err != nil {
		return nil, errors.Wrap(err, "build logger")
	}

	return lg, nil
}

// newClient builds a Telegram client using the given configuration, update
// handler, and logger.
//
// handler is optional: when non-nil it is used as the update handler so that
// callers can wire in a dispatcher or the updates manager. Pass nil for the
// default no-op handler.
//
// The returned waiter must wrap client.Run:
//
//	return waiter.Run(ctx, func(ctx context.Context) error {
//	    return client.Run(ctx, handler)
//	})
func newClient(cfg Config, handler telegram.UpdateHandler, lg *zap.Logger) (*telegram.Client, *floodwait.Waiter, error) {
	if err := os.MkdirAll(cfg.SessionDir, 0o700); err != nil {
		return nil, nil, errors.Wrap(err, "create session dir")
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
			invokeLogger(lg),
			waiter,
			ratelimit.New(rate.Every(time.Millisecond*100), 5),
		},
	})

	return client, waiter, nil
}

// invokeLogger is a Telegram middleware that logs every MTProto RPC call at
// debug level, including the request type, duration, and any error.
func invokeLogger(lg *zap.Logger) telegram.Middleware {
	return telegram.MiddlewareFunc(func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			start := time.Now()
			err := next.Invoke(ctx, input, output)

			lg.Debug("MTProto call",
				zap.String("method", fmt.Sprintf("%T", input)),
				zap.Duration("took", time.Since(start)),
				zap.Error(err),
			)

			return err
		}
	})
}
