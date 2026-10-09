package server

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
)

const (
	chatWebhookPath     = "/api/v1/settings/chat-webhook"
	chatWebhookTestPath = "/api/v1/settings/chat-webhook/test"
	testChatKey         = "AIzaSyDdI0hCZtE6vySjMm-WEfRq3CPzqKqqsHI"
	testChatToken       = "q0Lz3kV9Yw8pXcRtN2bHsJdFgA7uEmKiOlP1nZxQ%3D"
	testChatHook        = "https://chat.googleapis.com/v1/spaces/AAQA1b2C3d4/messages?key=" + testChatKey + "&token=" + testChatToken
)

// refuseTransport fails every request, as a Chat endpoint that is down.
type refuseTransport struct{}

func (refuseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

// chatTransport sends every request to the server at target, as if it
// were chat.googleapis.com.
type chatTransport struct {
	target *url.URL
}

func (c chatTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = c.target.Scheme, c.target.Host
	return http.DefaultTransport.RoundTrip(out)
}

// newTestChat returns a Google Chat service over a migrated temp database
// whose requests all fail.
func newTestChat(t *testing.T) *chat.Service {
	t.Helper()
	return newTestChatVia(t, refuseTransport{})
}

func newTestChatVia(t *testing.T, transport http.RoundTripper) *chat.Service {
	t.Helper()
	pub, err := url.Parse("https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := chat.NewService(newTestStore(t), newTestBox(t), pub, transport)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// lockedBuffer is a bytes.Buffer safe for a logger.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newChatRouter returns a router serving svc, logging at debug level to
// logs.
func newChatRouter(t *testing.T, svc *chat.Service, logs io.Writer) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), svc, newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestRunner(t),
		newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChatWebhookAPI(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		status   = http.StatusOK
		received []string
	)
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		received = append(received, string(body))
		w.WriteHeader(status)
	}))
	t.Cleanup(chatSrv.Close)
	target, _ := url.Parse(chatSrv.URL)
	setStatus := func(s int) {
		mu.Lock()
		defer mu.Unlock()
		status = s
	}

	var logs lockedBuffer
	router := newChatRouter(t, newTestChatVia(t, chatTransport{target}), &logs)
	var bodies []string
	call := func(method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		rec, decoded := serve(t, router, signedIn(method, target, r))
		bodies = append(bodies, rec.Body.String())
		return rec, decoded
	}

	rec, body := call(http.MethodGet, chatWebhookPath, "")
	if rec.Code != http.StatusOK || body["url_masked"] != nil || body["space_label"] != nil || body["last_test_at"] != nil || body["last_test_status"] != nil || len(body) != 4 {
		t.Errorf("GET unconfigured = %d %v", rec.Code, body)
	}
	rec, body = call(http.MethodPost, chatWebhookTestPath, "")
	if rec.Code != http.StatusConflict || body["error"].(map[string]any)["code"] != "not_configured" {
		t.Errorf("POST test unconfigured = %d %v", rec.Code, body)
	}

	q := "?key=" + testChatKey + "&token=" + testChatToken
	for _, bad := range []struct{ body, code string }{
		{`{"url":"https://chat.googleapis.com.evil.example/v1/spaces/X/messages` + q + `","space_label":"Family"}`, "invalid_webhook"},
		{`{"url":"http://chat.googleapis.com/v1/spaces/X/messages` + q + `","space_label":"Family"}`, "invalid_webhook"},
		{`{"url":"https://chat.googleapis.com/v1/spaces/X/messages?key=` + testChatKey + `","space_label":"Family"}`, "invalid_webhook"},
		{`{"url":"https://chat.googleapis.com/v1/spaces/X/threads` + q + `","space_label":"Family"}`, "invalid_webhook"},
		{`{"url":"` + testChatHook + `","space_label":""}`, "invalid_webhook"},
		{`{"url":"` + testChatHook + `"}`, "invalid_request"},
		{`{"space_label":"Family"}`, "invalid_request"},
		{`{"url":"` + testChatHook + `","space_label":null}`, "invalid_request"},
		{`{"url":"` + testChatHook + `","space_label":"Family","extra":1}`, "invalid_request"},
		{`not json`, "invalid_request"},
	} {
		rec, body := call(http.MethodPut, chatWebhookPath, bad.body)
		if rec.Code != http.StatusBadRequest || body["error"].(map[string]any)["code"] != bad.code {
			t.Errorf("PUT %s = %d %v, want 400 %s", bad.body, rec.Code, body, bad.code)
		}
	}

	rec, body = call(http.MethodPut, chatWebhookPath, `{"url":"`+testChatHook+`","space_label":"Family dinner"}`)
	wantMasked := "https://chat.googleapis.com/v1/spaces/AAQA1b2C3d4/messages?key=…&token=…ZxQ="
	if rec.Code != http.StatusOK || body["space_label"] != "Family dinner" || body["url_masked"] != wantMasked ||
		body["last_test_at"] != nil || body["last_test_status"] != nil || len(body) != 4 {
		t.Errorf("PUT = %d %v", rec.Code, body)
	}
	if _, ok := body["url"]; ok {
		t.Errorf("PUT response has a url field: %v", body)
	}
	rec, body = call(http.MethodGet, chatWebhookPath, "")
	if rec.Code != http.StatusOK || body["space_label"] != "Family dinner" || body["url_masked"] != wantMasked {
		t.Errorf("GET = %d %v", rec.Code, body)
	}

	rec, body = call(http.MethodPost, chatWebhookTestPath, "")
	if rec.Code != http.StatusOK || body["http_status"] != float64(200) || body["last_test_status"] != "ok" || body["last_test_at"] == nil {
		t.Errorf("POST test = %d %v, want 200 ok", rec.Code, body)
	}
	mu.Lock()
	if len(received) != 1 || !strings.Contains(received[0], `"text":"MealQuest · test message"`) {
		t.Errorf("Chat received %q, want the test message", received)
	}
	mu.Unlock()

	setStatus(http.StatusForbidden)
	rec, body = call(http.MethodPost, chatWebhookTestPath, "")
	if rec.Code != http.StatusOK || body["http_status"] != float64(403) || body["last_test_status"] != "failed" {
		t.Errorf("POST test answered 403 = %d %v, want 403 failed", rec.Code, body)
	}
	rec, body = call(http.MethodGet, chatWebhookPath, "")
	if body["last_test_status"] != "failed" || body["last_test_at"] == nil {
		t.Errorf("GET after a failed test = %d %v", rec.Code, body)
	}

	chatSrv.Close()
	rec, body = call(http.MethodPost, chatWebhookTestPath, "")
	if rec.Code != http.StatusBadGateway || body["error"].(map[string]any)["code"] != "chat_unreachable" {
		t.Errorf("POST test against a stopped endpoint = %d %v, want 502 chat_unreachable", rec.Code, body)
	}

	tokenDecoded, _ := url.QueryUnescape(testChatToken)
	for what, texts := range map[string][]string{"response": bodies, "log": {logs.String()}} {
		for _, text := range texts {
			for _, secret := range []string{testChatHook, testChatKey, testChatToken, tokenDecoded, testChatKey[:12], testChatToken[:12]} {
				if strings.Contains(text, secret) {
					t.Errorf("%s holds the webhook URL or a credential: %s", what, text)
				}
			}
		}
	}
	if !strings.Contains(logs.String(), "chat webhook test failed") {
		t.Errorf("log %q does not record the failed test, so the scan proves nothing", logs.String())
	}
}

func TestChatWebhookNeedsCSRF(t *testing.T) {
	t.Parallel()
	router := newChatRouter(t, newTestChat(t), io.Discard)
	for _, tc := range []struct{ method, target string }{
		{http.MethodPut, chatWebhookPath},
		{http.MethodPost, chatWebhookTestPath},
	} {
		req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(`{"url":"`+testChatHook+`","space_label":"Family"}`))
		req.AddCookie(validSessionCookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without CSRF token = %d, want 403", tc.method, tc.target, rec.Code)
		}
	}
}

func TestNewRouterRequiresChat(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), nil, newTestDinners(t), newTestSuggest(t), newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestRunner(t), newTestPhotos(t), fstest.MapFS{"index.html": {}}); err == nil {
		t.Fatal("NewRouter accepted a nil Google Chat service")
	}
}
