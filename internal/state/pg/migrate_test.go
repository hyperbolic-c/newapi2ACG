package pg

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeTx struct {
	applied               string
	calls                 []string
	fail                  string
	committed, rolledBack bool
}

func (f *fakeTx) exec(_ context.Context, q string, args ...any) error {
	f.calls = append(f.calls, q)
	if strings.Contains(q, f.fail) && f.fail != "" {
		return errors.New("private connection details")
	}
	return nil
}
func (f *fakeTx) checksum(_ context.Context, version int) (string, error) {
	if version == 2 {
		return "", nil
	}
	return f.applied, nil
}
func (f *fakeTx) commit() error   { f.committed = true; return nil }
func (f *fakeTx) rollback() error { f.rolledBack = true; return nil }
func TestMigrationTransaction(t *testing.T) {
	f := &fakeTx{}
	if err := migrate(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if !f.committed {
		t.Fatal("not committed")
	}
	joined := strings.Join(f.calls, "\n")
	for _, name := range []string{"jobs", "checkpoints", "pending", "naming_registry", "id_map", "last_applied", "key_access", "operations", "failed_items", "target_snapshots", "config_versions", "audit_log"} {
		if !strings.Contains(joined, "CREATE TABLE migrator."+name+" (") {
			t.Errorf("missing %s", name)
		}
	}
	// design v3.3: no copy of New API data is kept in the state schema.
	for _, removed := range []string{"mirror_rows", "generations", "deps"} {
		if strings.Contains(joined, "migrator."+removed) {
			t.Errorf("source-copy table %s must not exist", removed)
		}
	}
	if !strings.Contains(f.calls[0], "pg_advisory_xact_lock") {
		t.Fatal("migration not serialized before DDL")
	}
}
func TestMigrationFailureRollsBack(t *testing.T) {
	f := &fakeTx{fail: "CREATE TABLE migrator.jobs"}
	err := migrate(context.Background(), f)
	if err == nil || f.committed || !f.rolledBack {
		t.Fatal("failed migration not rolled back")
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("driver error leaked")
	}
}
func TestMigrationReplayAndChecksum(t *testing.T) {
	for _, match := range []bool{true, false} {
		f := &fakeTx{applied: "modified"}
		if match {
			f.applied = migrationChecksum()
		}
		err := migrate(context.Background(), f)
		if match && (err != nil || !f.committed) {
			t.Fatal("replay failed")
		}
		if !match && (err == nil || f.committed) {
			t.Fatal("checksum mismatch accepted")
		}
		for _, q := range f.calls {
			if strings.Contains(q, "CREATE TABLE migrator.jobs") {
				t.Fatal("replayed schema")
			}
		}
	}
}
