package chat

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// posted is one request the fake Chat endpoint received.
type posted struct {
	Method, Host, Path string
	Query              url.Values
	ContentType        string
	Body               map[string]any
}

// fakeChat is an httptest Google Chat endpoint answering status.
type fakeChat struct {
	srv *httptest.Server
	mu  sync.Mutex
	// status is what it answers.
	status int
	got    []posted
}

func newFakeChat(t *testing.T) *fakeChat {
	t.Helper()
	f := &fakeChat{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("Chat endpoint body %q: %v", data, err)
		}
		f.mu.Lock()
		f.got = append(f.got, posted{r.Method, r.Host, r.URL.Path, r.URL.Query(), r.Header.Get("Content-Type"), body})
		status := f.status
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeChat) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeChat) requests() []posted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]posted(nil), f.got...)
}

// redirectTo sends every request to the server at target, keeping its path
// and query, as if it were chat.googleapis.com.
type redirectTo struct {
	target *url.URL
}

func (r redirectTo) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = r.target.Scheme, r.target.Host
	out.Host = req.URL.Host
	return http.DefaultTransport.RoundTrip(out)
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return st
}

func newTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// newTestService returns a service over a migrated temp database whose
// requests go to f, with cards linking under https://mealquest.example.
func newTestService(t *testing.T, f *fakeChat) (*Service, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	target, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := url.Parse("https://mealquest.example")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(st, newTestBox(t), pub, redirectTo{target})
	if err != nil {
		t.Fatal(err)
	}
	return svc, st
}

// assertNoSecret fails when s holds the webhook URL, its key or its token.
func assertNoSecret(t *testing.T, what, s string) {
	t.Helper()
	tokenDecoded, _ := url.QueryUnescape(testToken)
	for _, secret := range []string{testHook, testKey, testToken, tokenDecoded, testKey[:12], testToken[:12]} {
		if strings.Contains(s, secret) {
			t.Errorf("%s holds the webhook URL or a credential: %s", what, s)
			return
		}
	}
}

func TestNewServiceRequiresDependencies(t *testing.T) {
	t.Parallel()
	st, box := newTestStore(t), newTestBox(t)
	pub, _ := url.Parse("https://mealquest.example")
	for name, build := range map[string]func() (*Service, error){
		"store":      func() (*Service, error) { return NewService(nil, box, pub, http.DefaultTransport) },
		"box":        func() (*Service, error) { return NewService(st, nil, pub, http.DefaultTransport) },
		"public URL": func() (*Service, error) { return NewService(st, box, nil, http.DefaultTransport) },
		"transport":  func() (*Service, error) { return NewService(st, box, pub, nil) },
	} {
		if _, err := build(); err == nil {
			t.Errorf("NewService without %s succeeded", name)
		}
	}
}

func TestPutGetStoresSealedWebhook(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st := newTestService(t, newFakeChat(t))

	got, err := svc.Get(ctx)
	if err != nil || got.SpaceLabel != nil || got.URLMasked != nil || got.LastTestAt != nil {
		t.Fatalf("Get unconfigured = %+v, %v", got, err)
	}

	put, err := svc.Put(ctx, testHook, "  Family dinner  ")
	if err != nil {
		t.Fatal(err)
	}
	if *put.SpaceLabel != "Family dinner" || *put.URLMasked != "https://chat.googleapis.com/v1/spaces/"+testSpace+"/messages?key=…&token=…ZxQ=" || put.LastTestAt != nil || put.LastTestStatus != nil {
		t.Errorf("Put = %+v", put)
	}
	got, err = svc.Get(ctx)
	if err != nil || *got.URLMasked != *put.URLMasked || *got.SpaceLabel != "Family dinner" {
		t.Errorf("Get = %+v, %v", got, err)
	}

	row, err := db.New(st.Reader).GetChatSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.WebhookUrlCt, []byte(testKey)) || bytes.Contains(row.WebhookUrlCt, []byte("chat.googleapis.com")) {
		t.Errorf("webhook_url_ct holds the URL in plain text")
	}
	plain, err := svc.box.Decrypt(row.WebhookUrlCt)
	if err != nil || string(plain) != testHook {
		t.Errorf("webhook_url_ct decrypts to %q, %v", plain, err)
	}
	encoded, _ := json.Marshal(got)
	assertNoSecret(t, "GET settings", string(encoded))
}

