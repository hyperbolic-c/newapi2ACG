package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

// snapshotPass reads every dataset from one new consistent snapshot in
// primary-key batches and pushes each batch through the pipeline. It is used
// by the first full sync and by reconciliation. A failed batch is retried
// inside the pipeline without advancing the cursor; if the snapshot itself
// breaks, the whole pass fails and is redone on a new snapshot, never
// resumed from an old cursor (design-core §4.4).
func (c *Coordinator) snapshotPass(ctx context.Context, lease state.Lease, runID string, mode compareMode, batchSize int) (event.SourcePosition, error) {
	sess, err := c.Source.OpenSnapshot(ctx)
	if err != nil {
		return event.SourcePosition{}, err
	}
	defer func() { _ = sess.Close() }()
	pos := sess.Position()
	started := time.Now()
	total := 0
	c.Log.Info("snapshot pass started", "run", runID, "position", pos.String())
	for _, dataset := range c.Datasets {
		cursor := event.Cursor{}
		for {
			records, next, err := sess.ReadBatch(ctx, dataset, cursor, batchSize)
			if err != nil {
				return event.SourcePosition{}, fmt.Errorf("snapshot %s: %w", dataset, err)
			}
			if len(records) == 0 {
				break
			}
			batch := event.ChangeBatch{Events: make([]event.ChangeEvent, len(records))}
			for i := range records {
				batch.Events[i] = event.ChangeEvent{SourceID: c.SourceID, Operation: event.OpSnapshot, After: &records[i], Position: pos}
			}
			if err := c.Pipeline.process(ctx, lease, batch, batchOptions{mode: mode, runID: runID}); err != nil {
				return event.SourcePosition{}, fmt.Errorf("snapshot %s: %w", dataset, err)
			}
			total += len(records)
			cursor = next
			if len(records) < batchSize {
				break
			}
		}
	}
	if err := c.handleUnseen(ctx, lease, runID); err != nil {
		return event.SourcePosition{}, err
	}
	c.Log.Info("snapshot pass finished", "run", runID, "records", total, "duration", time.Since(started).Round(time.Millisecond).String())
	return pos, nil
}

// handleUnseen deals with resources whose source rows were not seen in the
// completed pass: their rows were deleted. Only entries of configured
// datasets that this task owns are considered (design-core §6.4).
func (c *Coordinator) handleUnseen(ctx context.Context, lease state.Lease, runID string) error {
	var entries []state.MapEntry
	if err := c.Pipeline.Retry.Do(ctx, "list unseen resources", func() (err error) {
		entries, err = c.Store.Unseen(ctx, c.TaskID, runID, c.Datasets)
		return err
	}); err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	c.Pipeline.Metrics.OrphanedResources.Store(int64(len(entries)))
	if c.Reconcile.DeletePolicy != "delete" {
		sample := make([]string, 0, 10)
		for _, e := range entries[:min(10, len(entries))] {
			sample = append(sample, e.Identity.String())
		}
		c.Log.Warn("target resources whose source rows no longer exist were kept (delete_policy=report)",
			"count", len(entries), "sample", sample)
		return nil
	}
	ops := make([]target.TargetOperation, len(entries))
	for i, e := range entries {
		ops[i] = target.TargetOperation{Identity: e.Identity, Action: target.ActionDelete, Source: e.Source}
	}
	c.Log.Info("deleting target resources whose source rows no longer exist", "count", len(ops))
	if err := c.Pipeline.applyOps(ctx, lease, ops, batchOptions{mode: compareStored}); err != nil {
		return err
	}
	c.Pipeline.Metrics.OrphanedResources.Store(0)
	return nil
}

// runFull performs the first full sync and records its snapshot position as
// the starting point for incremental sync.
func (c *Coordinator) runFull(ctx context.Context, lease state.Lease) (state.Phase, error) {
	runID := c.newRunID("full")
	pos, err := c.snapshotPass(ctx, lease, runID, compareStored, c.Full.BatchSize)
	if err != nil {
		return state.Snapshotting, err
	}
	cp := state.Checkpoint{SnapshotPosition: pos, AppliedPosition: pos, SnapshotDone: true, ReconcileRunID: runID}
	if err := c.commitCheckpoint(ctx, lease, cp); err != nil {
		return state.Snapshotting, err
	}
	c.Log.Info("full sync complete", "position", pos.String())
	return state.CatchingUp, nil
}
