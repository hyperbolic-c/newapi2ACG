package sync

import (
	"context"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
)

// streamOnce runs one binlog subscription from cp.AppliedPosition. readCtx
// stops reading (shutdown, pause or a due reconciliation); workCtx governs
// the processing of a batch already received, so stopping never interrupts a
// half-applied transaction. Each relevant transaction is applied and its end
// position committed in the same state commit. Transactions without relevant
// events only move the position; those are committed at most every
// IdleFlush to avoid one state write per unrelated transaction, except while
// eager reports true (catching up needs every position to detect completion).
func (c *Coordinator) streamOnce(readCtx, workCtx context.Context, lease state.Lease, cp *state.Checkpoint, committed func(event.SourcePosition), eager func() bool) error {
	var pending *event.SourcePosition
	lastFlush := time.Now()
	flush := func(ctx context.Context) error {
		if pending == nil {
			return nil
		}
		next := *cp
		next.AppliedPosition = *pending
		if err := c.commitCheckpoint(ctx, lease, next); err != nil {
			return err
		}
		*cp, pending, lastFlush = next, nil, time.Now()
		committed(cp.AppliedPosition)
		return nil
	}
	err := c.Streamer.Stream(readCtx, cp.AppliedPosition, func(b event.ChangeBatch) error {
		if len(b.Events) == 0 {
			p := b.EndPosition
			pending = &p
			if eager() || time.Since(lastFlush) >= c.idleFlush() {
				return flush(workCtx)
			}
			return nil
		}
		next := *cp
		next.AppliedPosition = b.EndPosition
		if err := c.Pipeline.process(workCtx, lease, b, batchOptions{mode: compareStored, checkpoint: &next}); err != nil {
			return err
		}
		*cp, pending, lastFlush = next, nil, time.Now()
		committed(cp.AppliedPosition)
		return nil
	})
	// Persist the position of trailing unrelated transactions.
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(workCtx), 10*time.Second)
	defer cancel()
	if ferr := flush(flushCtx); ferr != nil && err == nil {
		err = ferr
	}
	return err
}
