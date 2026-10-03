// Package config loads MealQuest configuration from MEALQUEST_* environment
// variables. Secrets are read only from files named by *_FILE variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secureurl"
)

// Environment variable names.
const (
	EnvHTTPAddr        = "MEALQUEST_HTTP_ADDR"
	EnvPublicURL       = "MEALQUEST_PUBLIC_URL"
	EnvDataDir         = "MEALQUEST_DATA_DIR"
	EnvShutdownTimeout = "MEALQUEST_SHUTDOWN_TIMEOUT"
	EnvSecretKeyFile   = "MEALQUEST_SECRET_KEY_FILE"

	EnvOIDCIssuer           = "MEALQUEST_OIDC_ISSUER"
	EnvOIDCClientID         = "MEALQUEST_OIDC_CLIENT_ID"
	EnvOIDCClientSecretFile = "MEALQUEST_OIDC_CLIENT_SECRET_FILE"
	EnvOIDCAllowedGroups    = "MEALQUEST_OIDC_ALLOWED_GROUPS"

	EnvSessionIdle = "MEALQUEST_SESSION_IDLE"
	EnvSessionMax  = "MEALQUEST_SESSION_MAX"
)

// Session lifetime defaults (D-004): long enough that the kitchen tablet
// stays signed in.
const (
	DefaultSessionIdle = 720 * time.Hour  // 30 days
	DefaultSessionMax  = 4320 * time.Hour // 180 days
)

// Config is the validated service configuration.
type Config struct {
	HTTPAddr        string
	PublicURL       *url.URL
	DataDir         string
	ShutdownTimeout time.Duration
	SecretKey       Secret

	// OIDCIssuer is the Authentik issuer URL used for discovery.
	OIDCIssuer       *url.URL
	OIDCClientID     string
	OIDCClientSecret Secret
	// OIDCAllowedGroups lists the groups whose members may sign in.
	OIDCAllowedGroups []string

	// SessionIdle ends a session left unused this long.
	SessionIdle time.Duration
	// SessionMax ends every session this long after sign-in, used or not.
	SessionMax time.Duration
}

// PublicOrigin returns the scheme and host of PublicURL, the only parts of it
// that are safe to log.
func (c Config) PublicOrigin() string {
	return c.PublicURL.Scheme + "://" + c.PublicURL.Host
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
		PublicURL:       r.secureURL(EnvPublicURL),
		DataDir:         r.dir(EnvDataDir),
		ShutdownTimeout: r.duration(EnvShutdownTimeout),
		SecretKey:       r.secretFile(EnvSecretKeyFile),

		OIDCIssuer:        r.secureURL(EnvOIDCIssuer),
		OIDCClientID:      r.str(EnvOIDCClientID),
		OIDCClientSecret:  r.secretFile(EnvOIDCClientSecretFile),
		OIDCAllowedGroups: r.list(EnvOIDCAllowedGroups),

		SessionIdle: r.sessionDuration(EnvSessionIdle, DefaultSessionIdle),
		SessionMax:  r.sessionDuration(EnvSessionMax, DefaultSessionMax),
	}
	if cfg.SessionIdle > 0 && cfg.SessionMax > 0 && cfg.SessionMax < cfg.SessionIdle {
		r.fail(EnvSessionMax, "must be at least %s (%s)", EnvSessionIdle, cfg.SessionIdle)
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

func (r *reader) str(name string) string {
	v, _ := r.required(name)
	return v
}

// list parses a comma-separated list. Every entry must be non-empty after
// trimming spaces.
func (r *reader) list(name string) []string {
	v, ok := r.required(name)
	if !ok {
		return nil
	}
	items := strings.Split(v, ",")
	for i, item := range items {
		items[i] = strings.TrimSpace(item)
		if items[i] == "" {
			r.fail(name, "empty entry in comma-separated list %q", v)
			return nil
		}
	}
	return items
}

func (r *reader) listenAddr(name string) string {
	v, ok := r.required(name)
	if !ok {
		return ""
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		r.fail(name, "invalid listen address %q (want host:port or :port)", v)
		return ""
	}
	// An empty host listens on all interfaces.
	if host != "" && net.ParseIP(host) == nil && !validHostname(host) {
		r.fail(name, "invalid host %q in listen address (want IP address or hostname)", host)
		return ""
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		r.fail(name, "invalid port %q in listen address (want 0-65535)", port)
		return ""
	}
	return v
}

// validHostname reports whether h is a DNS hostname: dot-separated labels of
// 1-63 letters, digits and hyphens, not starting or ending with a hyphen.
func validHostname(h string) bool {
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// secureURL reads an absolute URL that must pass secureurl.Check: https://,
// or http:// to a loopback host only.
func (r *reader) secureURL(name string) *url.URL {
	v, ok := r.required(name)
	if !ok {
		return nil
	}
	// The value is never echoed: it may carry credentials.
	u, err := url.Parse(v)
	if err != nil {
		r.fail(name, "invalid URL (want absolute https:// URL)")
		return nil
	}
	if err := secureurl.Check(u); err != nil {
		r.fail(name, "invalid URL: %v", err)
		return nil
	}
	if u.User != nil {
		r.fail(name, "URL must not contain user credentials")
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
	return r.parseDuration(name, v)
}

// optionalDuration returns def when name is unset. A set value, even an empty
// one, must be a valid positive duration.
func (r *reader) optionalDuration(name string, def time.Duration) time.Duration {
	v, ok := r.lookup(name)
	if !ok {
		return def
	}
	return r.parseDuration(name, v)
}

// sessionDuration is optionalDuration restricted to whole milliseconds:
// session expiry is stored as a store.Timestamp, which keeps millisecond
// precision, so a finer duration could not be honoured.
func (r *reader) sessionDuration(name string, def time.Duration) time.Duration {
	d := r.optionalDuration(name, def)
	if d%time.Millisecond != 0 {
		r.fail(name, "duration must be a whole number of milliseconds, got %s", d)
		return 0
	}
	return d
}

func (r *reader) parseDuration(name, v string) time.Duration {
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
