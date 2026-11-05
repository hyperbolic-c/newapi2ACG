// Package app wires configuration into a running coordinator.
package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/mapping"
	"github.com/hyperbolic-c/newapi2ACG/internal/observability"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
	"github.com/hyperbolic-c/newapi2ACG/internal/source/mysql"
	"github.com/hyperbolic-c/newapi2ACG/internal/state/postgres"
	isync "github.com/hyperbolic-c/newapi2ACG/internal/sync"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
	"github.com/hyperbolic-c/newapi2ACG/internal/target/mock"
)

// Env resolves environment variables (os.Getenv in production).
type Env func(string) string

func require(env Env, name string) (string, error) {
	v := env(name)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return v, nil
}

// OpenState connects the state store named by the config.
func OpenState(ctx context.Context, cfg *config.Config, env Env) (*postgres.Store, *sql.DB, error) {
	dsn, err := require(env, cfg.State.DSNEnv)
	if err != nil {
		return nil, nil, err
	}
	return postgres.Open(ctx, dsn, cfg.State.Schema)
}

// App is a built task ready to run.
type App struct {
	Coordinator *isync.Coordinator
	db          *sql.DB
}

func (a *App) Close() error {
	if a.db != nil {
		return a.db.Close()
	}
	return nil
}

func newTarget(cfg *config.Config, log *slog.Logger) (target.Target, error) {
	switch cfg.Target.Type {
	case "mock":
		log.Warn("target is the in-memory mock: nothing is written to ACG (framework testing only)")
		return mock.New(), nil
	case "acg-api":
		return nil, errors.New("target type acg-api is not implemented yet: the ACG management API has not been defined; use target.type mock")
	}
	return nil, fmt.Errorf("unknown target type %q", cfg.Target.Type)
}

// Build validates the environment and connects the state store. The source
// is not contacted until the coordinator's precheck runs.
func Build(ctx context.Context, cfg *config.Config, env Env, log *slog.Logger, metrics *observability.Metrics) (*App, error) {
	srcDSN, err := require(env, cfg.Source.DSNEnv)
	if err != nil {
		return nil, err
	}
	if _, err := require(env, cfg.State.HashKeyEnv); err != nil {
		return nil, err
	}
	hasher, err := secrets.NewFingerprinter(cfg.State.HashKeyEnv)
	if err != nil {
		return nil, err
	}
	mapper, err := mapping.New(cfg.Mapping.Profile)
	if err != nil {
		return nil, err
	}
	tgt, err := newTarget(cfg, log)
	if err != nil {
		return nil, err
	}
	src, err := mysql.New(mysql.Options{
		DSN: srcDSN, ServerID: cfg.Source.Replication.ServerID, PositionMode: cfg.Source.Replication.PositionMode,
		Datasets: cfg.Source.Datasets, LockWaitTimeout: cfg.Sync.Full.LockWaitTimeout.D(), Logger: log,
	})
	if err != nil {
		return nil, err
	}
	store, db, err := OpenState(ctx, cfg, env)
	if err != nil {
		return nil, err
	}
	retry := isync.Retry{Attempts: cfg.Sync.Incremental.MaxRetries, Base: time.Second, Max: 30 * time.Second, Log: log, Metrics: metrics}
	coord := &isync.Coordinator{
		TaskID: cfg.Job.ID, SourceID: "mysql:" + src.Schema(), ConfigHash: cfg.Hash(), Owner: owner(),
		Source: src, Streamer: src, Store: store, Datasets: cfg.DatasetNames(),
		Pipeline: &isync.Pipeline{TaskID: cfg.Job.ID, Mapper: mapper, Target: tgt, Store: store, Hasher: hasher,
			Chunk: cfg.Sync.Incremental.BatchSize, Retry: retry, Log: log, Metrics: metrics},
		Full: cfg.Sync.Full, Incremental: cfg.Sync.Incremental, Reconcile: cfg.Sync.Reconcile,
		LeaseTTL: cfg.Job.LeaseTTL.D(), Log: log,
	}
	return &App{Coordinator: coord, db: db}, nil
}

// owner identifies this process in the task lease.
func owner() string {
	host, _ := os.Hostname()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b))
}
