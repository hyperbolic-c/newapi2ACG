package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/service"
	"github.com/hyperbolic-c/newapi2ACG/internal/state/pg"
	_ "github.com/lib/pq"
)

// precheckPending is the persisted reason while source probing and target
// capability checks (M2) are not implemented. The job must not advance past
// PRECHECK on a stub, so the handler reports failure and the runner retries.
const precheckPending = "precheck not implemented yet: source probe and target capability checks are pending (M2)"

type stateBackend interface {
	service.Repository
	Ensure(ctx context.Context, job string, definition any, configHash string) error
	Close() error
}

type jobLease interface {
	Context() context.Context
	Close() error
}

// runEnv holds the process boundaries so the command is testable without a
// database. Production values come from defaultRunEnv.
type runEnv struct {
	getenv    func(string) string
	openState func(ctx context.Context, dsn string) (stateBackend, error)
	acquire   func(ctx context.Context, dsn, job string) (jobLease, error)
}

func defaultRunEnv() runEnv {
	return runEnv{
		getenv:    os.Getenv,
		openState: openPGState,
		acquire: func(ctx context.Context, dsn, job string) (jobLease, error) {
			return pg.AcquireLease(ctx, dsn, job, pg.LeaseOptions{})
		},
	}
}

type pgState struct {
	*pg.JobRepository
	db *sql.DB
}

func (s pgState) Close() error { return s.db.Close() }

// openPGState connects the state store pool and applies migrations. The lease
// uses its own dedicated session (pg.AcquireLease), never this pool.
func openPGState(ctx context.Context, dsn string) (stateBackend, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.New("open state database failed")
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, errors.New("connect state database failed")
	}
	if err := pg.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return pgState{JobRepository: pg.NewJobRepository(db), db: db}, nil
}

func runCommand(ctx context.Context, args []string, errs io.Writer, env runEnv) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(errs)
	dir := flags.String("config-dir", "configs", "configuration directory")
	jobName := flags.String("job", "", "job to run (required when more than one job is configured)")
	leaseRetry := flags.Duration("lease-retry", 5*time.Second, "wait between attempts while another instance holds the job lease")
	workRetry := flags.Duration("work-retry", 30*time.Second, "wait before retrying failed state work")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errs, "run accepts no positional arguments")
		return 2
	}
	if *leaseRetry <= 0 || *workRetry <= 0 {
		fmt.Fprintln(errs, "retry durations must be positive")
		return 2
	}
	log := slog.New(slog.NewTextHandler(errs, nil))

	bundle, err := config.LoadDir(*dir)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	if err := bundle.ValidateReferences(); err != nil {
		fmt.Fprintln(errs, err)
		return 1
	}
	name, err := selectJob(bundle, *jobName)
	if err != nil {
		fmt.Fprintln(errs, err)
		return 2
	}
	job := bundle.Jobs[name]
	target := bundle.Targets[job.Job.Target]
	if target.StateDSNEnv == "" {
		fmt.Fprintf(errs, "targets/%s: state_dsn_env is required to run\n", job.Job.Target)
		return 1
	}
	dsn := env.getenv(target.StateDSNEnv)
	if dsn == "" {
		// Name the variable, never its value.
		fmt.Fprintf(errs, "environment variable %s is not set\n", target.StateDSNEnv)
		return 1
	}

	backend, err := env.openState(ctx, dsn)
	if err != nil {
		log.Error("state store unavailable", "error", err.Error())
		return 1
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Ensure(ctx, name, job, bundle.Hash); err != nil {
		log.Error("register job failed", "job", name, "error", err.Error())
		return 1
	}

	lease, ok := waitForLease(ctx, log, env, dsn, name, *leaseRetry)
	if !ok {
		if ctx.Err() != nil {
			log.Info("shutdown before acquiring job lease", "job", name)
			return 0
		}
		return 1
	}
	defer func() {
		if err := lease.Close(); err != nil {
			// The PG session ends with the process anyway, which releases the lock.
			log.Warn("close job lease failed", "job", name, "error", err.Error())
		}
	}()
	log.Info("job lease acquired", "job", name, "config_hash", bundle.Hash)

	runner := service.Runner{
		Store:      backend,
		RetryDelay: *workRetry,
		Handlers: map[service.State]service.Handler{
			service.Precheck: func(context.Context) (service.Event, error) {
				return "", &service.ReasonError{Reason: precheckPending}
			},
		},
	}
	err = runner.Run(ctx, name, lease)
	if err == nil {
		// PAUSED or STOPPED: hold the lease and idle so a Deployment does not
		// crash-loop. Resume arrives with the admin interface (M4).
		log.Info("job is paused or stopped; idling until shutdown", "job", name)
		select {
		case <-ctx.Done():
		case <-lease.Context().Done():
		}
	}
	return exitCode(ctx, log, lease, name, err)
}

func exitCode(ctx context.Context, log *slog.Logger, lease jobLease, job string, err error) int {
	if errors.Is(context.Cause(lease.Context()), pg.ErrLeaseLost) {
		log.Error("job lease lost; stopping", "job", job)
		return 1
	}
	if ctx.Err() != nil {
		log.Info("shutdown complete", "job", job)
		return 0
	}
	if err != nil {
		log.Error("job runner stopped", "job", job, "error", err.Error())
		return 1
	}
	return 0
}

func waitForLease(ctx context.Context, log *slog.Logger, env runEnv, dsn, job string, retry time.Duration) (jobLease, bool) {
	for {
		lease, err := env.acquire(ctx, dsn, job)
		if err == nil {
			return lease, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		if errors.Is(err, pg.ErrLeaseHeld) {
			log.Info("job lease held by another instance; waiting", "job", job, "retry", retry.String())
		} else {
			log.Warn("acquire job lease failed; retrying", "job", job, "error", err.Error(), "retry", retry.String())
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
	}
}

func selectJob(bundle *config.Bundle, requested string) (string, error) {
	if requested != "" {
		if _, ok := bundle.Jobs[requested]; !ok {
			return "", fmt.Errorf("job %q is not configured", requested)
		}
		return requested, nil
	}
	if len(bundle.Jobs) == 1 {
		for name := range bundle.Jobs {
			return name, nil
		}
	}
	names := make([]string, 0, len(bundle.Jobs))
	for name := range bundle.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return "", fmt.Errorf("--job is required; configured jobs: %v", names)
}
