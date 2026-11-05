package postgres

import (
	"context"
	"errors"
)

// ResetCheckpoint clears the checkpoint so the next start performs a new
// full sync. Operator use only, while the task is paused or failed.
func (s *Store) ResetCheckpoint(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE `+s.table("migration_checkpoints")+`
SET snapshot_position='{}', applied_position='{}', snapshot_done=false, reconcile_run_id='', updated_at=now() WHERE task_id=$1`, taskID)
	if err != nil {
		return errors.New("reset checkpoint failed")
	}
	return nil
}
