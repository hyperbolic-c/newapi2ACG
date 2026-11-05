package sync

import (
	"context"
	"errors"
	"log/slog"
	gosync "sync"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/source"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

// Coordinator runs one migration task: one active executor, checkpoints
// committed in order (design-core §7). Phases:
//
//	initializing → snapshotting → catching_up → streaming ⇄ reconciling
//	any → failed (needs an operator), streaming → paused (operator)
//
// On start, every phase except paused/failed restarts from initializing:
// an interrupted full sync is redone on a new snapshot, an interrupted
// reconciliation resumes incremental sync from the last checkpoint.
type Coordinator struct {
	TaskID, SourceID, ConfigHash, Owner string

	Source   source.Source
	Streamer source.Streamer // nil when the source has no change log
	Pipeline *Pipeline
	Store    state.Store
	Datasets []string

	Full        config.Full
	Incremental config.Incremental
	Reconcile   config.Reconcile
	LeaseTTL    time.Duration
	Log         *slog.Logger

	// Tunables; zero values select defaults (tests shorten them).
	ControlInterval time.Duration // how often operator commands are noticed (5s)
	IdleFlush       time.Duration // max delay before committing positions of unrelated transactions (1s)
	ErrorBackoff    time.Duration // first backoff after a transient phase failure (2s, doubling to 2m)
	Now             func() time.Time

	started          time.Time
	reconcileSoon    bool
	reconcileRetryAt time.Time
	failures         int
}

func (c *Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Coordinator) controlInterval() time.Duration {
	return orDefault(c.ControlInterval, 5*time.Second)
}
func (c *Coordinator) idleFlush() time.Duration { return orDefault(c.IdleFlush, time.Second) }

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run executes the task until ctx is canceled (returns nil) or the lease is
// lost (returns state.ErrLeaseLost).
func (c *Coordinator) Run(ctx context.Context) error {
	c.started = c.now()
	if _, err := c.Store.EnsureTask(ctx, c.TaskID, c.SourceID, c.ConfigHash); err != nil {
		return err
	}
	lease, err := c.acquire(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = c.Store.ReleaseLease(releaseCtx, lease)
	}()
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go c.keepLease(runCtx, cancel, lease)
	err = c.loop(runCtx, lease)
	if errors.Is(context.Cause(runCtx), state.ErrLeaseLost) || errors.Is(err, state.ErrLeaseLost) {
		c.Log.Error("task lease lost; stopping")
		return state.ErrLeaseLost
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (c *Coordinator) acquire(ctx context.Context) (state.Lease, error) {
	lease := state.Lease{TaskID: c.TaskID, Owner: c.Owner}
	waiting := false
	for {
		ok, err := c.Store.AcquireLease(ctx, c.TaskID, c.Owner, c.LeaseTTL)
		if ok {
			c.Log.Info("task lease acquired", "task", c.TaskID, "owner", c.Owner)
			return lease, nil
		}
		if err != nil {
			c.Log.Warn("acquire task lease failed", "error", err.Error())
		} else if !waiting {
			c.Log.Info("task lease held by another instance; waiting", "task", c.TaskID)
			waiting = true
		}
		if err := sleep(ctx, c.LeaseTTL/3); err != nil {
			return lease, err
		}
	}
}

// keepLease renews the lease; when renewal is impossible for most of the
// TTL, the run is canceled before another instance can take over.
func (c *Coordinator) keepLease(ctx context.Context, cancel context.CancelCauseFunc, lease state.Lease) {
	t := time.NewTicker(c.LeaseTTL / 3)
	defer t.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := c.Store.RenewLease(ctx, lease, c.LeaseTTL)
			if err == nil {
				lastOK = time.Now()
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, state.ErrLeaseLost) || time.Since(lastOK) > c.LeaseTTL*2/3 {
				cancel(state.ErrLeaseLost)
				return
			}
			c.Log.Warn("renew task lease failed; retrying", "error", err.Error())
		}
	}
}

func (c *Coordinator) loop(ctx context.Context, lease state.Lease) error {
	task, err := c.Store.LoadTask(ctx, c.TaskID)
	if err != nil {
		return err
	}
	if task.ConfigHash != c.ConfigHash {
		if err := c.Store.SetConfigHash(ctx, lease, c.ConfigHash); err != nil {
			return err
		}
		c.reconcileSoon = true // new rules: recompute target state (design-core §6.1)
		c.Log.Info("configuration changed; a reconciliation will run after catching up")
	}
	phase := task.Phase
	if phase != state.Paused && phase != state.Failed {
		phase = state.Initializing
		if err := c.setPhase(ctx, lease, phase, ""); err != nil {
			return err
		}
	} else {
		c.Log.Warn("task is waiting for an operator", "phase", string(phase), "last_error", task.LastError)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.Pipeline.Metrics.SetPhase(string(phase))
		next, err := c.step(ctx, lease, phase)
		lastErr := ""
		if err != nil {
			if next, lastErr, err = c.onError(ctx, lease, phase, err); err != nil {
				return err
			}
		} else {
			c.failures = 0
		}
		if next != phase || lastErr != "" {
			if err := c.setPhase(ctx, lease, next, lastErr); err != nil {
				return err
			}
		}
		phase = next
	}
}

func (c *Coordinator) step(ctx context.Context, lease state.Lease, phase state.Phase) (state.Phase, error) {
	switch phase {
	case state.Paused, state.Failed:
		return c.waitForOperator(ctx, phase)
	case state.Initializing:
		return c.initialize(ctx, lease)
	case state.Snapshotting:
		return c.runFull(ctx, lease)
	default: // catching_up, streaming, reconciling
		return c.runIncremental(ctx, lease, phase)
	}
}

func isFatal(err error) bool {
	return errors.Is(err, source.ErrIncompatibleDDL) || target.IsPermanent(err)
}

// onError chooses the next phase. Position loss starts a new full sync;
// incompatible DDL and permanent errors stop the task for an operator;
// anything else is retried with backoff in the same phase.
func (c *Coordinator) onError(ctx context.Context, lease state.Lease, phase state.Phase, err error) (state.Phase, string, error) {
	switch {
	case ctx.Err() != nil:
		return phase, "", ctx.Err()
	case errors.Is(err, state.ErrLeaseLost):
		return phase, "", err
	case errors.Is(err, source.ErrPositionLost):
		c.Log.Error("binlog position no longer available; starting a new full sync", "error", err.Error())
		if cerr := c.commitCheckpoint(ctx, lease, state.Checkpoint{}); cerr != nil {
			return phase, cerr.Error(), nil
		}
		return state.Snapshotting, err.Error(), nil
	case isFatal(err):
		c.Log.Error("task stopped; operator action required", "phase", string(phase), "error", err.Error())
		return state.Failed, err.Error(), nil
	}
	c.failures++
	delay := orDefault(c.ErrorBackoff, 2*time.Second)
	for i := 1; i < c.failures && delay < 2*time.Minute; i++ {
		delay *= 2
	}
	delay = min(delay, 2*time.Minute)
	c.Log.Warn("phase failed; retrying", "phase", string(phase), "attempt", c.failures, "delay", delay.String(), "error", err.Error())
	if serr := sleep(ctx, delay); serr != nil {
		return phase, "", serr
	}
	if phase == state.Streaming || phase == state.Reconciling {
		phase = state.CatchingUp // resume from the checkpoint, then catch up
	}
	return phase, err.Error(), nil
}

func (c *Coordinator) setPhase(ctx context.Context, lease state.Lease, phase state.Phase, lastErr string) error {
	err := c.Pipeline.Retry.Do(ctx, "record task phase", func() error { return c.Store.SetPhase(ctx, lease, phase, lastErr) })
	if err == nil {
		c.Pipeline.Metrics.SetPhase(string(phase))
		c.Log.Info("task phase", "phase", string(phase))
	}
	return err
}

func (c *Coordinator) commitCheckpoint(ctx context.Context, lease state.Lease, cp state.Checkpoint) error {
	err := c.Pipeline.Retry.Do(ctx, "commit checkpoint", func() error {
		return c.Store.Commit(ctx, lease, state.Commit{Checkpoint: &cp})
	})
	if err == nil {
		c.Pipeline.Metrics.SetPosition(cp.AppliedPosition.String())
	}
	return err
}

func (c *Coordinator) initialize(ctx context.Context, lease state.Lease) (state.Phase, error) {
	if err := c.Source.Check(ctx); err != nil {
		return state.Initializing, err
	}
	cp, err := c.Store.LoadCheckpoint(ctx, c.TaskID)
	if err != nil {
		return state.Initializing, err
	}
	if cp.SnapshotDone && !cp.AppliedPosition.IsZero() {
		return state.CatchingUp, nil
	}
	if c.Full.Enabled && c.Full.OnFirstRun {
		return state.Snapshotting, nil
	}
	if c.Streamer == nil {
		return state.Initializing, target.Permanent("full sync is disabled and the source has no change log")
	}
	pos, err := c.Streamer.CurrentPosition(ctx)
	if err != nil {
		return state.Initializing, err
	}
	c.Log.Warn("initial full sync disabled; starting incremental sync from the current position", "position", pos.String())
	if err := c.commitCheckpoint(ctx, lease, state.Checkpoint{SnapshotPosition: pos, AppliedPosition: pos, SnapshotDone: true}); err != nil {
		return state.Initializing, err
	}
	return state.CatchingUp, nil
}

// waitForOperator idles until an operator changes the phase (resume or
// recover set it to initializing).
func (c *Coordinator) waitForOperator(ctx context.Context, phase state.Phase) (state.Phase, error) {
	t := time.NewTicker(c.controlInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return phase, ctx.Err()
		case <-t.C:
			task, err := c.Store.LoadTask(ctx, c.TaskID)
			if err == nil && task.Phase != phase {
				c.Log.Info("operator changed the task phase", "from", string(phase), "to", string(task.Phase))
				return state.Initializing, nil
			}
		}
	}
}

