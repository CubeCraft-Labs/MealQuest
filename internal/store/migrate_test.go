package store

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

var testMigrations = fstest.MapFS{
	"0001_first.sql":  {Data: []byte("CREATE TABLE first (id INTEGER PRIMARY KEY);")},
	"0002_second.sql": {Data: []byte("CREATE TABLE second (id INTEGER PRIMARY KEY);\nCREATE TABLE third (id INTEGER PRIMARY KEY);")},
	"README.md":       {Data: []byte("not a migration")},
}

type migrationRow struct {
	version   string
	appliedAt string
}

func migrationRows(t *testing.T, s *Store) []migrationRow {
	t.Helper()
	rows, err := s.Reader.Query("SELECT version, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []migrationRow
	for rows.Next() {
		var r migrationRow
		if err := rows.Scan(&r.version, &r.appliedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func versions(rows []migrationRow) string {
	vs := make([]string, len(rows))
	for i, r := range rows {
		vs[i] = r.version
	}
	return strings.Join(vs, ",")
}

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.Reader.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigrateFreshDatabaseReachesLatest(t *testing.T) {
	s := openTestStore(t)
	if err := Migrate(context.Background(), s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}
	rows := migrationRows(t, s)
	if got := versions(rows); got != "0001_first,0002_second" {
		t.Fatalf("versions = %q", got)
	}
	for _, r := range rows {
		if _, err := time.Parse(time.RFC3339, r.appliedAt); err != nil || !strings.HasSuffix(r.appliedAt, "Z") {
			t.Errorf("applied_at %q is not UTC RFC 3339", r.appliedAt)
		}
	}
	for _, table := range []string{"first", "second", "third"} {
		if !tableExists(t, s, table) {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestMigrateSecondRunIsNoOp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}
	before := migrationRows(t, s)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatalf("second run: %v", err)
	}
	after := migrationRows(t, s)
	if len(after) != len(before) {
		t.Fatalf("rows after second run = %v, want %v", after, before)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("row %d changed: %v -> %v", i, before[i], after[i])
		}
	}
}

func TestMigrateBrokenMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}

	broken := fstest.MapFS{"0003_broken.sql": {Data: []byte(
		// The first statement succeeds; the second is invalid, so the
		// table created by the first must be rolled back with it.
		"CREATE TABLE fourth (id INTEGER PRIMARY KEY);\nCREATE TABLE oops (;",
	)}}
	for name, f := range testMigrations {
		broken[name] = f
	}
	err := Migrate(ctx, s.Writer, broken)
	if err == nil {
		t.Fatal("broken migration returned no error")
	}
	if !strings.Contains(err.Error(), "0003_broken.sql") {
		t.Errorf("error %q does not name the failing migration", err)
	}
	if got := versions(migrationRows(t, s)); got != "0001_first,0002_second" {
		t.Errorf("versions after failure = %q, want previous version intact", got)
	}
	if tableExists(t, s, "fourth") {
		t.Error("table from the failed migration was not rolled back")
	}
	for _, table := range []string{"first", "second", "third"} {
		if !tableExists(t, s, table) {
			t.Errorf("previous table %s missing after failed migration", table)
		}
	}
}

func TestMigrateAppliesInFilenameOrder(t *testing.T) {
	s := openTestStore(t)
	// 0002 depends on 0001; MapFS iteration order is random, so this only
	// passes if migrations are sorted by name.
	ordered := fstest.MapFS{
		"0002_child.sql":  {Data: []byte("CREATE TABLE child (parent_id INTEGER REFERENCES parent(id)); INSERT INTO child VALUES (1);")},
		"0001_parent.sql": {Data: []byte("CREATE TABLE parent (id INTEGER PRIMARY KEY); INSERT INTO parent VALUES (1);")},
	}
	if err := Migrate(context.Background(), s.Writer, ordered); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateUnknownAppliedVersionFails(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}
	older := fstest.MapFS{"0001_first.sql": testMigrations["0001_first.sql"]}
	if err := Migrate(ctx, s.Writer, older); err == nil {
		t.Fatal("migrating a database newer than the build returned no error")
	}
}

func TestMigrateNoMigrationsFails(t *testing.T) {
	s := openTestStore(t)
	if err := Migrate(context.Background(), s.Writer, fstest.MapFS{}); err == nil {
		t.Fatal("empty migrations returned no error")
	}
}

func TestMigrateEmbeddedMigrations(t *testing.T) {
	s := openTestStore(t)
	if err := Migrate(context.Background(), s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if got := versions(migrationRows(t, s)); !strings.HasPrefix(got, "0001_init") {
		t.Errorf("versions = %q, want 0001_init first", got)
	}
}
