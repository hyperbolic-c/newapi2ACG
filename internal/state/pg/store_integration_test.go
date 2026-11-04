//go:build integration

package pg

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/model"
	"github.com/hyperbolic-c/newapi2ACG/internal/state"
	"github.com/hyperbolic-c/newapi2ACG/test/contract"
)

func integrationStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db := integrationDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO migrator.jobs(job_id) VALUES ('job'),('other')"); err != nil {
		t.Fatal("create test jobs failed")
	}
	return &Store{DB: db}, db
}
func beginState(t *testing.T, s *Store) state.Transaction {
	t.Helper()
	tx, err := s.Begin(context.Background(), "job", "target")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}
func requireCommit(t *testing.T, tx state.Transaction) {
	t.Helper()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func queuedID() model.Identity {
	return contract.Group("group:a", "a").Metadata.SourceIdentity
}
func pendingCount(t *testing.T, tx state.Transaction) int {
	t.Helper()
	items, err := tx.TakePending(10)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}
func TestRollbackDoesNotAdvance(t *testing.T) {
	s, _ := integrationStore(t)
	start := beginState(t, s)
	if err := start.SetFullSyncStart("uuid:1"); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, start)
	tx := beginState(t, s)
	if err := tx.Enqueue([]model.Identity{queuedID()}, "source_change"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Advance("uuid:1-2"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	read := beginState(t, s)
	if pendingCount(t, read) != 0 {
		t.Fatal("rollback retained queued resource")
	}
	pos, err := read.Position()
	if err != nil || pos != "uuid:1" {
		t.Fatalf("rollback changed position: %q %v", pos, err)
	}
}
func TestEnqueueAndCheckpointAtomic(t *testing.T) {
	s, _ := integrationStore(t)
	start := beginState(t, s)
	if err := start.SetFullSyncStart("uuid:1"); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, start)
	tx := beginState(t, s)
	if err := tx.Enqueue([]model.Identity{queuedID()}, "source_change"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Advance("uuid:1-2"); err != nil {
		t.Fatal(err)
	}
	before := beginState(t, s)
	if pendingCount(t, before) != 0 {
		t.Fatal("uncommitted queue entry visible")
	}
	if pos, err := before.Position(); err != nil || pos != "uuid:1" {
		t.Fatal("uncommitted position visible")
	}
	if err := before.Rollback(); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, tx)
	read := beginState(t, s)
	if pendingCount(t, read) != 1 {
		t.Fatal("committed queue entry missing")
	}
	if pos, err := read.Position(); err != nil || pos != "uuid:1-2" {
		t.Fatal("committed position missing")
	}
	other, err := s.Begin(context.Background(), "other", "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback() }()
	if pos, err := other.Position(); err != nil || pos != "" {
		t.Fatal("position crossed job scope")
	}
}
func TestAdvanceWithoutFullSyncStartFails(t *testing.T) {
	s, _ := integrationStore(t)
	tx := beginState(t, s)
	if err := tx.Advance("uuid:1-2"); err == nil {
		t.Fatal("advance accepted before full sync recorded G0")
	}
}
func TestDequeueSkipsReenqueued(t *testing.T) {
	s, _ := integrationStore(t)
	id := contract.Group("group:a", "a").Metadata.SourceIdentity
	first := beginState(t, s)
	if err := first.Enqueue([]model.Identity{id}, "first"); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, first)
	read := beginState(t, s)
	old, err := read.TakePending(10)
	if err != nil || len(old) != 1 {
		t.Fatal("missing pending")
	}
	if err := read.Rollback(); err != nil {
		t.Fatal(err)
	}
	again := beginState(t, s)
	if err := again.Enqueue([]model.Identity{id}, "second"); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, again)
	stale := beginState(t, s)
	if err := stale.Dequeue(old); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, stale)
	read = beginState(t, s)
	current, err := read.TakePending(10)
	if err != nil || len(current) != 1 || current[0].Version <= old[0].Version {
		t.Fatal("stale receipt removed new work")
	}
	if err := read.Rollback(); err != nil {
		t.Fatal(err)
	}
	remove := beginState(t, s)
	if err := remove.Dequeue(current); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, remove)
	fresh := beginState(t, s)
	if err := fresh.Enqueue([]model.Identity{id}, "third"); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, fresh)
	read = beginState(t, s)
	last, err := read.TakePending(10)
	if err != nil || len(last) != 1 || last[0].Version <= current[0].Version {
		t.Fatal("dequeue/re-enqueue reused version")
	}
}
func TestIntentVisibleAfterCommit(t *testing.T) {
	s, db := integrationStore(t)
	c := contract.Change("op", contract.Group("group:a", "a"))
	tx := beginState(t, s)
	if err := tx.RecordIntent([]model.Change{c}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM migrator.operations").Scan(&count); err != nil || count != 0 {
		t.Fatal("uncommitted intent visible")
	}
	requireCommit(t, tx)
	var status string
	if err := db.QueryRow("SELECT status FROM migrator.operations WHERE job_id='job' AND op_id='op'").Scan(&status); err != nil || status != "pending" {
		t.Fatal("committed intent missing")
	}
	repeat := beginState(t, s)
	if err := repeat.RecordIntent([]model.Change{c}); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, repeat)
	c.Resource.Group.Name = "changed"
	conflict := beginState(t, s)
	if conflict.RecordIntent([]model.Change{c}) == nil || conflict.Commit() == nil {
		t.Fatal("changed intent accepted")
	}
}
func TestResultReceiptAtomicAndImmutable(t *testing.T) {
	s, db := integrationStore(t)
	c := contract.Change("op", contract.Group("group:a", "a"))
	tx := beginState(t, s)
	if err := tx.RecordIntent([]model.Change{c}); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, tx)
	item := state.AppliedResult{Change: c, Result: model.Result{OpID: c.OpID, Outcome: model.Succeeded, TargetID: "target-1"}, ConfigHash: strings.Repeat("a", 64)}
	results := beginState(t, s)
	if err := results.RecordResults([]state.AppliedResult{item}); err != nil {
		t.Fatal(err)
	}
	if err := results.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM migrator.last_applied").Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback retained resource receipt")
	}
	results = beginState(t, s)
	if err := results.RecordResults([]state.AppliedResult{item}); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, results)
	read := beginState(t, s)
	last, err := read.LastApplied([]model.Identity{c.Resource.Metadata.SourceIdentity})
	if err != nil || len(last) != 1 {
		t.Fatal("resource receipt missing")
	}
	if err := read.Rollback(); err != nil {
		t.Fatal(err)
	}
	repeat := beginState(t, s)
	if err := repeat.RecordResults([]state.AppliedResult{item}); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, repeat)
	item.Result.Outcome = model.Failed
	bad := beginState(t, s)
	if bad.RecordResults([]state.AppliedResult{item}) == nil || bad.Commit() == nil {
		t.Fatal("successful receipt downgraded")
	}
}

