package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"
)

func TestValidateFloodWaitLimit(t *testing.T) {
	for _, limit := range []time.Duration{0, DefaultFloodWaitLimit, MaxFloodWaitLimit} {
		if err := ValidateFloodWaitLimit(limit); err != nil {
			t.Fatalf("ValidateFloodWaitLimit(%s): %v", limit, err)
		}
	}
	for _, limit := range []time.Duration{-time.Second, MaxFloodWaitLimit + time.Second} {
		if err := ValidateFloodWaitLimit(limit); err == nil {
			t.Fatalf("ValidateFloodWaitLimit(%s) succeeded", limit)
		}
	}
}

func TestFloodWaitMiddlewareRetriesWithinTotalBudget(t *testing.T) {
	var calls int
	next := gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		if calls < 3 {
			return tgerr.New(420, "FLOOD_WAIT_2")
		}
		return nil
	})
	var waits []time.Duration
	middleware := newFloodWaitMiddleware(7*time.Second, func(_ context.Context, wait time.Duration) error {
		waits = append(waits, wait)
		return nil
	})
	if err := middleware.Handle(next).Invoke(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(waits) != 2 || waits[0] != 3*time.Second || waits[1] != 3*time.Second {
		t.Fatalf("calls=%d waits=%v", calls, waits)
	}
}

func TestFloodWaitMiddlewareReturnsOriginalErrorOutsideBudget(t *testing.T) {
	flood := tgerr.New(420, "FLOOD_WAIT_5")
	var calls int
	next := gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		return flood
	})
	middleware := newFloodWaitMiddleware(5*time.Second, func(context.Context, time.Duration) error {
		t.Fatal("sleep called outside budget")
		return nil
	})
	err := middleware.Handle(next).Invoke(context.Background(), nil, nil)
	if err != flood || calls != 1 {
		t.Fatalf("err=%v calls=%d, want original flood error and one call", err, calls)
	}
}

func TestFloodWaitMiddlewareStopsWhenRepeatedWaitsExhaustBudget(t *testing.T) {
	flood := tgerr.New(420, "FLOOD_WAIT_2")
	var calls, sleeps int
	next := gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		return flood
	})
	middleware := newFloodWaitMiddleware(5*time.Second, func(context.Context, time.Duration) error {
		sleeps++
		return nil
	})
	err := middleware.Handle(next).Invoke(context.Background(), nil, nil)
	if err != flood || calls != 2 || sleeps != 1 {
		t.Fatalf("err=%v calls=%d sleeps=%d", err, calls, sleeps)
	}
}

func TestFloodWaitMiddlewareHonorsCancellation(t *testing.T) {
	flood := tgerr.New(420, "FLOOD_WAIT_1")
	next := gotdtelegram.InvokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return flood })
	want := errors.New("wait canceled")
	middleware := newFloodWaitMiddleware(5*time.Second, func(context.Context, time.Duration) error { return want })
	if err := middleware.Handle(next).Invoke(context.Background(), nil, nil); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
