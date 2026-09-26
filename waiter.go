package main

import (
	"context"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Defaults for [floodWaiter].
const (
	defaultFloodRetries = 5
	defaultMaxFloodWait = 5 * time.Minute
)

// floodWaiter retries FLOOD_WAIT errors, waiting out the delay Telegram asks
// for.
//
// It retries in the calling goroutine and holds no queue, which is what
// separates it from [floodwait.Waiter]. gotd handles a datacenter migration
// inside an invocation by issuing auth.exportAuthorization through the same
// middleware chain, so a middleware that funnels every invocation through a
// single worker deadlocks: the nested call waits for a worker that is blocked
// on the outer call, and every later request piles up behind it. Downloading
// any file hosted on another datacenter is enough to trigger it.
//
// See https://github.com/gotd/td/issues/1842.
type floodWaiter struct {
	// maxRetries bounds how many times one call may be retried.
	maxRetries int
	// maxWait gives up instead of sleeping when Telegram asks for longer than
	// this, so a call cannot hang for an unbounded time.
	maxWait time.Duration
	// sleep is swapped in tests so they do not wait in real time.
	sleep func(ctx context.Context, d time.Duration) error
	// onWait, when set, is called before each wait.
	onWait func(ctx context.Context, d time.Duration)
}

func newFloodWaiter() *floodWaiter {
	return &floodWaiter{
		maxRetries: defaultFloodRetries,
		maxWait:    defaultMaxFloodWait,
		sleep:      sleepCtx,
	}
}

// withCallback reports every wait to fn.
func (w *floodWaiter) withCallback(fn func(ctx context.Context, d time.Duration)) *floodWaiter {
	w.onWait = fn

	return w
}

// Handle implements [telegram.Middleware].
func (w *floodWaiter) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		for attempt := 0; ; attempt++ {
			err := next.Invoke(ctx, input, output)

			d, ok := tgerr.AsFloodWait(err)
			if !ok {
				return err
			}
			if attempt >= w.maxRetries || (w.maxWait > 0 && d > w.maxWait) {
				return err
			}
			if w.onWait != nil {
				w.onWait(ctx, d)
			}
			if err := w.sleep(ctx, d); err != nil {
				return err
			}
		}
	}
}

// sleepCtx waits for d, or returns early if ctx is done.
//
// Telegram counts the delay from when it rejected the call, so waiting exactly
// d tends to race; a second of slack avoids an immediate second rejection.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d + time.Second)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
