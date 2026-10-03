package main

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/config"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// useBootMigrations swaps bootMigrations for the test and restores it after.
// Tests that call it must not run in parallel.
func useBootMigrations(t *testing.T, m fstest.MapFS) {
	t.Helper()
	saved := bootMigrations
	bootMigrations = m
	t.Cleanup(func() { bootMigrations = saved })
}

var brokenMigrations = fstest.MapFS{
	"0000_good.sql":   {Data: []byte("CREATE TABLE good (id INTEGER PRIMARY KEY);")},
	"0001_broken.sql": {Data: []byte("CREATE TABLE broken (id INTEGER PRIMARY KEY;")},
}

func TestOpenStoreMigrationFailureStopsBoot(t *testing.T) {
	useBootMigrations(t, brokenMigrations)

	db, err := openStore(context.Background(), t.TempDir())
	if err == nil {
		db.Close()
		t.Fatal("openStore succeeded with a broken migration")
	}
	if db != nil {
		t.Error("openStore returned a store alongside the error")
	}
	if !strings.Contains(err.Error(), "0001_broken.sql") {
		t.Errorf("error does not name the broken migration: %v", err)
	}
}

func TestOpenStoreAppliesEmbeddedMigrations(t *testing.T) {
	ctx := context.Background()
	db, err := openStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	var n int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if n != len(files) {
		t.Errorf("schema_migrations has %d rows, want %d", n, len(files))
	}
}

func TestRunMigrationFailureReturnsBeforeListening(t *testing.T) {
	useBootMigrations(t, brokenMigrations)

	var logs bytes.Buffer
	cfg := config.Config{
		HTTPAddr:        "127.0.0.1:0",
		PublicURL:       &url.URL{Scheme: "http", Host: "localhost"},
		DataDir:         t.TempDir(),
		ShutdownTimeout: time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- run(cfg, slog.New(slog.NewJSONHandler(&logs, nil))) }()

	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "0001_broken.sql") {
			t.Fatalf("run error = %v, want the broken migration's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run kept going after a migration failure")
	}
	if strings.Contains(logs.String(), `"listening"`) {
		t.Errorf("run started listening despite the migration failure: %s", logs.String())
	}
}
