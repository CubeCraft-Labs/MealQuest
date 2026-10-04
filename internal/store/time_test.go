package store

import (
	"context"
	"testing"
	"time"
)

func openTypesTestStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if _, err := s.Writer.Exec("CREATE TABLE v (val TEXT)"); err != nil {
		t.Fatal(err)
	}
	return s
}

// roundTrip writes in through the writer, then reads it back into out and
// returns the stored SQLite type and raw text.
func roundTrip(t *testing.T, s *Store, in, out any) (typ, raw string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Writer.ExecContext(ctx, "DELETE FROM v"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Writer.ExecContext(ctx, "INSERT INTO v (val) VALUES (?)", in); err != nil {
		t.Fatal(err)
	}
	if err := s.Reader.QueryRowContext(ctx, "SELECT typeof(val), val, val FROM v").Scan(&typ, &raw, out); err != nil {
		t.Fatal(err)
	}
	return typ, raw
}

func TestTimestampRoundTrip(t *testing.T) {
	s := openTypesTestStore(t)
	zone := time.FixedZone("EDT", -4*60*60)
	in := Timestamp{time.Date(2026, 10, 3, 8, 37, 5, 123456789, zone)}

	var out Timestamp
	typ, raw := roundTrip(t, s, in, &out)
	if typ != "text" || raw != "2026-10-03T12:37:05.123Z" {
		t.Fatalf("stored %s %q, want text %q", typ, raw, "2026-10-03T12:37:05.123Z")
	}
	if want := in.Truncate(time.Millisecond); !out.Equal(want) {
		t.Fatalf("read back %v, want %v", out, want)
	}
	if out.Location() != time.UTC {
		t.Fatalf("read back location %v, want UTC", out.Location())
	}

	now := Now()
	roundTrip(t, s, now, &out)
	if out != now {
		t.Fatalf("Now() round trip = %#v, want %#v", out, now)
	}
}

func TestTimestampRejects(t *testing.T) {
	if _, err := (Timestamp{}).Value(); err == nil {
		t.Error("zero Timestamp Value succeeded")
	}
	for _, tm := range []time.Time{
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(-1, 12, 31, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("west", -2*3600)),
	} {
		if v, err := (Timestamp{tm}).Value(); err == nil {
			t.Errorf("Timestamp{%v}.Value() = %v, want error", tm, v)
		}
	}
	for _, src := range []any{
		nil, int64(0), []byte("2026-10-03T12:37:05.123Z"), "2026-10-03T12:37:05Z",
		"2026-10-03T08:37:05.123-04:00", "2026-10-03T12:37:05,123Z", "garbage",
		"0001-01-01T00:00:00.000Z",
	} {
		var ts Timestamp
		if err := ts.Scan(src); err == nil {
			t.Errorf("Scan(%#v) succeeded", src)
		}
	}
}

func TestLocalDateRoundTrip(t *testing.T) {
	s := openTypesTestStore(t)
	for _, str := range []string{"2026-10-03", "2028-02-29", "0001-01-01", "9999-12-31"} {
		in, err := ParseLocalDate(str)
		if err != nil {
			t.Fatalf("ParseLocalDate(%q): %v", str, err)
		}
		if in.String() != str {
			t.Fatalf("String() = %q, want %q", in.String(), str)
		}
		var out LocalDate
		typ, raw := roundTrip(t, s, in, &out)
		if typ != "text" || raw != str {
			t.Fatalf("stored %s %q, want text %q", typ, raw, str)
		}
		if out != in {
			t.Fatalf("read back %v, want %v", out, in)
		}
	}
}

func TestLocalDateOf(t *testing.T) {
	auckland, err := time.LoadLocation("Pacific/Auckland")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-03T20:00Z is already 2026-10-04 in Auckland (UTC+13).
	instant := time.Date(2026, time.October, 3, 20, 0, 0, 0, time.UTC)
	if got := LocalDateOf(instant).String(); got != "2026-10-03" {
		t.Errorf("LocalDateOf(UTC) = %s, want 2026-10-03", got)
	}
	d := LocalDateOf(instant.In(auckland))
	if got := d.String(); got != "2026-10-04" {
		t.Errorf("LocalDateOf(Auckland) = %s, want 2026-10-04", got)
	}
	if y, m, day := d.Date(); y != 2026 || m != time.October || day != 4 {
		t.Errorf("Date() = %d-%d-%d, want 2026-10-4", y, m, day)
	}
}

func TestLocalDateRejectsInvalid(t *testing.T) {
	for _, str := range []string{
		"", "2026-02-30", "2026-02-29", "2026-13-01", "2026-00-10", "2026-10-00",
		"2026-10-32", "2026-1-03", "2026-10-3", "26-10-03", "2026/10/03",
		"2026-10-03T00:00:00Z", " 2026-10-03", "20261003", "not-a-date",
	} {
		if d, err := ParseLocalDate(str); err == nil {
			t.Errorf("ParseLocalDate(%q) = %v, want error", str, d)
		}
		var d LocalDate
		if err := d.Scan(str); err == nil {
			t.Errorf("Scan(%q) succeeded", str)
		}
	}
	if _, err := (LocalDate{}).Value(); err == nil {
		t.Error("zero LocalDate Value succeeded")
	}
	for _, y := range []int{-1, 10000} {
		d := LocalDateOf(time.Date(y, time.January, 1, 0, 0, 0, 0, time.UTC))
		if _, err := d.Value(); err == nil {
			t.Errorf("LocalDate year %d Value succeeded", y)
		}
	}
	var d LocalDate
	if err := d.Scan(nil); err == nil {
		t.Error("Scan(nil) succeeded")
	}
}
