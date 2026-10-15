package github

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// listenerFailure deliberately has no wrapped upstream error or response body.
// A completed polling iteration proves recovery; session creation alone does not.
type listenerFailure struct {
	category string
	healthy  bool
}

func (e listenerFailure) Error() string { return e.category }

// PollWithRetry reconnects a failed session, never replays an acquisition itself.
// Existing credentialed executions are unaffected when outstanding demand clears.
func PollWithRetry(ctx context.Context, values chan<- int, initial time.Duration, poll func(context.Context, chan<- int) error) error {
	return pollWithWait(ctx, values, initial, poll, func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}
func pollWithWait(ctx context.Context, values chan<- int, initial time.Duration, poll func(context.Context, chan<- int) error, wait func(context.Context, time.Duration) error) error {
	initial = max(time.Millisecond, min(30*time.Second, initial))
	delay := initial
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := poll(ctx, values)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var failure listenerFailure
		if errors.As(err, &failure) && failure.healthy {
			delay = initial
		}
		reason := FailureCategory(err)
		if reason == "" {
			reason = "listener-unavailable"
		}
		slog.Warn("GitHub demand listener unavailable; preserving existing jobs and retrying", "reason", reason, "retry_ms", delay.Milliseconds())
		select {
		case values <- 0:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
		delay = min(30*time.Second, delay*2)
	}
}
