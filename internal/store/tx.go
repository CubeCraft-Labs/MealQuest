package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// WithTx runs fn in a transaction on the writer. It commits when fn returns
// nil and rolls back when fn returns an error, panics or exits the goroutine.
// A panic is not recovered: it propagates unchanged after the rollback.
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.Writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback() // fn panicked; the panic carries on regardless
		}
	}()

	if err := fn(tx); err != nil {
		finished = true
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("store: roll back transaction: %w", rbErr))
		}
		return err
	}
	finished = true
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit transaction: %w", err)
	}
	return nil
}
