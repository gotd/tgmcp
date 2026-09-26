package main

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The bot sharing a process with the MCP server only works if a bot that cannot
// start keeps failing on its own. Run inside a synctest bubble, so the backoff
// is waited out in virtual time rather than real seconds.
func TestSuperviseRestartsAndBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const attempts = 4

		var (
			gaps  []time.Duration
			last  time.Time
			calls int
			done  = make(chan struct{})
		)

		go func() {
			defer close(done)
			supervise(ctx, zap.NewNop(), func(context.Context) error {
				if calls > 0 {
					gaps = append(gaps, time.Since(last))
				}
				calls++
				last = time.Now()
				if calls == attempts {
					cancel()
				}

				return errors.New("bot is down")
			})
		}()

		<-done

		require.Equal(t, attempts, calls)
		// Doubling from the shortest delay, since every run failed immediately.
		require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, gaps)
	})
}

// A run that stayed up is not a crash loop, so the delay after it starts over
// rather than inheriting a backoff earned long ago.
func TestSuperviseResetsAfterHealthyRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var (
			gaps  []time.Duration
			last  time.Time
			calls int
			done  = make(chan struct{})
		)

		go func() {
			defer close(done)
			supervise(ctx, zap.NewNop(), func(ctx context.Context) error {
				if calls > 0 {
					gaps = append(gaps, time.Since(last))
				}
				calls++

				// The third attempt stays up past the cap before failing.
				if calls == 3 {
					select {
					case <-ctx.Done():
					case <-time.After(2 * echoBotMaxRetry):
					}
				}
				last = time.Now()
				if calls == 4 {
					cancel()
				}

				return errors.New("bot is down")
			})
		}()

		<-done

		require.Len(t, gaps, 3)
		require.Equal(t, time.Second, gaps[0])
		require.Equal(t, 2*time.Second, gaps[1])
		require.Equal(t, time.Second, gaps[2], "backoff did not reset after a healthy run")
	})
}

// Cancelling must stop the supervisor rather than restart into a doomed run,
// and must not report the cancellation as a failure.
func TestSuperviseStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())

		calls := 0
		done := make(chan struct{})

		go func() {
			defer close(done)
			supervise(ctx, zap.NewNop(), func(ctx context.Context) error {
				calls++
				<-ctx.Done()

				return ctx.Err()
			})
		}()

		synctest.Wait()
		cancel()
		<-done

		require.Equal(t, 1, calls)
	})
}
