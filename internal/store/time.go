package store

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

// timestampLayout is RFC 3339 in UTC with fixed millisecond precision, so
// stored timestamps sort lexicographically in time order.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// Timestamp is an instant stored as UTC RFC 3339 TEXT with millisecond
// precision. Sub-millisecond precision is dropped on write. Use
// sql.Null[Timestamp] for a nullable column.
type Timestamp struct {
	time.Time
}

// Now returns the current instant as a Timestamp, in UTC at millisecond
// precision, so it equals its stored form.
func Now() Timestamp {
	return Timestamp{time.Now().UTC().Truncate(time.Millisecond)}
}

// String returns the stored TEXT form.
func (t Timestamp) String() string {
	return t.UTC().Format(timestampLayout)
}

// Value implements driver.Valuer. The zero Timestamp is an error.
func (t Timestamp) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, errors.New("store: zero Timestamp")
	}
	return t.String(), nil
}

// Scan implements sql.Scanner. It accepts only TEXT in the stored form.
func (t *Timestamp) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("store: scan Timestamp: want TEXT, got %T", src)
	}
	parsed, err := time.Parse(timestampLayout, s)
	if err != nil {
		return fmt.Errorf("store: scan Timestamp %q: %w", s, err)
	}
	t.Time = parsed
	return nil
}

// localDateLayout is the household-local calendar date form.
const localDateLayout = "2006-01-02"

// LocalDate is a household-local calendar date with no time or zone, stored
// as YYYY-MM-DD TEXT. The zero LocalDate is invalid and cannot be stored.
type LocalDate struct {
	year  int
	month time.Month
	day   int
}

// ParseLocalDate parses a YYYY-MM-DD date, rejecting any other form and any
// date that does not exist on the calendar.
func ParseLocalDate(s string) (LocalDate, error) {
	t, err := time.Parse(localDateLayout, s)
	if err != nil {
		return LocalDate{}, fmt.Errorf("store: invalid date %q: %w", s, err)
	}
	if t.Format(localDateLayout) != s {
		return LocalDate{}, fmt.Errorf("store: invalid date %q: want YYYY-MM-DD", s)
	}
	return LocalDate{t.Year(), t.Month(), t.Day()}, nil
}

// String returns the YYYY-MM-DD form.
func (d LocalDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.year, int(d.month), d.day)
}

// IsZero reports whether d is the zero LocalDate.
func (d LocalDate) IsZero() bool {
	return d == LocalDate{}
}

// Value implements driver.Valuer. The zero LocalDate is an error.
func (d LocalDate) Value() (driver.Value, error) {
	if d.IsZero() {
		return nil, errors.New("store: zero LocalDate")
	}
	return d.String(), nil
}

// Scan implements sql.Scanner. It accepts only YYYY-MM-DD TEXT.
func (d *LocalDate) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("store: scan LocalDate: want TEXT, got %T", src)
	}
	parsed, err := ParseLocalDate(s)
	if err != nil {
		return fmt.Errorf("store: scan LocalDate: %w", err)
	}
	*d = parsed
	return nil
}
