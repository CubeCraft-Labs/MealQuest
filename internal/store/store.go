// Package store owns the MealQuest SQLite database: one writer connection,
// a read pool, and the embedded schema migrations.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// FileName is the database file inside the data directory.
const FileName = "mealquest.db"

// readPoolSize is the maximum number of open read connections.
const readPoolSize = 4

// connPragmas are applied by the driver to every new connection.
var connPragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"foreign_keys(1)",
}

// Store holds the database handles. All writes go through Writer, which has
// exactly one connection; Reader is a pool of read-only connections.
type Store struct {
	Writer *sql.DB
	Reader *sql.DB
}

// Open opens <dataDir>/mealquest.db, creating it if absent, and verifies both
// handles can connect. It does not apply migrations; call Migrate for that.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	path, err := filepath.Abs(filepath.Join(dataDir, FileName))
	if err != nil {
		return nil, fmt.Errorf("store: resolve database path: %w", err)
	}

	// The writer is opened and pinged first so the file exists and is in WAL
	// mode before any read-only connection touches it.
	writer, err := openDB(ctx, path, 1, url.Values{"_txlock": {"immediate"}})
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	reader, err := openDB(ctx, path, readPoolSize, url.Values{"_pragma": {"query_only(1)"}})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("store: open reader: %w", err), writer.Close())
	}
	return &Store{Writer: writer, Reader: reader}, nil
}

// Close closes both handles.
func (s *Store) Close() error {
	return errors.Join(s.Reader.Close(), s.Writer.Close())
}

func openDB(ctx context.Context, path string, maxConns int, extra url.Values) (*sql.DB, error) {
	q := url.Values{"_pragma": connPragmas}
	for k, vs := range extra {
		q[k] = append(q[k], vs...)
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}