func TestPutRefusesInvalid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st := newTestService(t, newFakeChat(t))
	for _, tc := range []struct {
		url, label string
		fields     []string
	}{
		{"https://chat.googleapis.com.evil.example/v1/spaces/X/messages?key=" + testKey + "&token=" + testToken, "Family", []string{"url"}},
		{testHook, "   ", []string{"space_label"}},
		{testHook, strings.Repeat("x", MaxSpaceLabelChars+1), []string{"space_label"}},
		{testHook, "bad\nlabel", []string{"space_label"}},
		{"http://chat.googleapis.com/v1/spaces/X/messages?key=" + testKey + "&token=" + testToken, "", []string{"url", "space_label"}},
	} {
		_, err := svc.Put(ctx, tc.url, tc.label)
		var invalid *ValidationError
		if !errors.As(err, &invalid) || len(invalid.Problems) != len(tc.fields) {
			t.Errorf("Put(%q, %q) = %v, want problems with %v", tc.url, tc.label, err, tc.fields)
			continue
		}
		for i, p := range invalid.Problems {
			if p.Field != tc.fields[i] {
				t.Errorf("Put(%q, %q) problem %d = %s, want %s", tc.url, tc.label, i, p.Field, tc.fields[i])
			}
		}
		assertNoSecret(t, "validation error", err.Error())
	}
	if _, err := db.New(st.Reader).GetChatSettings(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a refused Put stored a row: %v", err)
	}
}

