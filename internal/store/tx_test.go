package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func openTxTestStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if _, err := s.Writer.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	return s
}

func countRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.Reader.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func insertRow(tx *sql.Tx) error {
	_, err := tx.Exec("INSERT INTO t (id) VALUES (1)")
	return err
}

func TestWithTxCommitsOnNil(t *testing.T) {
	s := openTxTestStore(t)
	if err := s.WithTx(context.Background(), insertRow); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s); n != 1 {
		t.Fatalf("rows after commit = %d, want 1", n)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	s := openTxTestStore(t)
	errBoom := errors.New("boom")
	err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
		if err := insertRow(tx); err != nil {
			t.Fatal(err)
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("WithTx error = %v, want %v", err, errBoom)
	}
	if n := countRows(t, s); n != 0 {
		t.Fatalf("rows after rollback = %d, want 0", n)
	}
}

func TestWithTxRollsBackOnPanic(t *testing.T) {
	s := openTxTestStore(t)
	const value = "kaboom"
	func() {
		defer func() {
			if p := recover(); p != value {
				t.Fatalf("recovered %v, want panic %q to propagate", p, value)
			}
		}()
		_ = s.WithTx(context.Background(), func(tx *sql.Tx) error {
			if err := insertRow(tx); err != nil {
				t.Fatal(err)
			}
			panic(value)
		})
		t.Fatal("WithTx returned instead of panicking")
	}()
	if n := countRows(t, s); n != 0 {
		t.Fatalf("rows after panic = %d, want 0", n)
	}
	// The single writer connection must have been released.
	if err := s.WithTx(context.Background(), insertRow); err != nil {
		t.Fatalf("WithTx after panic: %v", err)
	}
}
