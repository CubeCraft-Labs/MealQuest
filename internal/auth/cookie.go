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
	Expires  int64  `json:"e"` // Unix seconds
}

// loginCookie signs and verifies the login cookie with HMAC-SHA256.
type loginCookie struct {
	key []byte
}

func newLoginCookie(secretKey []byte) loginCookie {
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(loginCookieKeyLabel))
	return loginCookie{key: mac.Sum(nil)}
}

func (c loginCookie) sign(payload string) string {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c loginCookie) set(w http.ResponseWriter, s loginState, now time.Time) error {
	s.Expires = now.Add(loginCookieTTL).Unix()
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	http.SetCookie(w, &http.Cookie{
		Name:     loginCookieName,
		Value:    payload + "." + c.sign(payload),
		Path:     "/",
		MaxAge:   int(loginCookieTTL / time.Second),
		Secure:   true,
		HttpOnly: true,
		// Lax so the cookie rides the provider's top-level redirect back.
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (c loginCookie) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     loginCookieName,
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (c loginCookie) read(r *http.Request, now time.Time) (loginState, error) {
	cookie, err := r.Cookie(loginCookieName)
	if err != nil {
		return loginState{}, errors.New("login cookie missing")
	}
	payload, sig, ok := strings.Cut(cookie.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(c.sign(payload))) {
		return loginState{}, errors.New("login cookie signature invalid")
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return loginState{}, fmt.Errorf("login cookie encoding: %w", err)
	}
	var s loginState
	if err := json.Unmarshal(data, &s); err != nil {
		return loginState{}, fmt.Errorf("login cookie payload: %w", err)
	}
	if now.Unix() >= s.Expires {
		return loginState{}, errors.New("login cookie expired")
	}
	return s, nil
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
