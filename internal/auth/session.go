package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// SessionCookieName is the session cookie. The __Host- prefix makes the
// browser insist on Secure, Path=/ and no Domain.
const SessionCookieName = "__Host-mealquest_session"

// sessionIDBytes is the size of the random session ID: 256 bits.
const sessionIDBytes = 32

// ErrNoSession means the request carries no live session: the cookie is
// missing or malformed, or its session is unknown, ended or expired.
var ErrNoSession = errors.New("auth: no valid session")

// Session is a signed-in household member's server-side session.
type Session struct {
	Subject   string
	Email     string
	Name      string
	Groups    []string
	CSRFToken string
	CreatedAt time.Time
	// LastSeenAt is the last time the session was used.
	LastSeenAt time.Time
	// ExpiresAt is when the session ends unless used again first.
	ExpiresAt time.Time
}

// SessionManager keeps sessions in the sessions table. The cookie holds only
// the random session ID; the table holds only its SHA-256 hash. A session
// ends after idle without use and always max after it was created.
type SessionManager struct {
	db   *store.Store
	idle time.Duration
	max  time.Duration
	// now is the clock; tests replace it.
	now func() time.Time
}

// NewSessionManager returns a SessionManager over db. idle and max must be
// positive with max >= idle.
func NewSessionManager(db *store.Store, idle, max time.Duration) (*SessionManager, error) {
	switch {
	case db == nil:
		return nil, errors.New("auth: session store is required")
	case idle <= 0 || max <= 0:
		return nil, errors.New("auth: session idle and max lifetimes must be positive")
	case max < idle:
		return nil, errors.New("auth: session max lifetime must be at least the idle lifetime")
	}
	return &SessionManager{db: db, idle: idle, max: max, now: time.Now}, nil
}

// StartSession creates a session for claims and sets the session cookie.
func (m *SessionManager) StartSession(w http.ResponseWriter, r *http.Request, claims Claims) error {
	if claims.Subject == "" {
		return errors.New("auth: session needs a subject")
	}
	id, csrf := randomToken(), randomToken()
	groups := claims.Groups
	if groups == nil {
		groups = []string{}
	}
	groupsJSON, err := json.Marshal(groups)
	if err != nil {
		return fmt.Errorf("auth: encode session groups: %w", err)
	}

	now := m.timestamp()
	if _, err := m.db.Writer.ExecContext(r.Context(), `
		INSERT INTO sessions (id_hash, subject, email, name, groups, csrf_token, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hashSessionID(id), claims.Subject, claims.Email, claims.Name, string(groupsJSON), csrf,
		now, now, m.expiry(now.Time, now.Time),
	); err != nil {
		return fmt.Errorf("auth: create session: %w", err)
	}

	// The cookie outlives idle gaps so a returning device presents its ID;
	// the server decides whether the session is still live.
	http.SetCookie(w, sessionCookie(id, int(m.max/time.Second)))
	return nil
}

// SessionFor returns the live session r carries and slides its last use to
// now. It returns ErrNoSession when there is none; an expired session is
// deleted.
func (m *SessionManager) SessionFor(r *http.Request) (Session, error) {
	idHash, err := sessionIDHash(r)
	if err != nil {
		return Session{}, err
	}
	now := m.timestamp()

	var s Session
	live := false
	err = m.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		var groupsJSON string
		var created, lastSeen, expires store.Timestamp
		err := tx.QueryRowContext(r.Context(), `
			SELECT subject, email, name, groups, csrf_token, created_at, last_seen_at, expires_at
			FROM sessions WHERE id_hash = ?`, idHash,
		).Scan(&s.Subject, &s.Email, &s.Name, &groupsJSON, &s.CSRFToken, &created, &lastSeen, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("auth: read session: %w", err)
		}

		if !now.Before(expires.Time) || now.Sub(lastSeen.Time) >= m.idle || now.Sub(created.Time) >= m.max {
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE id_hash = ?`, idHash); err != nil {
				return fmt.Errorf("auth: delete expired session: %w", err)
			}
			return nil
		}

		if err := json.Unmarshal([]byte(groupsJSON), &s.Groups); err != nil {
			return fmt.Errorf("auth: decode session groups: %w", err)
		}
		expiresAt := m.expiry(created.Time, now.Time)
		if _, err := tx.ExecContext(r.Context(), `
			UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id_hash = ?`,
			now, expiresAt, idHash,
		); err != nil {
			return fmt.Errorf("auth: slide session: %w", err)
		}
		s.CreatedAt, s.LastSeenAt, s.ExpiresAt = created.Time, now.Time, expiresAt.Time
		live = true
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	if !live {
		return Session{}, ErrNoSession
	}
	return s, nil
}

// EndSession deletes the session r carries, if any, and clears the cookie.
func (m *SessionManager) EndSession(w http.ResponseWriter, r *http.Request) error {
	http.SetCookie(w, sessionCookie("", -1))
	idHash, err := sessionIDHash(r)
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := m.db.Writer.ExecContext(r.Context(), `DELETE FROM sessions WHERE id_hash = ?`, idHash); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// timestamp is the clock's current instant at stored precision.
func (m *SessionManager) timestamp() store.Timestamp {
	return store.Timestamp{Time: m.now().UTC().Truncate(time.Millisecond)}
}

// expiry is when a session created at created and last used at lastSeen
// ends: idle after lastSeen, but never later than max after created.
func (m *SessionManager) expiry(created, lastSeen time.Time) store.Timestamp {
	idle, absolute := lastSeen.Add(m.idle), created.Add(m.max)
	if absolute.Before(idle) {
		return store.Timestamp{Time: absolute}
	}
	return store.Timestamp{Time: idle}
}

// sessionCookie is the session cookie carrying value, with maxAge seconds;
// a negative maxAge clears it.
func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// sessionIDHash returns the stored hash of r's session ID, or ErrNoSession
// when the cookie is missing or is not a well-formed ID.
func sessionIDHash(r *http.Request) (string, error) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return "", ErrNoSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != sessionIDBytes {
		return "", ErrNoSession
	}
	return hashSessionID(c.Value), nil
}

// hashSessionID is the SHA-256 hex digest of the cookie value.
func hashSessionID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// randomToken returns 256 random bits, base64url-encoded without padding.
// crypto/rand.Read never returns an error; it crashes the program instead.
func randomToken() string {
	b := make([]byte, sessionIDBytes)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
