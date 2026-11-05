package sync

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

// reconcile runs while incremental application is paused (design-core
// §6.3): a new snapshot P1 is compared with the target's current state,
// differences are written, and the checkpoint jumps to P1. Changes between
// the old checkpoint and P1 are already reflected in the snapshot; rows
// deleted in that window are found by unseen-entry detection. On failure the
// old checkpoint is kept and incremental sync resumes from it.
func (c *Coordinator) reconcile(ctx context.Context, lease state.Lease) (state.Phase, error) {
	runID := c.newRunID("reconcile")
	c.Pipeline.Metrics.ReconcileRuns.Add(1)
	pos, err := c.snapshotPass(ctx, lease, runID, compareTarget, c.Reconcile.BatchSize)
	if err != nil {
		if target.IsPermanent(err) || ctx.Err() != nil || isFatal(err) {
			return state.Reconciling, err
		}
		c.reconcileRetryAt = c.now().Add(min(c.Reconcile.Interval.D(), 10*time.Minute))
		c.Log.Error("reconciliation failed; resuming incremental sync from the previous checkpoint",
			"error", err.Error(), "retry_at", c.reconcileRetryAt.Format(time.RFC3339))
		return state.CatchingUp, nil
	}
	cp, err := c.Store.LoadCheckpoint(ctx, c.TaskID)
	if err != nil {
		return state.Reconciling, err
	}
	cp.AppliedPosition, cp.ReconcileRunID, cp.SnapshotDone = pos, runID, true
	if err := c.commitCheckpoint(ctx, lease, cp); err != nil {
		return state.Reconciling, err
	}
	c.reconcileSoon, c.reconcileRetryAt = false, time.Time{}
	c.Log.Info("reconciliation complete", "position", pos.String())
	return state.CatchingUp, nil
}

// nextReconcile returns when the next reconciliation is due, based on the
// last completed snapshot pass recorded in the checkpoint.
func (c *Coordinator) nextReconcile(cp state.Checkpoint) (time.Time, bool) {
	if !c.Reconcile.Enabled {
		return time.Time{}, false
	}
	if c.reconcileSoon {
		return c.now(), true
	}
	if !c.reconcileRetryAt.IsZero() {
		return c.reconcileRetryAt, true
	}
	last, ok := runTime(cp.ReconcileRunID)
	if !ok {
		last = c.started
	}
	return last.Add(c.Reconcile.Interval.D()), true
}

func (c *Coordinator) newRunID(kind string) string {
	return kind + "-" + strconv.FormatInt(c.now().UnixMilli(), 10)
}

func runTime(runID string) (time.Time, bool) {
	i := strings.LastIndexByte(runID, '-')
	if i < 0 {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(runID[i+1:], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}
