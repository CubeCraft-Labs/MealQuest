package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	secretPath := filepath.Join(dir, "session_secret")
	if err := os.WriteFile(secretPath, []byte("s3cret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadablePath := filepath.Join(dir, "missing_secret")
	emptyPath := filepath.Join(dir, "empty_secret")
	if err := os.WriteFile(emptyPath, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	valid := func() map[string]string {
		return map[string]string{
			EnvHTTPAddr:          ":8080",
			EnvPublicURL:         "https://meals.example.com",
			EnvDataDir:           dir,
			EnvShutdownTimeout:   "15s",
			EnvSessionSecretFile: secretPath,
		}
	}
	with := func(changes map[string]string, unset ...string) map[string]string {
		env := valid()
		for k, v := range changes {
			env[k] = v
		}
		for _, k := range unset {
			delete(env, k)
		}
		return env
	}

	tests := []struct {
		name    string
		env     map[string]string
		badVars []string // variables the error must name; nil means success
	}{
		{
			name: "all required vars present",
			env:  valid(),
		},
		{
			name:    "one missing",
			env:     with(nil, EnvHTTPAddr),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "empty value counts as missing",
			env:     with(map[string]string{EnvDataDir: ""}),
			badVars: []string{EnvDataDir},
		},
		{
			name: "all missing are collected",
			env:  map[string]string{},
			badVars: []string{
				EnvHTTPAddr, EnvPublicURL, EnvDataDir, EnvShutdownTimeout, EnvSessionSecretFile,
			},
		},
		{
			name:    "invalid duration",
			env:     with(map[string]string{EnvShutdownTimeout: "ten seconds"}),
			badVars: []string{EnvShutdownTimeout},
		},
		{
			name:    "non-positive duration",
			env:     with(map[string]string{EnvShutdownTimeout: "0s"}),
			badVars: []string{EnvShutdownTimeout},
		},
		{
			name:    "invalid listen address",
			env:     with(map[string]string{EnvHTTPAddr: "8080"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "relative public URL",
			env:     with(map[string]string{EnvPublicURL: "meals.example.com"}),
			badVars: []string{EnvPublicURL},
		},
		{
			name:    "data dir does not exist",
			env:     with(map[string]string{EnvDataDir: filepath.Join(dir, "nope")}),
			badVars: []string{EnvDataDir},
		},
		{
			name:    "unreadable secret file",
			env:     with(map[string]string{EnvSessionSecretFile: unreadablePath}),
			badVars: []string{EnvSessionSecretFile},
		},
		{
			name:    "secret file path is a directory",
			env:     with(map[string]string{EnvSessionSecretFile: dir}),
			badVars: []string{EnvSessionSecretFile},
		},
		{
			name:    "empty secret file",
			env:     with(map[string]string{EnvSessionSecretFile: emptyPath}),
			badVars: []string{EnvSessionSecretFile},
		},
		{
			name:    "inline secret rejected",
			env:     with(map[string]string{"MEALQUEST_SESSION_SECRET": "inline"}),
			badVars: []string{"MEALQUEST_SESSION_SECRET"},
		},
		{
			name: "several invalid values are all named",
			env: with(map[string]string{
				EnvShutdownTimeout:   "soon",
				EnvSessionSecretFile: unreadablePath,
			}, EnvPublicURL),
			badVars: []string{EnvPublicURL, EnvShutdownTimeout, EnvSessionSecretFile},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lookup := func(k string) (string, bool) {
				v, ok := tc.env[k]
				return v, ok
			}
			cfg, err := load(lookup)

			if tc.badVars == nil {
				if err != nil {
					t.Fatalf("load() error = %v, want nil", err)
				}
				return
			}

			var cerr *Error
			if !errors.As(err, &cerr) {
				t.Fatalf("load() error = %v, want *Error", err)
			}
			var got []string
			for _, p := range cerr.Problems {
				got = append(got, p.Var)
			}
			if !slices.Equal(got, tc.badVars) {
				t.Errorf("problem vars = %v, want %v", got, tc.badVars)
			}
			for _, v := range tc.badVars {
				if !strings.Contains(err.Error(), v) {
					t.Errorf("error message %q does not name %s", err.Error(), v)
				}
			}
			if cfg != (Config{}) {
				t.Errorf("load() returned non-zero Config alongside error: %+v", cfg)
			}
		})
	}
}

func TestLoadValues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	secretPath := filepath.Join(dir, "session_secret")
	if err := os.WriteFile(secretPath, []byte("s3cret-value\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		EnvHTTPAddr:          "127.0.0.1:9000",
		EnvPublicURL:         "http://localhost:9000",
		EnvDataDir:           dir,
		EnvShutdownTimeout:   "1m30s",
		EnvSessionSecretFile: secretPath,
	}
	cfg, err := load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9000" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.PublicURL.String() != "http://localhost:9000" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
	if cfg.DataDir != dir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, dir)
	}
	if cfg.ShutdownTimeout != 90*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 1m30s", cfg.ShutdownTimeout)
	}
	if cfg.SessionSecret.Reveal() != "s3cret-value" {
		t.Errorf("SessionSecret.Reveal() = %q, want trailing newline trimmed", cfg.SessionSecret.Reveal())
	}
}

func TestSecretRedacted(t *testing.T) {
	t.Parallel()

	s := Secret{value: "s3cret-value"}
	for _, format := range []string{"%v", "%s", "%+v", "%#v"} {
		cfg := Config{SessionSecret: s}
		for _, out := range []string{fmt.Sprintf(format, s), fmt.Sprintf(format, cfg)} {
			if strings.Contains(out, "s3cret-value") {
				t.Errorf("format %s leaked secret: %s", format, out)
			}
		}
	}
}
