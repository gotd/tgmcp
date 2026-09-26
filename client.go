package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/sdk/app"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/contrib/oteltg"
	"github.com/gotd/log/logzap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/time/rate"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// zapConfig builds the JSON logger configuration written to stderr so that
// journald (or any supervisor) captures it.
//
// [app.Run] builds the logger from it and additionally honors OTEL_LOG_LEVEL.
func zapConfig(cfg Config) (zap.Config, error) {
	level, err := zapcore.ParseLevel(cfg.LogLevel)
	if err != nil {
		return zap.Config{}, errors.Wrapf(err, "parse log level %q", cfg.LogLevel)
	}

	logCfg := zap.NewProductionConfig()
	logCfg.Level = zap.NewAtomicLevelAt(level)
	logCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return logCfg, nil
}

// newLogger builds a standalone logger for commands that do not run under
// [app.Run], such as "tgmcp auth".
func newLogger(cfg Config) (*zap.Logger, error) {
	logCfg, err := zapConfig(cfg)
	if err != nil {
		return nil, err
	}

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
// t is optional: when non-nil, every MTProto call is traced and measured. Pass
// nil for commands that run outside [app.Run], such as "tgmcp auth".
func newClient(cfg Config, handler telegram.UpdateHandler, lg *zap.Logger, t *app.Telemetry) (*telegram.Client, error) {
	if err := os.MkdirAll(cfg.SessionDir, 0o700); err != nil {
		return nil, errors.Wrap(err, "create session dir")
	}

	middlewares := []telegram.Middleware{invokeLogger(lg)}

	var metrics *tgMetrics
	if t != nil {
		otelMW, err := oteltg.New(t.MeterProvider(), t.TracerProvider())
		if err != nil {
			return nil, errors.Wrap(err, "otel middleware")
		}
		middlewares = append(middlewares, otelMW)

		m, err := newTGMetrics(t.MeterProvider().Meter(instrumentName))
		if err != nil {
			return nil, errors.Wrap(err, "init Telegram metrics")
		}
		metrics = &m
	}

	waiter := newFloodWaiter().withCallback(func(ctx context.Context, wait time.Duration) {
		if metrics != nil {
			metrics.FloodWaits.Add(ctx, 1)
		}
		lg.Warn("Flood wait", zap.Duration("wait", wait))
	})
	middlewares = append(middlewares,
		waiter,
		ratelimit.New(rate.Every(time.Millisecond*100), 5),
	)

	client := telegram.NewClient(cfg.AppID, cfg.AppHash, telegram.Options{
		Logger: logzap.New(lg),
		SessionStorage: &telegram.FileSessionStorage{
			Path: filepath.Join(cfg.SessionDir, "session.json"),
		},
		UpdateHandler: handler,
		Middlewares:   middlewares,
	})

	return client, nil
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