// runIncremental streams the binlog from the checkpoint, catching up first,
// until shutdown, an operator pause, an error, or a due reconciliation.
func (c *Coordinator) runIncremental(ctx context.Context, lease state.Lease, phase state.Phase) (state.Phase, error) {
	cp, err := c.Store.LoadCheckpoint(ctx, c.TaskID)
	if err != nil {
		return phase, err
	}
	if !cp.SnapshotDone || cp.AppliedPosition.IsZero() {
		return state.Initializing, nil
	}
	due, hasDue := c.nextReconcile(cp)
	if c.Streamer == nil || !c.Incremental.Enabled {
		return c.pollOnly(ctx, lease, phase, due, hasDue)
	}
	current := phase
	if current == state.Reconciling {
		current = state.CatchingUp
	}
	var catchTarget event.SourcePosition
	if current == state.CatchingUp {
		if catchTarget, err = c.Streamer.CurrentPosition(ctx); err != nil {
			return current, err
		}
		if ok, _ := c.Streamer.Covers(cp.AppliedPosition, catchTarget); ok {
			if err := c.setPhase(ctx, lease, state.Streaming, ""); err != nil {
				return current, err
			}
			current = state.Streaming
		}
	}
	readCtx, stop := context.WithCancel(ctx)
	defer stop()
	var (
		mu       gosync.Mutex
		reason   string
		external state.Phase
		wg       gosync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		var timer <-chan time.Time
		if hasDue {
			t := time.NewTimer(max(0, due.Sub(c.now())))
			defer t.Stop()
			timer = t.C
		}
		ticker := time.NewTicker(c.controlInterval())
		defer ticker.Stop()
		for {
			select {
			case <-readCtx.Done():
				return
			case <-timer:
				mu.Lock()
				reason = "reconcile"
				mu.Unlock()
				stop()
				return
			case <-ticker.C:
				if task, err := c.Store.LoadTask(readCtx, c.TaskID); err == nil && task.Phase == state.Paused {
					mu.Lock()
					external = state.Paused
					mu.Unlock()
					stop()
					return
				}
			}
		}
	}()
	committed := func(pos event.SourcePosition) {
		if current != state.CatchingUp {
			return
		}
		if ok, err := c.Streamer.Covers(pos, catchTarget); err == nil && ok {
			if err := c.setPhase(ctx, lease, state.Streaming, ""); err == nil {
				current = state.Streaming
				c.Log.Info("caught up with the source", "position", pos.String())
			}
		}
	}
	err = c.streamOnce(readCtx, ctx, lease, &cp, committed, func() bool { return current == state.CatchingUp })
	stop()
	wg.Wait()
	mu.Lock()
	why, ext := reason, external
	mu.Unlock()
	if ctx.Err() != nil {
		return current, ctx.Err()
	}
	if ext != "" {
		c.Log.Info("task paused by an operator")
		return ext, nil
	}
	if why == "reconcile" && (err == nil || errors.Is(err, context.Canceled)) {
		if err := c.setPhase(ctx, lease, state.Reconciling, ""); err != nil {
			return current, err
		}
		return c.reconcile(ctx, lease)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return current, err
	}
	return current, nil
}

// pollOnly is used when the source has no change log or incremental sync
// is disabled: periodic reconciliation is the only way changes arrive.
func (c *Coordinator) pollOnly(ctx context.Context, lease state.Lease, phase state.Phase, due time.Time, hasDue bool) (state.Phase, error) {
	if phase != state.Streaming {
		if err := c.setPhase(ctx, lease, state.Streaming, ""); err != nil {
			return phase, err
		}
	}
	ticker := time.NewTicker(c.controlInterval())
	defer ticker.Stop()
	var timer <-chan time.Time
	if hasDue {
		t := time.NewTimer(max(0, due.Sub(c.now())))
		defer t.Stop()
		timer = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return state.Streaming, ctx.Err()
		case <-ticker.C:
			if task, err := c.Store.LoadTask(ctx, c.TaskID); err == nil && task.Phase == state.Paused {
				return state.Paused, nil
			}
		case <-timer:
			if err := c.setPhase(ctx, lease, state.Reconciling, ""); err != nil {
				return state.Streaming, err
			}
			next, err := c.reconcile(ctx, lease)
			if next == state.CatchingUp {
				next = state.Streaming
			}
			return next, err
		}
	}
}
