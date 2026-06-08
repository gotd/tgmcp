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
	lj "gopkg.in/natefinch/lumberjack.v2"

	"github.com/gotd/td/telegram"
)

// newClient builds a Telegram client using the given configuration.
//
// Logs are written to a rotating file inside the session directory so that they
// never interfere with the MCP JSON-RPC stream on stdout.
func newClient(cfg Config) (*telegram.Client, *zap.Logger, error) {
	if err := os.MkdirAll(cfg.SessionDir, 0o700); err != nil {
		return nil, nil, errors.Wrap(err, "create session dir")
	}

	logWriter := zapcore.AddSync(&lj.Logger{
		Filename:   filepath.Join(cfg.SessionDir, "log.jsonl"),
		MaxBackups: 3,
		MaxSize:    1, // megabytes
		MaxAge:     7, // days
	})
	lg := zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		logWriter,
		zap.InfoLevel,
	))

	waiter := floodwait.NewWaiter().WithCallback(func(ctx context.Context, wait floodwait.FloodWait) {
		lg.Warn("Flood wait", zap.Duration("wait", wait.Duration))
	})

	client := telegram.NewClient(cfg.AppID, cfg.AppHash, telegram.Options{
		Logger: lg,
		SessionStorage: &telegram.FileSessionStorage{
			Path: filepath.Join(cfg.SessionDir, "session.json"),
		},
		Middlewares: []telegram.Middleware{
			waiter,
			ratelimit.New(rate.Every(time.Millisecond*100), 5),
		},
	})

	return client, lg, nil
}
