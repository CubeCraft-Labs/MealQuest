package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

const createSchemaMigrations = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    TEXT PRIMARY KEY,
	applied_at TEXT NOT NULL
)`

// Migrate applies every *.sql file at the root of migrations that is not yet
// recorded in schema_migrations, in filename order. Each migration runs in its
// own transaction together with the insert that records it, so a failing
// migration leaves the database at the previous version. A version is the
// file name without its .sql extension.
//
// The database must not hold a version unknown to migrations: that means it
// was migrated by a newer build, and Migrate refuses to continue.
func Migrate(ctx context.Context, db *sql.DB, migrations fs.FS) error {
	files, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return fmt.Errorf("migrate: list migrations: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("migrate: no migrations found")
	}

	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	known := make(map[string]bool, len(files))
	for _, name := range files {
		known[strings.TrimSuffix(name, ".sql")] = true
	}
	for version := range applied {
		if !known[version] {
			return fmt.Errorf("migrate: database has version %q, which this build does not know", version)
		}
	}

	for _, name := range files { // fs.Glob returns names in lexical order
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}
		body, err := fs.ReadFile(migrations, name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if err := apply(ctx, db, version, string(body)); err != nil {
			return fmt.Errorf("migrate: apply %s: %w", name, err)
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	return applied, nil
}

func apply(ctx context.Context, db *sql.DB, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return err
	}
	return tx.Commit()
}
