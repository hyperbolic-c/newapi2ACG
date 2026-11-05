package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
)

func (s *Store) LoadCheckpoint(ctx context.Context, id string) (state.Checkpoint, error) {
	var cp state.Checkpoint
	var snap, applied []byte
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_position,applied_position,snapshot_done,reconcile_run_id
FROM `+s.table("migration_checkpoints")+` WHERE task_id=$1`, id).Scan(&snap, &applied, &cp.SnapshotDone, &cp.ReconcileRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return state.Checkpoint{}, nil
	}
	if err != nil {
		return state.Checkpoint{}, errors.New("load checkpoint failed")
	}
	if err := decodePosition(snap, &cp.SnapshotPosition); err != nil {
		return state.Checkpoint{}, err
	}
	if err := decodePosition(applied, &cp.AppliedPosition); err != nil {
		return state.Checkpoint{}, err
	}
	return cp, nil
}

func decodePosition(raw []byte, p *event.SourcePosition) error {
	if len(raw) == 0 || string(raw) == "{}" {
		*p = event.SourcePosition{}
		return nil
	}
	if err := json.Unmarshal(raw, p); err != nil {
		return errors.New("persisted position is invalid")
	}
	return nil
}

// Commit writes resource map changes and the checkpoint in one transaction.
// The task row is locked FOR UPDATE while the lease is verified, so another
// instance cannot take the lease over until this transaction ends.
func (s *Store) Commit(ctx context.Context, l state.Lease, c state.Commit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin state transaction failed")
	}
	defer func() { _ = tx.Rollback() }() // no-op after commit
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM `+s.table("migration_tasks")+`
WHERE task_id=$1 AND lease_owner=$2 AND lease_until > now() FOR UPDATE`, l.TaskID, l.Owner).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return state.ErrLeaseLost
	}
	if err != nil {
		return errors.New("verify task lease failed")
	}
	if err := s.writeEntries(ctx, tx, l.TaskID, c); err != nil {
		return err
	}
	if cp := c.Checkpoint; cp != nil {
		snap, _ := json.Marshal(cp.SnapshotPosition)
		applied, _ := json.Marshal(cp.AppliedPosition)
		if cp.SnapshotPosition.IsZero() {
			snap = []byte("{}")
		}
		if cp.AppliedPosition.IsZero() {
			applied = []byte("{}")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO `+s.table("migration_checkpoints")+`(task_id,snapshot_position,applied_position,snapshot_done,reconcile_run_id,updated_at)
VALUES($1,$2::jsonb,$3::jsonb,$4,$5,now())
ON CONFLICT (task_id) DO UPDATE SET snapshot_position=EXCLUDED.snapshot_position, applied_position=EXCLUDED.applied_position,
 snapshot_done=EXCLUDED.snapshot_done, reconcile_run_id=EXCLUDED.reconcile_run_id, updated_at=now()`,
			l.TaskID, string(snap), string(applied), cp.SnapshotDone, cp.ReconcileRunID)
		if err != nil {
			return errors.New("write checkpoint failed")
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("commit state failed; outcome unknown")
	}
	return nil
}
