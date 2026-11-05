// Package sync drives full, incremental and periodic synchronization through
// one pipeline (design-core §2, §7): collectors produce change batches, the
// pipeline maps and writes them, and checkpoints advance only after success.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/observability"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

// Retry retries transient failures with exponential backoff. Permanent
// target errors, lost leases and cancellation are returned immediately.
type Retry struct {
	Attempts int
	Base     time.Duration
	Max      time.Duration
	Log      *slog.Logger
	Metrics  *observability.Metrics
}

func (r Retry) Do(ctx context.Context, what string, fn func() error) error {
	delay := r.Base
	if delay <= 0 {
		delay = time.Second
	}
	maxDelay := r.Max
	if maxDelay < delay {
		maxDelay = delay
	}
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || !retryable(err) || ctx.Err() != nil {
			return err
		}
		if attempt >= r.Attempts {
			return fmt.Errorf("%s: giving up after %d retries: %w", what, r.Attempts, err)
		}
		if r.Metrics != nil {
			r.Metrics.Retries.Add(1)
		}
		if r.Log != nil {
			r.Log.Warn("retrying", "what", what, "attempt", attempt+1, "delay", delay.String(), "error", err.Error())
		}
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, maxDelay)
	}
}

func retryable(err error) bool {
	return !target.IsPermanent(err) && !errors.Is(err, state.ErrLeaseLost) &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
