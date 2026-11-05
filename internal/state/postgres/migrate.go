package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/hyperbolic-c/newapi2ACG/migrations"
)

var schemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func validSchema(schema string) error {
	if !schemaPattern.MatchString(schema) {
		return fmt.Errorf("state schema %q must match %s", schema, schemaPattern)
	}
	return nil
}

type migration struct {
	name, sql, checksum string
}

func loadMigrations(schema string) ([]migration, error) {
	names, err := fs.Glob(migrations.Postgres, "postgres/*.sql")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	out := make([]migration, 0, len(names))
	for _, name := range names {
		raw, err := migrations.Postgres.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{
			name:     strings.TrimPrefix(name, "postgres/"),
			sql:      strings.ReplaceAll(string(raw), "{{schema}}", schema),
			checksum: fmt.Sprintf("%x", sha256.Sum256(raw)),
		})
	}
	return out, nil
}

// Migrate creates the schema and applies pending migrations in one
// transaction, serialized by a transaction-level advisory lock. Applied
// files are verified by checksum; changing an applied file is an error.
func Migrate(ctx context.Context, db *sql.DB, schema string) error {
	if err := validSchema(schema); err != nil {
		return err
	}
	ms, err := loadMigrations(schema)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin migration transaction failed")
	}
	defer func() { _ = tx.Rollback() }() // no-op after commit
	for _, q := range []string{
		"SELECT pg_advisory_xact_lock(hashtext('" + schema + ".migrations'))",
		"CREATE SCHEMA IF NOT EXISTS " + schema,
		"CREATE TABLE IF NOT EXISTS " + schema + ".schema_migrations (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())",
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return errors.New("initialize migration metadata failed")
		}
	}
	for _, m := range ms {
		var applied string
		err := tx.QueryRowContext(ctx, "SELECT checksum FROM "+schema+".schema_migrations WHERE name=$1", m.name).Scan(&applied)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("apply migration %s failed", m.name)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+schema+".schema_migrations(name,checksum) VALUES($1,$2)", m.name, m.checksum); err != nil {
				return fmt.Errorf("record migration %s failed", m.name)
			}
		case err != nil:
			return errors.New("read migration history failed")
		case applied != m.checksum:
			return fmt.Errorf("migration %s was changed after it was applied", m.name)
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.New("commit migrations failed; rerun to check the outcome")
	}
	return nil
}
