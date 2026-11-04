package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hyperbolic-c/newapi2ACG/internal/service"
)

// ErrJobNotFound means the job row has not been registered with Ensure.
var ErrJobNotFound = errors.New("job is not registered in the state store")

type jobRow struct {
	state, resumeFrom, reason string
}

// jobSQL isolates the three statements so repository rules (validation,
// error sanitization, row-count checks) are testable without a database.
type jobSQL interface {
	insert(ctx context.Context, job string, definition []byte, configHash string) error
	load(ctx context.Context, job string) (jobRow, error)
	update(ctx context.Context, job string, row jobRow) (int64, error)
}

type sqlJobs struct{ db *sql.DB }

func (s sqlJobs) insert(ctx context.Context, job string, definition []byte, configHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO migrator.jobs(job_id,definition,config_hash) VALUES($1,$2::jsonb,$3) ON CONFLICT (job_id) DO NOTHING`, job, string(definition), configHash)
	return err
}
func (s sqlJobs) load(ctx context.Context, job string) (jobRow, error) {
	var state string
	var resume, reason sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT state,resume_from,reason FROM migrator.jobs WHERE job_id=$1`, job).Scan(&state, &resume, &reason)
	return jobRow{state: state, resumeFrom: resume.String, reason: reason.String}, err
}
func (s sqlJobs) update(ctx context.Context, job string, row jobRow) (int64, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE migrator.jobs SET state=$2,resume_from=NULLIF($3,''),reason=NULLIF($4,''),updated_at=now() WHERE job_id=$1`, job, row.state, row.resumeFrom, row.reason)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// JobRepository persists the service state machine in migrator.jobs. It only
// touches state, resume_from and reason; checkpoints, generations and the
// mirror are owned by other components and are never reset here.
type JobRepository struct{ sql jobSQL }

var _ service.Repository = (*JobRepository)(nil)

func NewJobRepository(db *sql.DB) *JobRepository { return &JobRepository{sql: sqlJobs{db}} }

// Ensure registers the job in PRECHECK if it does not exist. An existing row is
// left untouched, including its state and config_hash: switching configuration
// versions is the hot-reload protocol's responsibility (design 8.3), not
// something a restart may do implicitly.
func (r *JobRepository) Ensure(ctx context.Context, job string, definition any, configHash string) error {
	if r == nil || r.sql == nil || strings.TrimSpace(job) == "" {
		return errors.New("job repository requires a database and job")
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return errors.New("encode job definition failed")
	}
	if err := r.sql.insert(ctx, job, encoded, configHash); err != nil {
		return errors.New("register job failed")
	}
	return nil
}

func (r *JobRepository) Load(ctx context.Context, job string) (service.JobState, error) {
	if r == nil || r.sql == nil {
		return service.JobState{}, errors.New("job repository requires a database")
	}
	row, err := r.sql.load(ctx, job)
	if errors.Is(err, sql.ErrNoRows) {
		return service.JobState{}, ErrJobNotFound
	}
	if err != nil {
		return service.JobState{}, errors.New("load job state failed")
	}
	s := service.JobState{State: service.State(row.state), ResumeFrom: service.State(row.resumeFrom), Reason: row.reason}
	if err := s.Validate(); err != nil {
		return service.JobState{}, errors.New("persisted job state is invalid; refusing to run")
	}
	return s, nil
}

func (r *JobRepository) Save(ctx context.Context, job string, s service.JobState) error {
	if r == nil || r.sql == nil {
		return errors.New("job repository requires a database")
	}
	if err := s.Validate(); err != nil {
		return err
	}
	n, err := r.sql.update(ctx, job, jobRow{state: string(s.State), resumeFrom: string(s.ResumeFrom), reason: s.Reason})
	if err != nil {
		return errors.New("save job state failed")
	}
	if n != 1 {
		return ErrJobNotFound
	}
	return nil
}
