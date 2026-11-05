//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hyperbolic-c/newapi2ACG/internal/event"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/internal/state/postgres"
	"github.com/hyperbolic-c/newapi2ACG/internal/target"
)

func TestPostgresStore(t *testing.T) {
	ctx := context.Background()
	dsn := postgresDB(t)
	s, db, err := postgres.Open(ctx, dsn, "migration")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := postgres.Migrate(ctx, db, "migration"); err != nil {
		t.Fatalf("migrations not idempotent: %v", err)
	}
	if _, err := s.EnsureTask(ctx, "t", "src", "h1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AcquireLease(ctx, "t", "a", 30*time.Second); !ok || err != nil {
		t.Fatalf("acquire: %v %v", ok, err)
	}
	if ok, _ := s.AcquireLease(ctx, "t", "b", 30*time.Second); ok {
		t.Fatal("second owner acquired a live lease")
	}
	a := state.Lease{TaskID: "t", Owner: "a"}
	key := state.MapKey{Source: target.SourceRef{Dataset: "users", Key: "1"}, Identity: target.TargetIdentity{Kind: "users", ID: "1"}}
	cp := state.Checkpoint{SnapshotDone: true, AppliedPosition: event.SourcePosition{Mode: event.ModeFilePos, BinlogFile: "b.1", BinlogPos: 9}}
	if err := s.Commit(ctx, a, state.Commit{Upserts: []state.MapEntry{{MapKey: key, TargetID: "x", Hash: "h", LastSeenRun: "r1"}}, Checkpoint: &cp}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCheckpoint(ctx, "t")
	if err != nil || got.AppliedPosition != cp.AppliedPosition || !got.SnapshotDone {
		t.Fatalf("checkpoint %+v %v", got, err)
	}
	hashes, _ := s.Hashes(ctx, "t", []target.TargetIdentity{key.Identity})
	if hashes[key.Identity] != "h" {
		t.Fatalf("hashes %v", hashes)
	}
	if unseen, _ := s.Unseen(ctx, "t", "r2", []string{"users"}); len(unseen) != 1 || unseen[0].TargetID != "x" {
		t.Fatalf("unseen %+v", unseen)
	}
	// A stale owner can neither commit nor change the phase.
	if _, err := db.Exec("UPDATE migration.migration_tasks SET lease_until = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AcquireLease(ctx, "t", "b", 30*time.Second); !ok {
		t.Fatal("expired lease not taken over")
	}
	if err := s.Commit(ctx, a, state.Commit{Checkpoint: &state.Checkpoint{}}); !errors.Is(err, state.ErrLeaseLost) {
		t.Fatalf("stale commit: %v", err)
	}
	if err := s.SetPhase(ctx, a, state.Streaming, ""); !errors.Is(err, state.ErrLeaseLost) {
		t.Fatalf("stale phase: %v", err)
	}
}
