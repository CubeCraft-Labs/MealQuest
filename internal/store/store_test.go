package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestOpenCreatesDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("database file: %v", err)
	}
}

func TestOpenMissingDirFails(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("Open succeeded on a missing data directory")
	}
}

func TestPoolSizes(t *testing.T) {
	s := openTestStore(t)
	if got := s.Writer.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer max open connections = %d, want 1", got)
	}
	if got := s.Reader.Stats().MaxOpenConnections; got != readPoolSize {
		t.Errorf("reader max open connections = %d, want %d", got, readPoolSize)
	}
}

// TestPragmasOnEveryConnection holds every reader connection open at once so
// each one is a distinct connection, then checks the pragmas on each and on
// the writer.
func TestPragmasOnEveryConnection(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	conns := []*sql.Conn{}
	for range readPoolSize {
		c, err := s.Reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns = append(conns, c)
	}
	w, err := s.Writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	conns = append(conns, w)

	for i, c := range conns {
		var fk, busy int
		var journal string
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || busy != 5000 || journal != "wal" {
			t.Errorf("connection %d: foreign_keys=%d busy_timeout=%d journal_mode=%q, want 1, 5000, wal", i, fk, busy, journal)
		}
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Reader.Exec("CREATE TABLE t (id INTEGER)"); err == nil {
		t.Fatal("write through the reader succeeded")
	}
	if _, err := s.Writer.Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatalf("write through the writer: %v", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Writer.Exec(`
		CREATE TABLE parent (id INTEGER PRIMARY KEY);
		CREATE TABLE child (parent_id INTEGER NOT NULL REFERENCES parent(id));
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Writer.Exec("INSERT INTO child (parent_id) VALUES (42)"); err == nil {
		t.Fatal("insert violating a foreign key succeeded")
	}
}
