// Package config loads MealQuest configuration from MEALQUEST_* environment
// variables. Secrets are read only from files named by *_FILE variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvHTTPAddr          = "MEALQUEST_HTTP_ADDR"
	EnvPublicURL         = "MEALQUEST_PUBLIC_URL"
	EnvDataDir           = "MEALQUEST_DATA_DIR"
	EnvShutdownTimeout   = "MEALQUEST_SHUTDOWN_TIMEOUT"
	EnvSessionSecretFile = "MEALQUEST_SESSION_SECRET_FILE"
)

// Config is the validated service configuration.
type Config struct {
	HTTPAddr        string
	PublicURL       *url.URL
	DataDir         string
	ShutdownTimeout time.Duration
	SessionSecret   Secret
}

// Load reads and validates configuration from the process environment.
// It reports every missing or invalid variable in a single *Error.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	r := reader{lookup: lookup}
	cfg := Config{
		HTTPAddr:        r.listenAddr(EnvHTTPAddr),
		PublicURL:       r.httpURL(EnvPublicURL),
		DataDir:         r.dir(EnvDataDir),
		ShutdownTimeout: r.duration(EnvShutdownTimeout),
		SessionSecret:   r.secretFile(EnvSessionSecretFile),
	}
	if len(r.problems) > 0 {
		return Config{}, &Error{Problems: r.problems}
	}
	return cfg, nil
}

// Problem describes one missing or invalid variable.
type Problem struct {
	Var    string
	Reason string
}

// Error lists every configuration problem found by Load.
type Error struct {
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("invalid configuration:")
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  %s: %s", p.Var, p.Reason)
	}
	return b.String()
}

// Secret holds a sensitive value. It never prints its contents.
type Secret struct {
	value string
}

// Reveal returns the secret value. Call it only where the value is used.
func (s Secret) Reveal() string { return s.value }

// String redacts the secret so it cannot leak through fmt or logs.
func (s Secret) String() string { return "[REDACTED]" }

// GoString redacts the secret for %#v.
func (s Secret) GoString() string { return "[REDACTED]" }

// reader reads variables and collects every problem instead of stopping at
// the first one.
type reader struct {
	lookup   func(string) (string, bool)
	problems []Problem
}

func (r *reader) fail(name, format string, args ...any) {
	r.problems = append(r.problems, Problem{Var: name, Reason: fmt.Sprintf(format, args...)})
}

// required returns the variable's value, recording a problem when it is
// unset or empty.
func (r *reader) required(name string) (string, bool) {
	v, ok := r.lookup(name)
	if !ok || strings.TrimSpace(v) == "" {
		r.fail(name, "required but not set")
		return "", false
	}
	return v, true
}

func (r *reader) listenAddr(name string) string {
	v, ok := r.required(name)
	if !ok {
		return ""
	}
	if _, _, err := net.SplitHostPort(v); err != nil {
		r.fail(name, "invalid listen address %q (want host:port or :port)", v)
		return ""
	}
	return v
}

func (r *reader) httpURL(name string) *url.URL {
	v, ok := r.required(name)
	if !ok {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		r.fail(name, "invalid URL %q (want absolute http:// or https:// URL)", v)
		return nil
	}
	return u
}

func (r *reader) dir(name string) string {
	v, ok := r.required(name)
	if !ok {
		return ""
	}
	info, err := os.Stat(v)
	if err != nil {
		r.fail(name, "cannot access directory %q: %v", v, err)
		return ""
	}
	if !info.IsDir() {
		r.fail(name, "%q is not a directory", v)
		return ""
	}
	return v
}

func (r *reader) duration(name string) time.Duration {
	v, ok := r.required(name)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail(name, "invalid duration %q (want e.g. 10s, 1m)", v)
		return 0
	}
	if d <= 0 {
		r.fail(name, "duration must be positive, got %q", v)
		return 0
	}
	return d
}

// secretFile reads a secret from the file named by name, which must end in
// _FILE. Setting the secret inline (name without _FILE) is rejected.
func (r *reader) secretFile(name string) Secret {
	inline := strings.TrimSuffix(name, "_FILE")
	if _, set := r.lookup(inline); set {
		r.fail(inline, "secrets must not be set inline; use %s", name)
	}
	path, ok := r.required(name)
	if !ok {
		return Secret{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Report the cause without echoing any file contents.
		r.fail(name, "cannot read secret file %q: %v", path, unwrapPathError(err))
		return Secret{}
	}
	value := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(value) == "" {
		r.fail(name, "secret file %q is empty", path)
		return Secret{}
	}
	return Secret{value: value}
}

func unwrapPathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
