//go:build integration

// Package pgtest creates disposable PostgreSQL databases for integration tests.
// Each call creates its own database on the server named by
// MIGRATOR_TEST_PG_DSN; the supplied database itself is never migrated or
// dropped. The role needs CREATEDB on a disposable server.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
)

// IsolatedDatabase returns an open handle and DSN for a fresh database that is
// dropped when the test finishes.
func IsolatedDatabase(t testing.TB) (*sql.DB, string) {
	t.Helper()
	raw := os.Getenv("MIGRATOR_TEST_PG_DSN")
	if raw == "" {
		t.Fatal("MIGRATOR_TEST_PG_DSN is required for integration tests")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("test DSN must be a PostgreSQL URL")
	}
	admin, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal("open test server failed")
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name := fmt.Sprintf("migrator_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name)); err != nil {
		t.Fatal("create isolated test database failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(name)+" WITH (FORCE)"); err != nil {
			t.Error("cleanup isolated test database failed")
		}
	})
	dsn := IsolatedDatabaseURL(u, name)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("open isolated database failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	var actual string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&actual); err != nil || actual != name {
		t.Fatal("isolated database connection verification failed")
	}
	return db, dsn
}

// IsolatedDatabaseURL points a DSN at another database, removing dbname and
// database query overrides that would otherwise win over the path.
func IsolatedDatabaseURL(original *url.URL, name string) string {
	u := *original
	u.Path = "/" + name
	u.RawPath = ""
	query := u.Query()
	query.Del("dbname")
	query.Del("database")
	u.RawQuery = query.Encode()
	return u.String()
}
