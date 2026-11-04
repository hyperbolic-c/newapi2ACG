package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Repository is implemented by the PG state store. Save must atomically persist
// state; it must not reset checkpoints or queues.
type Repository interface {
	Load(context.Context, string) (JobState, error)
	Save(context.Context, string, JobState) error
}

// Lease cancellation is authoritative. The owner acquires/releases the lease;
// Runner only derives a work context from it and never extends its lifetime.
type Lease interface{ Context() context.Context }

// ReasonError lets a handler publish a fixed, non-sensitive summary as the
// persisted job reason. Only wrap text written by this codebase; never wrap
// driver, source-row or target-response text.
type ReasonError struct{ Reason string }

func (e *ReasonError) Error() string { return e.Reason }

// Handler must respect cancellation and return only after its child work stops.
// Streaming/Polling handlers typically run until pause, stop or cancellation.
type Handler func(context.Context) (Event, error)
type Runner struct {
	Store      Repository
	Handlers   map[State]Handler
	RetryDelay time.Duration
}

func (r Runner) Run(parent context.Context, job string, lease Lease) error {
	if r.Store == nil || lease == nil || lease.Context() == nil {
		return errors.New("runner requires repository and lease")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(lease.Context(), cancel)
	defer stop()
	if err := lease.Context().Err(); err != nil {
		return err
	}
	state, err := r.Store.Load(ctx, job)
	if err != nil {
		return err
	}
	retry := r.RetryDelay
	if retry <= 0 {
		retry = time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if state.State == Paused || state.State == Stopped {
			return nil
		}
		if !active(state.State) {
			return errors.New("invalid persisted job state")
		}
		handler, ok := r.Handlers[state.State]
		if !ok {
			return fmt.Errorf("no handler registered for state %s", state.State)
		}
		event, workErr := handler(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if workErr != nil {
			// Handler errors may contain connection strings or target request bodies.
			// Detailed diagnostics must be sanitized at their boundary, not persisted here.
			state.Reason = string(state.State) + " work failed"
			var safe *ReasonError
			if errors.As(workErr, &safe) && safe.Reason != "" && len(safe.Reason) <= MaxReasonLength {
				state.Reason = safe.Reason
			}
			if err := r.Store.Save(ctx, job, state); err != nil {
				return err
			}
			timer := time.NewTimer(retry)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		next, err := Transition(state, event)
		if err != nil {
			return err
		}
		if err := r.Store.Save(ctx, job, next); err != nil {
			return err
		}
		state = next
	}
}
