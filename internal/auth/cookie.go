package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	loginCookieName = "__Host-mealquest_login"
	loginCookieTTL  = 10 * time.Minute
	// loginCookieKeyLabel separates the login cookie's signing key from other
	// keys derived from the same server secret.
	loginCookieKeyLabel = "mealquest auth login cookie v1"
)

// loginState travels from login to callback in the signed login cookie.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r"`
	FlowExpiry
}

// loginCookie is the FlowCookie carrying loginState.
type loginCookie struct {
	flow FlowCookie
}

func newLoginCookie(secretKey []byte) loginCookie {
	return loginCookie{flow: NewFlowCookie(loginCookieName, loginCookieKeyLabel, loginCookieTTL, secretKey)}
}

func (c loginCookie) set(w http.ResponseWriter, s loginState, now time.Time) error {
	return c.flow.Set(w, &s, now)
}

func (c loginCookie) clear(w http.ResponseWriter) { c.flow.Clear(w) }

func (c loginCookie) read(r *http.Request, now time.Time) (loginState, error) {
	var s loginState
	if err := c.flow.Read(r, &s, now); err != nil {
		return loginState{}, err
	}
	return s, nil
}

// FlowExpiry is embedded in every value a FlowCookie carries; Set fills it
// and Read enforces it.
type FlowExpiry struct {
	Expires int64 `json:"e"` // Unix seconds
}

func (e *FlowExpiry) flowExpiry() *FlowExpiry { return e }

// flowValue is a pointer to a struct that embeds FlowExpiry.
type flowValue interface {
	flowExpiry() *FlowExpiry
}

// FlowCookie carries a redirect flow's state — the OAuth state, PKCE
// verifier and the like — from the request that sends the browser to a
// provider to the callback it returns to. The value is JSON, signed with
// HMAC-SHA256 under a key derived from the server secret and the cookie's
// own label, and expires after a fixed time. It is not encrypted: it must
// not hold anything the browser may not see.
type FlowCookie struct {
	name string
	ttl  time.Duration
	key  []byte
}

// NewFlowCookie returns the flow cookie name, valid for ttl, signed under a
// key derived from secretKey and keyLabel. Each flow uses its own keyLabel so
// one flow's cookie never verifies as another's. name must carry the __Host-
// prefix: the cookie is Secure, host-only and Path=/.
func NewFlowCookie(name, keyLabel string, ttl time.Duration, secretKey []byte) FlowCookie {
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(keyLabel))
	return FlowCookie{name: name, ttl: ttl, key: mac.Sum(nil)}
}

func (c FlowCookie) sign(payload string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Set stores v, which expires ttl after now, in the cookie.
func (c FlowCookie) Set(w http.ResponseWriter, v flowValue, now time.Time) error {
	v.flowExpiry().Expires = now.Add(c.ttl).Unix()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	http.SetCookie(w, &http.Cookie{
		Name:     c.name,
		Value:    payload + "." + c.sign(payload),
		Path:     "/",
		MaxAge:   int(c.ttl / time.Second),
		Secure:   true,
		HttpOnly: true,
		// Lax so the cookie rides the provider's top-level redirect back.
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Clear asks the browser to discard the cookie. A copy of it still verifies
// until it expires, so a flow that must be single-use also consumes its
// state on the server.
func (c FlowCookie) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.name,
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Read verifies the cookie r carries and decodes it into v. It fails when
// the cookie is missing, its signature is invalid or it has expired at now.
func (c FlowCookie) Read(r *http.Request, v flowValue, now time.Time) error {
	cookie, err := r.Cookie(c.name)
	if err != nil {
		return fmt.Errorf("%s cookie missing", c.name)
	}
	payload, sig, ok := strings.Cut(cookie.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(c.sign(payload))) {
		return fmt.Errorf("%s cookie signature invalid", c.name)
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return fmt.Errorf("%s cookie encoding: %w", c.name, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s cookie payload: %w", c.name, err)
	}
	if now.Unix() >= v.flowExpiry().Expires {
		return fmt.Errorf("%s cookie expired", c.name)
	}
	return nil
}

// maxPendingStates bounds each flow's pending states. The login route is
// unauthenticated, so without a bound anyone could grow the set without
// limit; one household never has close to this many flows in progress.
const maxPendingStates = 10000

// ErrTooManyPending means a flow's pending states are at maxPendingStates,
// so a new flow is refused until older ones expire.
var ErrTooManyPending = errors.New("too many sign-in flows in progress")

// PendingStates holds the state of every redirect flow started and not yet
// completed, until its flow cookie expires. A copy of a flow cookie verifies
// until it expires, so a flow is single-use only because its callback
// consumes the state here. The states live in memory — MealQuest runs as one
// process — so a flow started before a restart fails closed after it.
type PendingStates struct {
	ttl    time.Duration
	mu     sync.Mutex
	states map[string]time.Time
	// order is every added state in the order added. Every state is pending
	// for the same ttl, so it is also expiry order and Add forgets expired
	// states from its front.
	order []pendingState
}

type pendingState struct {
	state   string
	expires time.Time
}

// NewPendingStates returns an empty set whose states are pending for ttl,
// the flow cookie's lifetime.
func NewPendingStates(ttl time.Duration) *PendingStates {
	return &PendingStates{ttl: ttl, states: map[string]time.Time{}}
}

// Add records state, started at now, as pending and forgets expired ones.
// It fails with ErrTooManyPending when maxPendingStates are pending.
func (p *PendingStates) Add(state string, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	expired := 0
	for expired < len(p.order) && !now.Before(p.order[expired].expires) {
		delete(p.states, p.order[expired].state)
		expired++
	}
	p.order = p.order[expired:]
	if len(p.states) >= maxPendingStates {
		return ErrTooManyPending
	}
	expires := now.Add(p.ttl)
	p.states[state] = expires
	p.order = append(p.order, pendingState{state: state, expires: expires})
	return nil
}

// Consume removes state from the pending states, reporting whether it was
// there.
func (p *PendingStates) Consume(state string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.states[state]
	delete(p.states, state)
	return ok
}

// returnPath validates the optional post-login return path. Empty means "/".
// Only same-origin absolute paths are allowed: no scheme, host, protocol-
// relative "//" prefix, backslashes or control characters.
func returnPath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsRune(raw, '\\') {
		return "", errors.New("return path must be a same-origin path")
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return "", errors.New("return path contains a control character")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "", errors.New("return path must be a same-origin path")
	}
	return raw, nil
}