func TestTestRecordsOutcome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeChat(t)
	svc, _ := newTestService(t, f)
	svc.now = func() time.Time { return time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC) }

	if _, err := svc.Test(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Test unconfigured = %v, want ErrNotConfigured", err)
	}
	if _, err := svc.Put(ctx, testHook, "Family"); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Test(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.HTTPStatus != 200 || *res.LastTestStatus != TestOK || *res.LastTestAt != "2026-10-09T15:00:00.000Z" {
		t.Errorf("Test = %+v, want 200 ok", res)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("Chat got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Host != "chat.googleapis.com" || r.Path != "/v1/spaces/"+testSpace+"/messages" ||
		r.Query.Get("key") != testKey || r.Query.Get("threadKey") != "" || r.Body["text"] != TestText || r.Body["cardsV2"] != nil {
		t.Errorf("test request = %+v", r)
	}

	f.setStatus(http.StatusNotFound)
	res, err = svc.Test(ctx)
	if err != nil || res.HTTPStatus != 404 || *res.LastTestStatus != TestFailed {
		t.Errorf("Test answered 404 = %+v, %v; want 404 failed", res, err)
	}
	got, _ := svc.Get(ctx)
	if *got.LastTestStatus != TestFailed || *got.LastTestAt != "2026-10-09T15:00:00.000Z" {
		t.Errorf("Get after a failed test = %+v", got)
	}

	// A new webhook has no test outcome.
	got, err = svc.Put(ctx, testHook, "Family")
	if err != nil || got.LastTestAt != nil || got.LastTestStatus != nil {
		t.Errorf("Put after a test = %+v, %v; want the outcome cleared", got, err)
	}

	f.srv.Close()
	_, err = svc.Test(ctx)
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("Test against a stopped endpoint = %v, want ErrUnreachable", err)
	} else {
		assertNoSecret(t, "unreachable error", err.Error())
	}
	if got, _ := svc.Get(ctx); got.LastTestStatus == nil || *got.LastTestStatus != TestFailed {
		t.Errorf("Get after an unanswered test = %+v, want failed", got)
	}
}

func TestSendThreadsByWeek(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeChat(t)
	svc, _ := newTestService(t, f)
	if _, err := svc.Put(ctx, testHook, "Family"); err != nil {
		t.Fatal(err)
	}
	week := mustDate(t, "2026-10-12")
	events := []notify.Event{
		PlanReady(week, []string{"Tacos", "Bolognese", "Tikka masala", "A", "B", "C", "D"}),
		ListReady(week, 19, 7),
		DayOfReminder(mustDate(t, "2026-10-18"), "01J9DINNER", "Tacos"),
		NewSuggestions("01J9BATCH", week, []string{"Pho"}),
		RotationReview("01J9DINNER", "Pho", week),
	}
	for _, ev := range events {
		if err := svc.Send(ctx, notify.Message{ID: "n1", EventKey: ev.Key, Channel: notify.ChannelChat, Payload: payloadOf(ev), Attempt: 1}); err != nil {
			t.Fatalf("Send %s: %v", ev.Type, err)
		}
	}
	reqs := f.requests()
	if len(reqs) != len(events) {
		t.Fatalf("Chat got %d requests, want %d", len(reqs), len(events))
	}
	for i, r := range reqs {
		ev := events[i]
		if r.Method != http.MethodPost || r.Path != "/v1/spaces/"+testSpace+"/messages" || !strings.HasPrefix(r.ContentType, "application/json") {
			t.Errorf("%s: request = %+v", ev.Type, r)
		}
		if r.Query.Get("key") != testKey || r.Query.Get("token") != strings.ReplaceAll(testToken, "%3D", "=") {
			t.Errorf("%s: key and token not kept: %v", ev.Type, r.Query)
		}
		if r.Query.Get("threadKey") != "mealquest-week-2026-W42" || r.Query.Get("messageReplyOption") != "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD" {
			t.Errorf("%s: threading = %v", ev.Type, r.Query)
		}
		if want := Text(payloadOf(ev)); r.Body["text"] != want {
			t.Errorf("%s: text = %q, want %q", ev.Type, r.Body["text"], want)
		}
		_, hasCards := r.Body["cardsV2"]
		if hasCards != (ev.Type == notify.PlanReady || ev.Type == notify.ListReady) {
			t.Errorf("%s: cardsV2 present = %v", ev.Type, hasCards)
		}
	}
	if reqs[0].Body["text"] != "This week's plan is ready: Tacos, Bolognese, Tikka masala + 4 more. Open the plan →" {
		t.Errorf("plan text = %q", reqs[0].Body["text"])
	}

	// The cards' "Open" buttons link under the public URL.
	for i, wantLink := range map[int]string{0: "https://mealquest.example/plan", 1: "https://mealquest.example/plan/shopping"} {
		data, _ := json.Marshal(reqs[i].Body["cardsV2"])
		var cards []cardV2
		if err := json.Unmarshal(data, &cards); err != nil || len(cards) != 1 {
			t.Fatalf("cardsV2 = %s, %v", data, err)
		}
		c := cards[0]
		buttons := c.Card.Sections[0].Widgets[0].ButtonList.Buttons
		if c.CardID != string(events[i].Type) || c.Card.Header.Title != events[i].Title || c.Card.Header.Subtitle != events[i].Body ||
			len(buttons) != 1 || buttons[0].Text != "Open" || buttons[0].OnClick.OpenLink.URL != wantLink {
			t.Errorf("%s card = %s, want an Open button to %s", events[i].Type, data, wantLink)
		}
	}
}

func TestSendWithoutWeekIsUnthreaded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeChat(t)
	svc, _ := newTestService(t, f)
	if _, err := svc.Put(ctx, testHook, "Family"); err != nil {
		t.Fatal(err)
	}
	p := notify.Payload{Event: notify.RotationReview, Title: "Keep it in rotation?", URL: "/", Tag: "rotation_review:x"}
	if err := svc.Send(ctx, notify.Message{Payload: p}); err != nil {
		t.Fatal(err)
	}
	if q := f.requests()[0].Query; q.Has("threadKey") || q.Has("messageReplyOption") {
		t.Errorf("query = %v, want no threading", q)
	}
}

func TestSendClassifiesStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeChat(t)
	svc, _ := newTestService(t, f)
	if _, err := svc.Put(ctx, testHook, "Family"); err != nil {
		t.Fatal(err)
	}
	m := notify.Message{Payload: payloadOf(PlanReady(mustDate(t, "2026-10-12"), []string{"Tacos"}))}
	for _, tc := range []struct {
		status        int
		ok, permanent bool
	}{
		{200, true, false},
		{204, true, false},
		{301, false, true},
		{400, false, true},
		{401, false, true},
		{403, false, true},
		{404, false, true},
		{413, false, true},
		{429, false, false},
		{500, false, false},
		{502, false, false},
		{503, false, false},
	} {
		f.setStatus(tc.status)
		err := svc.Send(ctx, m)
		if (err == nil) != tc.ok || errors.Is(err, notify.ErrUndeliverable) != tc.permanent {
			t.Errorf("status %d: Send = %v, want ok %v, undeliverable %v", tc.status, err, tc.ok, tc.permanent)
		}
		if err != nil {
			assertNoSecret(t, "Send error", err.Error())
		}
	}

	f.srv.Close()
	err := svc.Send(ctx, m)
	if err == nil || errors.Is(err, notify.ErrUndeliverable) {
		t.Errorf("Send to a stopped endpoint = %v, want a retryable error", err)
	} else {
		assertNoSecret(t, "Send error", err.Error())
	}
}

func TestSendWithoutWebhookIsUndeliverable(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t, newFakeChat(t))
	err := svc.Send(context.Background(), notify.Message{Payload: payloadOf(ListReady(mustDate(t, "2026-10-12"), 3, 2))})
	if !errors.Is(err, notify.ErrUndeliverable) || !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Send = %v, want undeliverable, not configured", err)
	}
}

// TestOutboxRowsFollowClassification delivers through the notify worker: a
// 400 fails the row at once, a 503 leaves it pending for a retry. Neither
// the stored error nor the log holds the webhook URL.
func TestOutboxRowsFollowClassification(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeChat(t)
	svc, st := newTestService(t, f)
	if _, err := svc.Put(ctx, testHook, "Family"); err != nil {
		t.Fatal(err)
	}
	if err := household.SeedNotificationPrefs(ctx, st); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.Exec(`UPDATE notification_prefs SET push = 0, chat = 1`); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	notifier, err := notify.NewService(st, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := notifier.Register(notify.ChannelChat, svc); err != nil {
		t.Fatal(err)
	}
	if err := notifier.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		notifier.Wait()
	})

	week := mustDate(t, "2026-10-12")
	f.setStatus(http.StatusBadRequest)
	planned := PlanReady(week, []string{"Tacos"})
	if err := notifier.Emit(ctx, planned); err != nil {
		t.Fatal(err)
	}
	status, lastErr := waitForAttempt(t, st, planned.Key)
	if status != "failed" || !strings.Contains(lastErr, "status 400") {
		t.Errorf("row after a 400 = %s %q, want failed", status, lastErr)
	}

	f.setStatus(http.StatusServiceUnavailable)
	listed := ListReady(week, 3, 1)
	if err := notifier.Emit(ctx, listed); err != nil {
		t.Fatal(err)
	}
	status, lastErr = waitForAttempt(t, st, listed.Key)
	if status != "pending" || !strings.Contains(lastErr, "status 503") {
		t.Errorf("row after a 503 = %s %q, want pending for a retry", status, lastErr)
	}
	cancel()
	notifier.Wait()
	assertNoSecret(t, "log", logs.String())
	assertNoSecret(t, "last_error", lastErr)
}

// waitForAttempt waits for the chat row of key to have been attempted and
// returns its status and last error.
func waitForAttempt(t *testing.T, st *store.Store, key string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		var attempts int
		var lastErr sql.NullString
		err := st.Reader.QueryRow(`SELECT status, attempts, last_error FROM notification_outbox WHERE event_key = ? AND channel = 'chat'`, key).
			Scan(&status, &attempts, &lastErr)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		if attempts > 0 {
			return status, lastErr.String
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("chat row of %s was never attempted", key)
	return "", ""
}

// syncBuffer is a bytes.Buffer safe for the worker's logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
