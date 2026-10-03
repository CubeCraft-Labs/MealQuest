package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// signInWith completes a login whose ID token has claims edited by edit.
func (h *harness) signInWith(t *testing.T, edit func(claims map[string]any)) *http.Response {
	t.Helper()
	l := h.login(t, LoginPath)
	c := h.provider.claims(l.query.Get("nonce"))
	edit(c)
	return h.callback(l, l.query.Get("state"), h.provider.signRS256(c))
}

// assertRefused checks resp is the refused-sign-in page and sets no session.
func assertRefused(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("callback status = %d, want 403", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"This kitchen is for the household only",
		`<form method="post" action="/auth/logout">`,
		"Sign out",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("refused page lacks %q:\n%s", want, body)
		}
	}
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName || c.Name == "test_session" {
			t.Errorf("refused sign-in set session cookie %s", c.Name)
		}
	}
	assertLoginCookieCleared(t, resp)
}

func TestGroupGateMember(t *testing.T) {
	t.Parallel()
	// Any one allowed group is enough, alongside groups that are not allowed.
	h := newHarness(t)
	resp := h.signInWith(t, func(c map[string]any) { c["groups"] = []string{"guests", "mealquest-family"} })
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", resp.StatusCode)
	}
	if started := h.sessions.started(); len(started) != 1 || !slices.Equal(started[0].Groups, []string{"guests", "mealquest-family"}) {
		t.Errorf("sessions started = %+v, want one with the ID token groups", started)
	}
	if n := h.provider.userinfoRequests(); n != 0 {
		t.Errorf("userinfo requested %d times, want 0 when the ID token has groups", n)
	}
}

func TestGroupGateRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		groups   any // ID token groups claim; nil removes it
		userinfo map[string]any
	}{
		{name: "non-member", groups: []string{"guests", "MEALQUEST-ADMIN"}},
		{name: "empty groups claim", groups: []string{}},
		{name: "groups claim missing from ID token and userinfo", groups: nil},
		{
			name:     "non-member in userinfo",
			groups:   nil,
			userinfo: map[string]any{"sub": "user-123", "groups": []string{"guests"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			if tc.userinfo != nil {
				h.provider.setUserinfo(tc.userinfo)
			}
			resp := h.signInWith(t, func(c map[string]any) {
				if tc.groups == nil {
					delete(c, "groups")
				} else {
					c["groups"] = tc.groups
				}
			})
			assertRefused(t, resp)
			if started := h.sessions.started(); len(started) != 0 {
				t.Errorf("sessions started = %+v, want none", started)
			}
			if !strings.Contains(h.logs.String(), "sign-in refused") {
				t.Errorf("refusal not logged:\n%s", h.logs.String())
			}
		})
	}
}

func TestGroupGateUsesUserinfoWhenIDTokenHasNoGroups(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.provider.setUserinfo(map[string]any{"sub": "user-123", "groups": []string{"mealquest-admin"}})
	resp := h.signInWith(t, func(c map[string]any) { delete(c, "groups") })
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d, want 303", resp.StatusCode)
	}
	if started := h.sessions.started(); len(started) != 1 || !slices.Equal(started[0].Groups, []string{"mealquest-admin"}) {
		t.Errorf("sessions started = %+v, want one with the userinfo groups", started)
	}
	if n := h.provider.userinfoRequests(); n != 1 {
		t.Errorf("userinfo requested %d times, want 1", n)
	}
}

func TestGroupGateUserinfoFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		userinfo map[string]any
	}{
		{"subject mismatch", map[string]any{"sub": "someone-else", "groups": []string{"mealquest-admin"}}},
		{"malformed groups", map[string]any{"sub": "user-123", "groups": "mealquest-admin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			h.provider.setUserinfo(tc.userinfo)
			resp := h.signInWith(t, func(c map[string]any) { delete(c, "groups") })
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("callback status = %d, want 401", resp.StatusCode)
			}
			if started := h.sessions.started(); len(started) != 0 {
				t.Errorf("sessions started = %+v, want none", started)
			}
		})
	}
}

// A failed userinfo request fails the sign-in without logging the
// endpoint's response, which may echo credentials.
func TestGroupGateUserinfoErrorNotLogged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.provider.mu.Lock()
	h.provider.userinfoReply = &tokenReply{status: http.StatusInternalServerError, body: `{"error":"leaked-s3cret"}`}
	h.provider.mu.Unlock()
	resp := h.signInWith(t, func(c map[string]any) { delete(c, "groups") })
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("callback status = %d, want 401", resp.StatusCode)
	}
	if started := h.sessions.started(); len(started) != 0 {
		t.Errorf("sessions started = %+v, want none", started)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "userinfo") || strings.Contains(logs, "leaked-s3cret") {
		t.Errorf("logs = %s, want the userinfo failure without the response body", logs)
	}
}

// Membership is checked at every login: after removal from the group, the
// next login is refused and starts no session.
func TestGroupGateRemovedMemberRefusedAtNextLogin(t *testing.T) {
	t.Parallel()
	h := newSessionHarness(t)
	h.signIn(t)
	if n := len(h.rows(t)); n != 1 {
		t.Fatalf("sessions after first login = %d, want 1", n)
	}

	resp := h.signInWith(t, func(c map[string]any) { c["groups"] = []string{"former-household"} })
	assertRefused(t, resp)
	if n := len(h.rows(t)); n != 1 {
		t.Errorf("sessions after refused login = %d, want still 1", n)
	}
}

func TestNewRequiresAllowedGroups(t *testing.T) {
	t.Parallel()
	p := newFakeProvider(t)
	_, err := New(context.Background(), Config{
		Issuer:       p.issuer(),
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		PublicURL:    testPublicURL,
		SecretKey:    []byte("k"),
	}, &memorySessions{}, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "allowed group") {
		t.Errorf("New error = %v, want missing allowed groups named", err)
	}
}

func TestNewRequiresUserinfoEndpoint(t *testing.T) {
	t.Parallel()
	p := newFakeProvider(t)
	p.discoveryOverrides = map[string]any{"userinfo_endpoint": nil}
	_, err := New(context.Background(), Config{
		Issuer:        p.issuer(),
		ClientID:      testClientID,
		ClientSecret:  testClientSecret,
		PublicURL:     testPublicURL,
		SecretKey:     []byte("k"),
		AllowedGroups: testAllowedGroups,
	}, &memorySessions{}, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "userinfo endpoint") {
		t.Errorf("New error = %v, want the missing userinfo endpoint named", err)
	}
}
