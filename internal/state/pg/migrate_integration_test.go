//go:build integration

package pg

import (
	"context"
	"database/sql"
	"net/url"
	"testing"

	"github.com/hyperbolic-c/newapi2ACG/internal/state/pg/pgtest"
	"github.com/lib/pq"
)

// Each test creates its own database via pgtest; see that package for the
// isolation rules.
func integrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, _ := integrationDatabase(t)
	return db
}

func integrationDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	return pgtest.IsolatedDatabase(t)
}
func TestMigrateFromEmpty(t *testing.T) {
	db := integrationDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"jobs", "checkpoints", "pending", "naming_registry", "id_map", "last_applied", "key_access", "operations", "failed_items", "target_snapshots", "config_versions", "audit_log"} {
		var exists bool
		if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='migrator' AND table_name=$1)", name).Scan(&exists); err != nil || !exists {
			t.Errorf("missing table %s", name)
		}
	}
}
func TestMigrateIdempotent(t *testing.T) {
	db := integrationDB(t)
	for i := 0; i < 2; i++ {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM migrator.schema_migrations").Scan(&count); err != nil || count != 2 {
		t.Fatal("unexpected migration count")
	}
}
func TestNamingRegistryUnique(t *testing.T) {
	db := integrationDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO migrator.jobs(job_id) VALUES ('a'),('b')"); err != nil {
		t.Fatal("insert jobs failed")
	}
	insert := "INSERT INTO migrator.naming_registry(job_id,target_id,kind,name,source_identity) VALUES ($1,$2,'Provider','shared',$3)"
	if _, err := db.Exec(insert, "a", "target", "source/Provider/a"); err != nil {
		t.Fatal("first name insert failed")
	}
	_, err := db.Exec(insert, "b", "target", "source/Provider/b")
	pgErr, ok := err.(*pq.Error)
	if !ok || pgErr.Code != "23505" {
		t.Fatal("duplicate target/kind/name did not violate unique constraint")
	}
	if _, err := db.Exec(insert, "b", "other-target", "source/Provider/b"); err != nil {
		t.Fatal("independent target name rejected")
	}
}

func TestIsolatedDatabaseURL(t *testing.T) {
	u, _ := url.Parse("postgres://localhost/original?dbname=original&database=original&sslmode=disable")
	isolated := pgtest.IsolatedDatabaseURL(u, "isolated")
	parsed, _ := url.Parse(isolated)
	if parsed.Path != "/isolated" || parsed.Query().Get("dbname") != "" || parsed.Query().Get("database") != "" || parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("database override survived isolation")
	}
}
