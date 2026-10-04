package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/config"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
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

// loadConfig loads a configuration through config.Load whose secret key file
// holds keyFile. Tests that call it must not run in parallel.
func loadConfig(t *testing.T, keyFile string) config.Config {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "secret_key")
	if err := os.WriteFile(keyPath, []byte(keyFile), 0o600); err != nil {
		t.Fatal(err)
	}
	clientSecretPath := filepath.Join(dir, "oidc_client_secret")
	if err := os.WriteFile(clientSecretPath, []byte("client-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHTTPAddr, "127.0.0.1:0")
	t.Setenv(config.EnvPublicURL, "http://localhost")
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvShutdownTimeout, "1s")
	t.Setenv(config.EnvSecretKeyFile, keyPath)
	t.Setenv(config.EnvOIDCIssuer, "https://auth.example.com/application/o/mealquest/")
	t.Setenv(config.EnvOIDCClientID, "mealquest")
	t.Setenv(config.EnvOIDCClientSecretFile, clientSecretPath)
	t.Setenv(config.EnvOIDCAllowedGroups, "mealquest-admin")
	// MEALQUEST_DEFAULT_TZ is left unset; t.Setenv restores any outer value.
	t.Setenv(config.EnvDefaultTZ, "")
	if err := os.Unsetenv(config.EnvDefaultTZ); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// validKeyFile returns secret key file contents as written by the README:
// base64 of 32 random bytes followed by a newline.
func validKeyFile(t *testing.T) string {
	t.Helper()
	key := make([]byte, secretbox.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key) + "\n"
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
	cfg := loadConfig(t, validKeyFile(t))
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

func TestOpenSecretBoxAcceptsReadmeKey(t *testing.T) {
	cfg := loadConfig(t, validKeyFile(t))
	box, err := openSecretBox(cfg.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	token, err := box.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Decrypt(token); err != nil {
		t.Fatal(err)
	}
}

func TestRunBadSecretKeyStopsBoot(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))
	tests := []struct {
		name    string
		keyFile string
		want    error
	}{
		{"not base64", "this is not a base64 key!\n", secretbox.ErrKeyEncoding},
		{"wrong length", short + "\n", secretbox.ErrKeyLength},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := loadConfig(t, tt.keyFile)
			var logs bytes.Buffer
			errc := make(chan error, 1)
			go func() { errc <- run(cfg, slog.New(slog.NewJSONHandler(&logs, nil))) }()

			var err error
			select {
			case err = <-errc:
			case <-time.After(10 * time.Second):
				t.Fatal("run kept going with a bad secret key")
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("run error = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), config.EnvSecretKeyFile) {
				t.Errorf("error does not name %s: %v", config.EnvSecretKeyFile, err)
			}
			if key := strings.TrimSpace(tt.keyFile); strings.Contains(err.Error(), key) {
				t.Errorf("error leaks the key: %v", err)
			}
			if strings.Contains(logs.String(), `"listening"`) {
				t.Errorf("run started listening despite the bad key: %s", logs.String())
			}
			if _, statErr := os.Stat(filepath.Join(cfg.DataDir, store.FileName)); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("run opened the database before rejecting the key (stat: %v)", statErr)
			}
		})
	}
}

func TestLoadMissingSecretKeyFileStopsBoot(t *testing.T) {
	loadConfig(t, validKeyFile(t)) // sets every other variable
	t.Setenv(config.EnvSecretKeyFile, filepath.Join(t.TempDir(), "missing"))
	_, err := config.Load()
	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) || !strings.Contains(err.Error(), config.EnvSecretKeyFile) {
		t.Fatalf("Load error = %v, want a config error naming %s", err, config.EnvSecretKeyFile)
	}
}

func TestRunWithoutValidDefaultZoneOnFirstBootStops(t *testing.T) {
	tests := []struct {
		name      string
		defaultTZ *string // nil leaves MEALQUEST_DEFAULT_TZ unset
		want      error
	}{
		{"unset", nil, household.ErrNoDefaultZone},
		{"not an IANA zone", ptr("Mars/Olympus_Mons"), household.ErrInvalidDefaultZone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadConfig(t, validKeyFile(t))
			if tt.defaultTZ != nil {
				t.Setenv(config.EnvDefaultTZ, *tt.defaultTZ)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			errc := make(chan error, 1)
			go func() { errc <- run(cfg, slog.New(slog.NewJSONHandler(&logs, nil))) }()

			select {
			case err = <-errc:
			case <-time.After(10 * time.Second):
				t.Fatal("run kept going with no household settings and no valid default zone")
			}
			if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), config.EnvDefaultTZ) {
				t.Fatalf("run error = %v, want %v naming %s", err, tt.want, config.EnvDefaultTZ)
			}
			if strings.Contains(logs.String(), `"listening"`) {
				t.Errorf("run started listening without household settings: %s", logs.String())
			}
		})
	}
}

// A reboot over existing household settings must not depend on
// MEALQUEST_DEFAULT_TZ: config.Load accepts an invalid value and seeding
// ignores it, keeping the stored zone.
func TestRebootWithSettingsIgnoresInvalidDefaultZone(t *testing.T) {
	ctx := context.Background()
	cfg := loadConfig(t, validKeyFile(t))

	db, err := openStore(ctx, cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedSettings(ctx, db, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv(config.EnvDefaultTZ, "Mars/Olympus_Mons")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load with an invalid %s and existing settings: %v", config.EnvDefaultTZ, err)
	}
	db, err = openStore(ctx, cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := seedSettings(ctx, db, cfg.DefaultTZ); err != nil {
		t.Fatalf("reboot with %s=%q: %v", config.EnvDefaultTZ, cfg.DefaultTZ, err)
	}
	got, err := household.NewSettingsService(db).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != "America/Denver" {
		t.Errorf("timezone after reboot = %q, want America/Denver", got.Timezone)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSeedSettingsAtBoot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// First boot seeds from the default zone.
	db, err := openStore(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedSettings(ctx, db, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A later boot needs no default zone and keeps the stored one.
	db, err = openStore(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := seedSettings(ctx, db, ""); err != nil {
		t.Fatalf("reboot without %s: %v", config.EnvDefaultTZ, err)
	}
	got, err := household.NewSettingsService(db).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Timezone != "America/Denver" {
		t.Errorf("timezone after reboot = %q, want America/Denver", got.Timezone)
	}
}

func TestSeedDefaultsAtBoot(t *testing.T) {
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
	if err := seedDefaults(ctx, db, "America/Denver"); err != nil {
		t.Fatal(err)
	}

	if _, err := household.NewSettingsService(db).Get(ctx); err != nil {
		t.Errorf("household settings: %v", err)
	}
	var provider string
	if err := db.Reader.QueryRowContext(ctx, `SELECT provider FROM ai_settings`).Scan(&provider); err != nil {
		t.Errorf("AI settings: %v", err)
	} else if provider != household.SeedAIProvider {
		t.Errorf("AI provider = %q, want %q", provider, household.SeedAIProvider)
	}
	var prefs int
	if err := db.Reader.QueryRowContext(ctx, `SELECT count(*) FROM notification_prefs`).Scan(&prefs); err != nil {
		t.Fatal(err)
	}
	if prefs != 5 {
		t.Errorf("%d notification preferences, want 5", prefs)
	}

	// A second boot seeds nothing new and succeeds.
	if err := seedDefaults(ctx, db, ""); err != nil {
		t.Fatalf("reboot: %v", err)
	}
}
