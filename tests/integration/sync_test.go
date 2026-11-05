//go:build integration

package integration

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/config"
	"github.com/hyperbolic-c/newapi2ACG/internal/mapping"
	"github.com/hyperbolic-c/newapi2ACG/internal/observability"
	"github.com/hyperbolic-c/newapi2ACG/internal/secrets"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/state/postgres"
	isync "github.com/hyperbolic-c/newapi2ACG/internal/sync"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
	"github.com/hyperbolic-c/newapi2ACG/internal/target/mock"
)

// TestSyncEndToEnd: full sync with concurrent writes, incremental changes,
// and a restart that resumes from the PostgreSQL checkpoint.
func TestSyncEndToEnd(t *testing.T) {
	dsn, admin := mysqlDB(t)
	must(t, admin, "CREATE TABLE users (id bigint PRIMARY KEY, name varchar(64), api_key varchar(64), updated datetime(3)) ENGINE=InnoDB",
		"INSERT INTO users VALUES (1,'a','k1',NULL),(2,'b','k2',NULL)")
	ctx := context.Background()
	store, db, err := postgres.Open(ctx, postgresDB(t), "migration")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t.Setenv("IT_HASH_KEY", "integration")
	hasher, _ := secrets.NewFingerprinter("IT_HASH_KEY")
	tgt := mock.New()
	src := mysqlSource(t, dsn)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	newCoord := func(owner string) *isync.Coordinator {
		m := observability.NewMetrics()
		retry := isync.Retry{Attempts: 3, Base: 10 * time.Millisecond, Max: 100 * time.Millisecond, Log: log, Metrics: m}
		return &isync.Coordinator{TaskID: "it", SourceID: "mysql", ConfigHash: "h", Owner: owner,
			Source: src, Streamer: src, Store: store, Datasets: []string{"users"},
			Pipeline: &isync.Pipeline{TaskID: "it", Mapper: mapping.Passthrough{}, Target: tgt, Store: store, Hasher: hasher, Chunk: 50, Retry: retry, Log: log, Metrics: m},
			Full:     config.Full{Enabled: true, OnFirstRun: true, BatchSize: 1}, Incremental: config.Incremental{Enabled: true},
			Reconcile: config.Reconcile{Interval: config.Duration(time.Hour), BatchSize: 100, DeletePolicy: "report"},
			LeaseTTL:  6 * time.Second, Log: log, ControlInterval: 50 * time.Millisecond, IdleFlush: 50 * time.Millisecond, ErrorBackoff: 50 * time.Millisecond}
	}
	runFor := func(owner string, until func() bool) {
		t.Helper()
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- newCoord(owner).Run(ctx) }()
		deadline := time.Now().Add(30 * time.Second)
		for !until() {
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("timeout; target=%v", tgt.Resources())
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	streaming := func() bool { task, _ := store.LoadTask(ctx, "it"); return task.Phase == state.Streaming }
	id := func(k string) target.TargetIdentity { return target.TargetIdentity{Kind: "users", ID: k} }

	runFor("pod-1", func() bool { return streaming() && len(tgt.Resources()) == 2 })
	// Changes while no instance runs, then a new instance resumes from the checkpoint.
	must(t, admin, "INSERT INTO users VALUES (3,'c','k3',NULL)", "UPDATE users SET name='a2' WHERE id=1", "DELETE FROM users WHERE id=2")
	runFor("pod-2", func() bool {
		r := tgt.Resources()
		return streaming() && len(r) == 2 && r[id("1")]["name"] == "a2" && r[id("3")] != nil
	})
}