func TestLateResultCannotOverwriteNewIntent(t *testing.T) {
	s, db := integrationStore(t)
	original := contract.Change("old-delete", contract.Group("group:a", "old"))
	original.Op = model.Delete
	latest := contract.Change("new-create", contract.Group("group:a", "new"))
	for _, c := range []model.Change{original, latest} {
		tx := beginState(t, s)
		if err := tx.RecordIntent([]model.Change{c}); err != nil {
			t.Fatal(err)
		}
		requireCommit(t, tx)
	}
	var oldStatus, oldCode string
	if err := db.QueryRow("SELECT status,error_code FROM migrator.operations WHERE job_id='job' AND op_id='old-delete'").Scan(&oldStatus, &oldCode); err != nil || oldStatus != "failed" || oldCode != "superseded" {
		t.Fatal("superseded intent remains unresolved")
	}
	queued := beginState(t, s)
	items, err := queued.TakePending(10)
	if err != nil || len(items) != 1 {
		t.Fatal("superseded intent did not schedule current reconciliation")
	}
	if err := queued.Rollback(); err != nil {
		t.Fatal(err)
	}
	current := state.AppliedResult{Change: latest, Result: model.Result{OpID: latest.OpID, Outcome: model.Succeeded, TargetID: "new-id"}, ConfigHash: strings.Repeat("a", 64)}
	tx := beginState(t, s)
	if err := tx.RecordResults([]state.AppliedResult{current}); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, tx)
	// Replaying an old registration must not move the current intent backwards.
	replay := beginState(t, s)
	if err := replay.RecordIntent([]model.Change{original}); err != nil {
		t.Fatal(err)
	}
	requireCommit(t, replay)
	late := state.AppliedResult{Change: original, Result: model.Result{OpID: original.OpID, Outcome: model.Succeeded}, ConfigHash: strings.Repeat("a", 64)}
	tx = beginState(t, s)
	if tx.RecordResults([]state.AppliedResult{late}) == nil || tx.Commit() == nil {
		t.Fatal("late delete receipt accepted")
	}
	read := beginState(t, s)
	resources, err := read.LastApplied([]model.Identity{latest.Resource.Metadata.SourceIdentity})
	if err != nil || len(resources) != 1 || resources[latest.Resource.Metadata.SourceIdentity].Group.Name != "new" {
		t.Fatal("late receipt destroyed new state")
	}
}
