package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/bin"
	gotdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const (
	DefaultFloodWaitLimit = 30 * time.Second
	MaxFloodWaitLimit     = 5 * time.Minute
)

type floodSleeper func(context.Context, time.Duration) error

func ValidateFloodWaitLimit(limit time.Duration) error {
	if limit < 0 {
		return fmt.Errorf("--wait must not be negative")
	}
	if limit > MaxFloodWaitLimit {
		return fmt.Errorf("--wait must be at most %s", MaxFloodWaitLimit)
	}
	return nil
}

func floodWaitMiddleware(limit time.Duration) gotdtelegram.Middleware {
	return newFloodWaitMiddleware(limit, sleepContext)
}

func newFloodWaitMiddleware(limit time.Duration, sleep floodSleeper) gotdtelegram.Middleware {
	return gotdtelegram.MiddlewareFunc(func(next tg.Invoker) gotdtelegram.InvokeFunc {
		return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
			remaining := limit
			for {
				err := next.Invoke(ctx, input, output)
				wait, flood := tgerr.AsFloodWait(err)
				if !flood || remaining <= 0 {
					return err
				}
				wait += time.Second
				if wait > remaining {
					return err
				}
				if err := sleep(ctx, wait); err != nil {
					return err
				}
				remaining -= wait
			}
		}
	})
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
