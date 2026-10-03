package config

import (
	"errors"
	"fmt"
	"net/url"
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
	secretPath := filepath.Join(dir, "secret_key")
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
			EnvHTTPAddr:        ":8080",
			EnvPublicURL:       "https://meals.example.com",
			EnvDataDir:         dir,
			EnvShutdownTimeout: "15s",
			EnvSecretKeyFile:   secretPath,
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
				EnvHTTPAddr, EnvPublicURL, EnvDataDir, EnvShutdownTimeout, EnvSecretKeyFile,
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
			name:    "non-numeric port",
			env:     with(map[string]string{EnvHTTPAddr: ":notaport"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "empty port",
			env:     with(map[string]string{EnvHTTPAddr: "127.0.0.1:"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "port out of range",
			env:     with(map[string]string{EnvHTTPAddr: ":65536"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name: "listen address with IP host",
			env:  with(map[string]string{EnvHTTPAddr: "127.0.0.1:8080"}),
		},
		{
			name: "listen address with IPv6 host",
			env:  with(map[string]string{EnvHTTPAddr: "[::1]:8080"}),
		},
		{
			name: "listen address with hostname",
			env:  with(map[string]string{EnvHTTPAddr: "localhost:8080"}),
		},
		{
			name:    "listen host with space",
			env:     with(map[string]string{EnvHTTPAddr: "bad host:8080"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "listen host with leading hyphen label",
			env:     with(map[string]string{EnvHTTPAddr: "-bad.example.com:8080"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "listen host with empty label",
			env:     with(map[string]string{EnvHTTPAddr: "bad..example.com:8080"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "listen host with label over 63 chars",
			env:     with(map[string]string{EnvHTTPAddr: strings.Repeat("a", 64) + ".example.com:8080"}),
			badVars: []string{EnvHTTPAddr},
		},
		{
			name:    "public URL without hostname",
			env:     with(map[string]string{EnvPublicURL: "http://:8080"}),
			badVars: []string{EnvPublicURL},
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
			env:     with(map[string]string{EnvSecretKeyFile: unreadablePath}),
			badVars: []string{EnvSecretKeyFile},
		},
		{
			name:    "secret file path is a directory",
			env:     with(map[string]string{EnvSecretKeyFile: dir}),
			badVars: []string{EnvSecretKeyFile},
		},
		{
			name:    "empty secret file",
			env:     with(map[string]string{EnvSecretKeyFile: emptyPath}),
			badVars: []string{EnvSecretKeyFile},
		},
		{
			name:    "inline secret rejected",
			env:     with(map[string]string{"MEALQUEST_SECRET_KEY": "inline"}),
			badVars: []string{"MEALQUEST_SECRET_KEY"},
		},
		{
			name: "several invalid values are all named",
			env: with(map[string]string{
				EnvShutdownTimeout: "soon",
				EnvSecretKeyFile:   unreadablePath,
			}, EnvPublicURL),
			badVars: []string{EnvPublicURL, EnvShutdownTimeout, EnvSecretKeyFile},
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
	secretPath := filepath.Join(dir, "secret_key")
	if err := os.WriteFile(secretPath, []byte("s3cret-value\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		EnvHTTPAddr:        "127.0.0.1:9000",
		EnvPublicURL:       "http://localhost:9000",
		EnvDataDir:         dir,
		EnvShutdownTimeout: "1m30s",
		EnvSecretKeyFile:   secretPath,
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
	if cfg.SecretKey.Reveal() != "s3cret-value" {
		t.Errorf("SecretKey.Reveal() = %q, want trailing newline trimmed", cfg.SecretKey.Reveal())
	}
}

func TestSecretRedacted(t *testing.T) {
	t.Parallel()

	s := Secret{value: "s3cret-value"}
	for _, format := range []string{"%v", "%s", "%+v", "%#v"} {
		cfg := Config{SecretKey: s}
		for _, out := range []string{fmt.Sprintf(format, s), fmt.Sprintf(format, cfg)} {
			if strings.Contains(out, "s3cret-value") {
				t.Errorf("format %s leaked secret: %s", format, out)
			}
		}
	}
}

func TestPublicURLWithCredentialsRejected(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://alice:hunter2@example.com",
		"https://alice@example.com",
		"https://alice:hunter2@[bad",
	} {
		env := map[string]string{EnvPublicURL: raw}
		_, err := load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })

		var cerr *Error
		if !errors.As(err, &cerr) || !slices.ContainsFunc(cerr.Problems, func(p Problem) bool { return p.Var == EnvPublicURL }) {
			t.Errorf("load(%q) error = %v, want problem for %s", raw, err, EnvPublicURL)
		}
		if err != nil && (strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "alice")) {
			t.Errorf("load(%q) error leaks credentials: %v", raw, err)
		}
	}
}

func TestPublicOrigin(t *testing.T) {
	t.Parallel()

	u, err := url.Parse("https://alice:hunter2@meals.example.com:8443/app?token=x#frag")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := (Config{PublicURL: u}).PublicOrigin(), "https://meals.example.com:8443"; got != want {
		t.Errorf("PublicOrigin() = %q, want %q", got, want)
	}
}
