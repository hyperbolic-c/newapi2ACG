// Package pg implements the migrator's isolated PostgreSQL state schema.
package pg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
)

//go:embed migrations/001_init.sql
var initialSQL string

//go:embed migrations/002_resource_intents.sql
var resourceIntentsSQL string

func migrationChecksum() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(initialSQL))) }

type migrationTx interface {
	exec(context.Context, string, ...any) error
	checksum(context.Context, int) (string, error)
	commit() error
	rollback() error
}
type sqlMigrationTx struct{ *sql.Tx }

func (t sqlMigrationTx) exec(ctx context.Context, q string, args ...any) error {
	_, err := t.ExecContext(ctx, q, args...)
	return err
}
func (t sqlMigrationTx) checksum(ctx context.Context, v int) (string, error) {
	var hash string
	err := t.QueryRowContext(ctx, "SELECT checksum FROM migrator.schema_migrations WHERE version = $1", v).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return hash, err
}
func (t sqlMigrationTx) commit() error   { return t.Commit() }
func (t sqlMigrationTx) rollback() error { return t.Rollback() }

// Migrate atomically applies embedded migrations. The caller supplies a DB with
// a PostgreSQL database/sql driver. Driver diagnostics are not returned because
// they can contain credentials or SQL data. A commit error has unknown outcome;
// rerunning Migrate safely checks the persisted version and checksum.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("migration database is required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return errors.New("begin migration transaction failed")
	}
	return migrate(ctx, sqlMigrationTx{tx})
}
func migrate(ctx context.Context, tx migrationTx) error {
	defer func() { _ = tx.rollback() }() // no-op after commit
	steps := []string{
		"SELECT pg_advisory_xact_lock(684739128603217)",
		"CREATE SCHEMA IF NOT EXISTS migrator",
		"CREATE TABLE IF NOT EXISTS migrator.schema_migrations (version integer PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())",
	}
	for _, q := range steps {
		if err := tx.exec(ctx, q); err != nil {
			return errors.New("initialize migration metadata failed")
		}
	}
	for index, script := range []string{initialSQL, resourceIntentsSQL} {
		version := index + 1
		expected := fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
		hash, err := tx.checksum(ctx, version)
		if err != nil {
			return errors.New("read migration version failed")
		}
		if hash != "" && hash != expected {
			return fmt.Errorf("migration %d checksum mismatch", version)
		}
		if hash == "" {
			if err := tx.exec(ctx, script); err != nil {
				return fmt.Errorf("apply migration %d failed", version)
			}
			if err := tx.exec(ctx, "INSERT INTO migrator.schema_migrations(version,checksum) VALUES ($1,$2)", version, expected); err != nil {
				return fmt.Errorf("record migration %d failed", version)
			}
		}
	}
	if err := tx.commit(); err != nil {
		return errors.New("commit migration failed; outcome unknown, rerun migration")
	}
	return nil
}
