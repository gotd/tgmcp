package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// invokerFunc adapts a function to [tg.Invoker].
type invokerFunc func(ctx context.Context, input bin.Encoder, output bin.Decoder) error

func (f invokerFunc) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return f(ctx, input, output)
}

func floodWait(seconds int) error {
	return tgerr.New(420, "FLOOD_WAIT_"+strconv.Itoa(seconds))
}

// testWaiter returns a waiter whose waits return immediately, recording the
// durations it was asked to wait for.
func testWaiter(t *testing.T) (*floodWaiter, *[]time.Duration) {
	t.Helper()

	var slept []time.Duration
	w := newFloodWaiter()
	w.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)

		return nil
	}

	return w, &slept
}

func TestFloodWaiterPassesThrough(t *testing.T) {
	w, _ := testWaiter(t)

	var calls int
	h := w.Handle(invokerFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++

		return nil
	}))

	if err := h(t.Context(), nil, nil); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestFloodWaiterRetries(t *testing.T) {
	w, _ := testWaiter(t)

	var calls int
	h := w.Handle(invokerFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		if calls < 3 {
			return floodWait(1)
		}

		return nil
	}))

	if err := h(t.Context(), nil, nil); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

// TestFloodWaiterGivesUp checks that a call cannot be retried forever.
func TestFloodWaiterGivesUp(t *testing.T) {
	w, _ := testWaiter(t)
	w.maxRetries = 2

	var calls int
	h := w.Handle(invokerFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++

		return floodWait(1)
	}))

	if err := h(t.Context(), nil, nil); err == nil {
		t.Fatal("want error, got nil")
	}
	// The initial call plus maxRetries retries.
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

// TestFloodWaiterRefusesLongWait checks that an implausible delay is returned
// rather than slept through.
func TestFloodWaiterRefusesLongWait(t *testing.T) {
	w, _ := testWaiter(t)
	w.maxWait = 10 * time.Second

	var calls int
	h := w.Handle(invokerFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++

		return floodWait(3600)
	}))

	if err := h(t.Context(), nil, nil); err == nil {
		t.Fatal("want error, got nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1: the wait should not be retried", calls)
	}
}

func TestFloodWaiterHonorsContext(t *testing.T) {
	w := newFloodWaiter()
	// Real waits here: the sleep must be cut short by the context.
	w.maxWait = time.Hour

	ctx, cancel := context.WithCancel(t.Context())
	h := w.Handle(invokerFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		cancel()

		return floodWait(3000)
	}))

	if err := h(ctx, nil, nil); err == nil {
		t.Fatal("want error, got nil")
	}
}

// TestFloodWaiterIsReentrant is the regression test for the deadlock this
// waiter exists to avoid: gotd handles a datacenter migration by invoking
// auth.exportAuthorization through the middleware chain from inside an
// invocation that is already in it. A waiter that queues work through a single
// goroutine never returns from this.
//
// See https://github.com/gotd/td/issues/1842.
func TestFloodWaiterIsReentrant(t *testing.T) {
	w, _ := testWaiter(t)

	var (
		h      func(context.Context, bin.Encoder, bin.Decoder) error
		nested bool
	)
	h = w.Handle(invokerFunc(func(ctx context.Context, _ bin.Encoder, _ bin.Decoder) error {
		if nested {
			return nil
		}
		nested = true

		// Same middleware, called from within itself.
		return h(ctx, nil, nil)
	}))

	done := make(chan error, 1)
	go func() { done <- h(t.Context(), nil, nil) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested invoke: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested invoke deadlocked")
	}
	if !nested {
		t.Error("nested invoke did not run")
	}
}

func TestFloodWaiterCallback(t *testing.T) {
	var waits []time.Duration
	w, _ := testWaiter(t)
	w = w.withCallback(func(_ context.Context, d time.Duration) {
		waits = append(waits, d)
	})

	var calls int
	h := w.Handle(invokerFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		if calls == 1 {
			return floodWait(7)
		}

		return nil
	}))

	if err := h(t.Context(), nil, nil); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(waits) != 1 || waits[0] != 7*time.Second {
		t.Errorf("waits = %v, want [7s]", waits)
	}
}

var _ tg.Invoker = invokerFunc(nil)
