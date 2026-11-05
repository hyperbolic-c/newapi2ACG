package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/state"
)

// Leases use the database clock (now()), so instances need not agree on time.

func (s *Store) AcquireLease(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE `+s.table("migration_tasks")+`
SET lease_owner=$2, lease_until=now() + make_interval(secs => $3), updated_at=now()
WHERE task_id=$1 AND (lease_owner IS NULL OR lease_owner=$2 OR lease_until <= now())`, id, owner, ttl.Seconds())
	if err != nil {
		return false, errors.New("acquire task lease failed")
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, errors.New("acquire task lease failed")
	}
	if n == 0 {
		if _, err := s.LoadTask(ctx, id); errors.Is(err, state.ErrTaskNotFound) {
			return false, err
		}
	}
	return n == 1, nil
}

func (s *Store) RenewLease(ctx context.Context, l state.Lease, ttl time.Duration) error {
	return s.fenced(ctx, l, "lease_until=now() + make_interval(secs => $3)", ttl.Seconds())
}

func (s *Store) ReleaseLease(ctx context.Context, l state.Lease) error {
	_, err := s.db.ExecContext(ctx, `UPDATE `+s.table("migration_tasks")+` SET lease_owner=NULL, lease_until=NULL, updated_at=now()
WHERE task_id=$1 AND lease_owner=$2`, l.TaskID, l.Owner)
	if err != nil {
		return errors.New("release task lease failed")
	}
	return nil
}
