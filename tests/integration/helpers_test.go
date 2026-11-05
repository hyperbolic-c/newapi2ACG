//go:build integration

// Package integration runs the framework against real MySQL and PostgreSQL.
// Each test creates its own databases on the servers named by
// MIGRATOR_TEST_PG_DSN (postgres URL) and MIGRATOR_TEST_MYSQL_DSN (go-mysql
// form user:password@host:port/, with binlog_format=ROW, binlog_row_image=FULL,
// binlog_row_metadata=FULL and the RELOAD/REPLICATION privileges). The
// databases named in the DSNs are never modified.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/client"
	gomysqldriver "github.com/go-mysql-org/go-mysql/driver"
	_ "github.com/lib/pq"
)

func uniqueName(prefix string) string { return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()) }

// postgresDB returns the DSN of a fresh database, dropped after the test.
func postgresDB(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("MIGRATOR_TEST_PG_DSN")
	if raw == "" {
		t.Fatal("MIGRATOR_TEST_PG_DSN is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("MIGRATOR_TEST_PG_DSN must be a postgres URL")
	}
	admin, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := uniqueName("migrator_state")
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE " + name + " WITH (FORCE)") })
	u.Path = "/" + name
	q := u.Query()
	q.Del("dbname")
	u.RawQuery = q.Encode()
	return u.String()
}

// mysqlDB returns the DSN of a fresh database plus an admin connection on it.
func mysqlDB(t *testing.T) (dsn string, conn *client.Conn) {
	t.Helper()
	raw := os.Getenv("MIGRATOR_TEST_MYSQL_DSN")
	if raw == "" {
		t.Fatal("MIGRATOR_TEST_MYSQL_DSN is required")
	}
	c, err := gomysqldriver.ParseDSN(raw)
	if err != nil || c.Addr == "" {
		t.Fatal("MIGRATOR_TEST_MYSQL_DSN must be user:password@host:port/")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := client.ConnectWithContext(ctx, c.Addr, c.User, c.Password, "", 10*time.Second)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	name := uniqueName("migrator_src")
	if _, err := admin.Execute("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Execute("DROP DATABASE " + name); _ = admin.Close() })
	if _, err := admin.Execute("USE " + name); err != nil {
		t.Fatal(err)
	}
	u := url.URL{User: url.UserPassword(c.User, c.Password), Host: c.Addr, Path: "/" + name}
	return u.String()[2:], admin // strip the leading "//"
}

func must(t *testing.T, c *client.Conn, statements ...string) {
	t.Helper()
	for _, s := range statements {
		if _, err := c.Execute(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}
