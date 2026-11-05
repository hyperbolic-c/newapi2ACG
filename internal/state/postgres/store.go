// Package postgres implements state.Store on PostgreSQL (the ACG instance,
// in its own schema). Driver errors are never returned verbatim because they
// can contain connection details; callers get fixed summaries.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq" // registers the "postgres" database/sql driver

	"github.com/hyperbolic-c/newapi2ACG/internal/state"
)

type Store struct {
	db     *sql.DB
	schema string
}

var _ state.Store = (*Store)(nil)

// New wraps an open database. Call Migrate first.
func New(db *sql.DB, schema string) (*Store, error) {
	if db == nil {
		return nil, errors.New("state store requires a database")
	}
	if err := validSchema(schema); err != nil {
		return nil, err
	}
	return &Store{db: db, schema: schema}, nil
}

// Open connects, verifies the connection and applies migrations.
func Open(ctx context.Context, dsn, schema string) (*Store, *sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, nil, errors.New("open state database failed")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, nil, errors.New("connect state database failed")
	}
	if err := Migrate(ctx, db, schema); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	s, err := New(db, schema)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return s, db, nil
}

func (s *Store) table(name string) string { return s.schema + "." + name }

func (s *Store) EnsureTask(ctx context.Context, id, source, hash string) (state.Task, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("migration_tasks")+`(task_id,source_id,phase,config_hash)
VALUES($1,$2,'initializing',$3) ON CONFLICT (task_id) DO NOTHING`, id, source, hash)
	if err != nil {
		return state.Task{}, errors.New("register task failed")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("migration_checkpoints")+`(task_id) VALUES($1) ON CONFLICT (task_id) DO NOTHING`, id); err != nil {
		return state.Task{}, errors.New("register task checkpoint failed")
	}
	return s.LoadTask(ctx, id)
}

func (s *Store) LoadTask(ctx context.Context, id string) (state.Task, error) {
	var t state.Task
	var phase string
	var owner sql.NullString
	var until sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT task_id,source_id,phase,config_hash,last_error,lease_owner,lease_until,updated_at
FROM `+s.table("migration_tasks")+` WHERE task_id=$1`, id).Scan(&t.ID, &t.SourceID, &phase, &t.ConfigHash, &t.LastError, &owner, &until, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return state.Task{}, state.ErrTaskNotFound
	}
	if err != nil {
		return state.Task{}, errors.New("load task failed")
	}
	t.Phase = state.Phase(phase)
	if !t.Phase.Valid() {
		return state.Task{}, fmt.Errorf("task %s has invalid persisted phase", id)
	}
	t.LeaseOwner, t.LeaseUntil = owner.String, until.Time
	return t, nil
}

// fenced runs an UPDATE on migration_tasks only while l holds the lease.
func (s *Store) fenced(ctx context.Context, l state.Lease, set string, args ...any) error {
	all := append([]any{l.TaskID, l.Owner}, args...)
	r, err := s.db.ExecContext(ctx, `UPDATE `+s.table("migration_tasks")+` SET `+set+`, updated_at=now()
WHERE task_id=$1 AND lease_owner=$2 AND lease_until > now()`, all...)
	if err != nil {
		return errors.New("update task failed")
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return state.ErrLeaseLost
	}
	return nil
}

func (s *Store) SetPhase(ctx context.Context, l state.Lease, phase state.Phase, lastErr string) error {
	if !phase.Valid() {
		return fmt.Errorf("invalid phase %q", phase)
	}
	return s.fenced(ctx, l, "phase=$3, last_error=$4", string(phase), truncate(lastErr))
}

func (s *Store) SetConfigHash(ctx context.Context, l state.Lease, hash string) error {
	return s.fenced(ctx, l, "config_hash=$3", hash)
}

func (s *Store) SetPhaseAdmin(ctx context.Context, id string, phase state.Phase, lastErr string) error {
	if !phase.Valid() {
		return fmt.Errorf("invalid phase %q", phase)
	}
	r, err := s.db.ExecContext(ctx, `UPDATE `+s.table("migration_tasks")+` SET phase=$2, last_error=$3, updated_at=now() WHERE task_id=$1`, id, string(phase), truncate(lastErr))
	if err != nil {
		return errors.New("update task failed")
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return state.ErrTaskNotFound
	}
	return nil
}

// truncate bounds stored error summaries.
func truncate(s string) string {
	const limit = 1024
	if len(s) > limit {
		return s[:limit]
	}
	return s
}
