package pushtest

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/push"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server/srvtest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const (
	vapidPublicKeyPath    = "/api/v1/push/vapid-public-key"
	pushSubscriptionsPath = "/api/v1/push/subscriptions"
	testPushPath          = "/api/v1/settings/notifications/test-push"
	testPushEndpoint      = "https://updates.push.services.mozilla.com/wpush/v2/gAAAAABk"
)

// newPushRouter returns a router serving svc.
func newPushRouter(t *testing.T, svc *push.Service) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), svc, srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t),
		srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(srvtest.FixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// subscriptionJSON returns a POST /push/subscriptions body for endpoint
// with new browser keys.
func subscriptionJSON(t *testing.T, endpoint, userAgent string) string {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	body := map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
	}
	if userAgent != "" {
		body["user_agent"] = userAgent
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func countPushSubscriptions(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.Reader.QueryRow(`SELECT count(*) FROM push_subscriptions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNewRouterRequiresPush(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), srvtest.NewTestChat(t), srvtest.NewTestDinners(t), srvtest.NewTestSuggest(t), srvtest.NewTestPlan(t), srvtest.NewTestShopping(t), srvtest.NewTestGoogle(t), nil, srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), srvtest.NewTestRunner(t), srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil push service")
	}
}

func TestPushAPI(t *testing.T) {
	t.Parallel()
	svc, st, pair := srvtest.NewTestPushWith(t)
	router := newPushRouter(t, svc)
	var bodies []string
	call := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, srvtest.SignedIn(method, target, r))
		bodies = append(bodies, rec.Body.String())
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return m
	}

	rec := call(http.MethodGet, vapidPublicKeyPath, "")
	if body := decode(rec); rec.Code != http.StatusOK || len(body) != 1 || body["public_key"] != pair.PublicKey {
		t.Errorf("GET public key = %d %s, want only the public key", rec.Code, rec.Body.String())
	}

	// Posting the same endpoint twice keeps one subscription, updated.
	rec = call(http.MethodPost, pushSubscriptionsPath, subscriptionJSON(t, testPushEndpoint, "Firefox on the kitchen tablet"))
	first := decode(rec)
	if rec.Code != http.StatusOK || first["endpoint"] != testPushEndpoint || first["user_agent"] != "Firefox on the kitchen tablet" || first["id"] == "" {
		t.Errorf("POST subscription = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := first["keys"]; ok {
		t.Errorf("POST response holds the keys: %s", rec.Body.String())
	}
	rec = call(http.MethodPost, pushSubscriptionsPath, subscriptionJSON(t, testPushEndpoint, "Firefox 140"))
	again := decode(rec)
	if rec.Code != http.StatusOK || again["id"] != first["id"] || again["created_at"] != first["created_at"] || again["user_agent"] != "Firefox 140" {
		t.Errorf("POST same endpoint = %d %s, want the first subscription updated", rec.Code, rec.Body.String())
	}
	if n := countPushSubscriptions(t, st); n != 1 {
		t.Errorf("subscriptions after posting one endpoint twice = %d, want 1", n)
	}
	rec = call(http.MethodPost, pushSubscriptionsPath, subscriptionJSON(t, testPushEndpoint+"-phone", ""))
	if body := decode(rec); rec.Code != http.StatusOK || body["user_agent"] != nil {
		t.Errorf("POST second endpoint = %d %s", rec.Code, rec.Body.String())
	}
	if n := countPushSubscriptions(t, st); n != 2 {
		t.Errorf("subscriptions after a second endpoint = %d, want 2", n)
	}

	for _, bad := range []struct{ body, code string }{
		{"", "invalid_request"},
		{`{"endpoint":"` + testPushEndpoint + `","keys":{"p256dh":"x","auth":"y"},"expirationTime":null}`, "invalid_request"},
		{`{"endpoint":"` + testPushEndpoint + `","keys":{"p256dh":"x","auth":"y"},"extra":1}`, "invalid_request"},
		{`{"endpoint":"` + testPushEndpoint + `"}`, "invalid_subscription"},
		{`{"endpoint":"` + testPushEndpoint + `","keys":{"p256dh":"x","auth":"y"}}`, "invalid_subscription"},
		{subscriptionJSON(t, "http://push.example.com/x", ""), "invalid_subscription"},
		{`{"endpoint":"https://push.example.com/` + strings.Repeat("x", server.MaxPushBody) + `"}`, "invalid_request"},
	} {
		rec := call(http.MethodPost, pushSubscriptionsPath, bad.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"`+bad.code+`"`) {
			t.Errorf("POST %.80s = %d %s, want 400 %s", bad.body, rec.Code, rec.Body.String(), bad.code)
		}
	}
	if n := countPushSubscriptions(t, st); n != 2 {
		t.Errorf("subscriptions after invalid posts = %d, want 2", n)
	}

	// DELETE removes by endpoint and is 204 whether or not it was stored.
	for i := range 2 {
		rec = call(http.MethodDelete, pushSubscriptionsPath, `{"endpoint":"`+testPushEndpoint+`"}`)
		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Errorf("DELETE #%d = %d %s, want 204", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := countPushSubscriptions(t, st); n != 1 {
		t.Errorf("subscriptions after DELETE = %d, want 1", n)
	}
	for _, bad := range []string{"", `{}`, `{"endpoint":"not a url"}`} {
		rec := call(http.MethodDelete, pushSubscriptionsPath, bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %q = %d %s, want 400", bad, rec.Code, rec.Body.String())
		}
	}
	if n := countPushSubscriptions(t, st); n != 1 {
		t.Errorf("subscriptions after invalid deletes = %d, want 1", n)
	}

	// The private key never leaves the process, in any encoding.
	priv, err := base64.RawURLEncoding.DecodeString(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaks := []string{
		pair.PrivateKey,
		pair.PrivateKey[:16],
		base64.StdEncoding.EncodeToString(priv),
		base64.RawStdEncoding.EncodeToString(priv)[:16],
		hex.EncodeToString(priv),
	}
	for _, b := range bodies {
		for _, leak := range leaks {
			if strings.Contains(b, leak) {
				t.Errorf("response holds the VAPID private key: %s", b)
			}
		}
	}
}

func TestPushSubscriptionsNeedCSRF(t *testing.T) {
	t.Parallel()
	svc, st, _ := srvtest.NewTestPushWith(t)
	router := newPushRouter(t, svc)
	if _, err := svc.Subscribe(t.Context(), push.SubscribeRequest{Endpoint: testPushEndpoint + "-stored", P256dh: mustP256dh(t), Auth: "AAAAAAAAAAAAAAAAAAAAAA"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, body string
	}{
		{http.MethodPost, subscriptionJSON(t, testPushEndpoint, "")},
		{http.MethodDelete, `{"endpoint":"` + testPushEndpoint + `-stored"}`},
	} {
		for _, header := range []*string{nil, srvtest.Ptr(""), srvtest.Ptr("wrong-token")} {
			req := httptest.NewRequest(tc.method, pushSubscriptionsPath, strings.NewReader(tc.body))
			req.AddCookie(srvtest.ValidSessionCookie)
			if header != nil {
				req.Header.Set(server.CSRFHeader, *header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || rec.Body.String() != srvtest.CSRFJSON {
				t.Errorf("%s without a valid CSRF token (%v) = %d %s, want 403 csrf", tc.method, header, rec.Code, rec.Body.String())
			}
		}
	}
	if n := countPushSubscriptions(t, st); n != 1 {
		t.Errorf("subscriptions after refused requests = %d, want the 1 stored before", n)
	}
	// GET needs no token.
	req := httptest.NewRequest(http.MethodGet, vapidPublicKeyPath, nil)
	req.AddCookie(srvtest.ValidSessionCookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET public key without CSRF token = %d, want 200", rec.Code)
	}
}

func TestPushNeedsSession(t *testing.T) {
	t.Parallel()
	router := newPushRouter(t, srvtest.NewTestPush(t))
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		target := pushSubscriptionsPath
		if method == http.MethodGet {
			target = vapidPublicKeyPath
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", method, target, rec.Code)
		}
	}
}

func mustP256dh(t *testing.T) string {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

func TestTestPushAPI(t *testing.T) {
	t.Parallel()
	svc, st, _ := srvtest.NewTestPushWith(t)
	router := newPushRouter(t, svc)

	// With push off for plan_ready and no device subscribed, push off is
	// what the test says.
	if _, err := st.Writer.Exec(`UPDATE notification_prefs SET push = 0 WHERE event = 'plan_ready'`); err != nil {
		t.Fatal(err)
	}
	rec, body := srvtest.Serve(t, router, srvtest.SignedIn(http.MethodPost, testPushPath, nil))
	if rec.Code != http.StatusConflict || body["error"].(map[string]any)["code"] != "push_off" {
		t.Errorf("POST with push off and no subscriptions = %d %s, want 409 push_off", rec.Code, rec.Body)
	}
	if _, err := st.Writer.Exec(`UPDATE notification_prefs SET push = 1 WHERE event = 'plan_ready'`); err != nil {
		t.Fatal(err)
	}

	// With no device subscribed there is nothing to test.
	rec, body = srvtest.Serve(t, router, srvtest.SignedIn(http.MethodPost, testPushPath, nil))
	if rec.Code != http.StatusConflict || body["error"].(map[string]any)["code"] != "no_subscriptions" {
		t.Errorf("POST with no subscriptions = %d %s, want 409 no_subscriptions", rec.Code, rec.Body)
	}

	for _, ep := range []string{testPushEndpoint, testPushEndpoint + "-phone"} {
		req := srvtest.SignedIn(http.MethodPost, pushSubscriptionsPath, strings.NewReader(subscriptionJSON(t, ep, "")))
		if rec, _ := srvtest.Serve(t, router, req); rec.Code != http.StatusOK {
			t.Fatalf("POST subscription = %d %s", rec.Code, rec.Body)
		}
	}
	rec, body = srvtest.Serve(t, router, srvtest.SignedIn(http.MethodPost, testPushPath, nil))
	key, _ := body["event_key"].(string)
	if rec.Code != http.StatusAccepted || !strings.HasPrefix(key, "plan_ready:test-") || body["subscriptions"] != float64(2) {
		t.Fatalf("POST test-push = %d %s, want 202 with a plan_ready:test- key and 2 subscriptions", rec.Code, rec.Body)
	}
	// One push row, none for the household's Google Chat space, opening /plan.
	want := []notifytest.Row{{Key: key, Channel: notify.ChannelPush, Payload: notify.Payload{
		Event: notify.PlanReady, Title: "MealQuest test notification", Body: push.TestBody, URL: "/plan", Tag: key,
	}}}
	if got := notifytest.Rows(t, st, notify.PlanReady); len(got) != 1 || got[0] != want[0] {
		t.Errorf("outbox = %+v, want %+v", got, want)
	}

	// With push off for plan_ready, the test says so and sends nothing.
	if _, err := st.Writer.Exec(`UPDATE notification_prefs SET push = 0 WHERE event = 'plan_ready'`); err != nil {
		t.Fatal(err)
	}
	rec, body = srvtest.Serve(t, router, srvtest.SignedIn(http.MethodPost, testPushPath, nil))
	if rec.Code != http.StatusConflict || body["error"].(map[string]any)["code"] != "push_off" {
		t.Errorf("POST with push off = %d %s, want 409 push_off", rec.Code, rec.Body)
	}
	if got := notifytest.Rows(t, st, notify.PlanReady); len(got) != 1 {
		t.Errorf("outbox after push off = %+v, want still one row", got)
	}
}
