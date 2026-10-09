// Package chat sends notifications to the household's Google Chat space
// through its incoming webhook, and keeps that webhook in chat_settings.
//
// The webhook URL carries its own key and token, so it is a secret: it is
// sealed with secretbox in webhook_url_ct and is never returned, logged or
// put in an error. Only its space label, a masked tail and the outcome of
// the last test message are shown.
package chat

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const (
	// MaxSpaceLabelChars bounds a space label.
	MaxSpaceLabelChars = 100
	// requestTimeout bounds one post to Google Chat.
	requestTimeout = 15 * time.Second
	// maxDrainBytes bounds how much of a Google Chat response is read.
	maxDrainBytes = 4096
	// replyOption threads a message under its threadKey, starting the
	// thread when there is none yet.
	replyOption = "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
)

// Test outcomes, as chat_settings.last_test_status stores them.
const (
	TestOK     = "ok"
	TestFailed = "failed"
)

var (
	// ErrNotConfigured is returned when no webhook is stored.
	ErrNotConfigured = errors.New("chat: no Google Chat webhook is configured")
	// ErrUnreachable is wrapped by the error of a post Google Chat did not
	// answer.
	ErrUnreachable = errors.New("chat: Google Chat did not answer")
	// ErrRefused is matched by the *RefusedError of a post Google Chat
	// answered with a status other than 2xx.
	ErrRefused = errors.New("chat: Google Chat refused the message")
	// ErrWebhookChanged is returned by Test when the webhook was replaced
	// while the test message was being sent; nothing is recorded.
	ErrWebhookChanged = errors.New("chat: the Google Chat webhook changed during the test")
)

// Settings is the stored webhook as the API shows it: never the URL, only
// its masked form. URLMasked is nil when no webhook is stored.
type Settings struct {
	SpaceLabel *string `json:"space_label"`
	// URLMasked is the webhook URL without its key and with only the last
	// few characters of its token.
	URLMasked *string `json:"url_masked"`
	// LastTestAt and LastTestStatus ("ok" or "failed") are nil until a
	// test message is sent to this webhook.
	LastTestAt     *string `json:"last_test_at"`
	LastTestStatus *string `json:"last_test_status"`
}

// TestResult is the outcome of a test message.
type TestResult struct {
	// HTTPStatus is the status Google Chat answered.
	HTTPStatus int `json:"http_status"`
	Settings
}

// RefusedError is a post Google Chat answered with Status, not 2xx. It
// matches ErrRefused.
type RefusedError struct {
	Status int
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("%v with status %d", ErrRefused, e.Status)
}

func (e *RefusedError) Unwrap() error { return ErrRefused }

// Retryable reports whether posting again may succeed: Google Chat was
// busy (429) or failing (5xx), rather than refusing the message.
func (e *RefusedError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Problem is one invalid field of a webhook update, named by its JSON key.
type Problem struct {
	Field  string
	Reason string
}

// ValidationError lists every problem with a webhook update. It never
// quotes the URL.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Reason
	}
	return "invalid Google Chat webhook: " + strings.Join(parts, "; ")
}

// Service keeps the webhook and posts to it. It is the notify.Sender of the
// chat channel.
type Service struct {
	st  *store.Store
	box *secretbox.Box
	// publicURL is MEALQUEST_PUBLIC_URL, which a card's "Open" button
	// links under.
	publicURL *url.URL
	client    *http.Client
	// now is the clock settings are saved and tests recorded on; tests
	// replace it.
	now func() time.Time
}

// NewService returns the service over st, sealing the webhook with box and
// linking cards under publicURL. Requests go through transport
// (http.DefaultTransport in production) and never follow a redirect.
func NewService(st *store.Store, box *secretbox.Box, publicURL *url.URL, transport http.RoundTripper) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("chat: nil store")
	case box == nil:
		return nil, errors.New("chat: nil secret box")
	case publicURL == nil:
		return nil, errors.New("chat: nil public URL")
	case transport == nil:
		return nil, errors.New("chat: nil transport")
	}
	return &Service{
		st:        st,
		box:       box,
		publicURL: publicURL,
		client: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}, nil
}

// Get returns the stored webhook's settings; every field is nil before one
// is saved.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	row, err := db.New(s.st.Reader).GetChatSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("chat: read chat settings: %w", err)
	}
	hook, err := s.open(row)
	if err != nil {
		return Settings{}, err
	}
	return settingsFrom(row, hook), nil
}

// Put validates rawURL and spaceLabel and stores them, replacing any
// webhook stored before and clearing its test outcome. Every problem is
// reported in a *ValidationError.
func (s *Service) Put(ctx context.Context, rawURL, spaceLabel string) (Settings, error) {
	var problems []Problem
	hook, err := ParseWebhook(rawURL)
	if err != nil {
		problems = append(problems, Problem{"url", err.Error()})
	}
	label := strings.TrimSpace(spaceLabel)
	switch {
	case label == "":
		problems = append(problems, Problem{"space_label", "is required"})
	case utf8.RuneCountInString(label) > MaxSpaceLabelChars:
		problems = append(problems, Problem{"space_label", fmt.Sprintf("must be at most %d characters", MaxSpaceLabelChars)})
	case strings.ContainsFunc(label, func(r rune) bool { return r < ' ' || r == 0x7f }):
		problems = append(problems, Problem{"space_label", "must not contain control characters"})
	}
	if len(problems) > 0 {
		return Settings{}, &ValidationError{Problems: problems}
	}
	ct, err := s.box.Encrypt([]byte(hook.String()))
	if err != nil {
		return Settings{}, fmt.Errorf("chat: seal webhook: %w", err)
	}

	var saved db.ChatSetting
	err = s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		row, err := q.GetChatSettings(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat: read chat settings: %w", err)
		}
		now := s.timestamp()
		created := now
		if err == nil {
			created = row.CreatedAt
		}
		saved, err = q.UpsertChatSettings(ctx, db.UpsertChatSettingsParams{
			WebhookUrlCt: ct,
			SpaceLabel:   sql.NullString{String: label, Valid: true},
			CreatedAt:    created,
			UpdatedAt:    now,
		})
		if err != nil {
			return fmt.Errorf("chat: save chat settings: %w", err)
		}
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	return settingsFrom(saved, hook), nil
}

// Test posts TestText to the stored webhook, unthreaded, and records when
// and whether Google Chat accepted it. A status Google Chat answers is a
// TestResult, whatever it is; a request that gets no answer is recorded as
// failed and returned as an error. A test cut short by ctx records nothing.
func (s *Service) Test(ctx context.Context) (TestResult, error) {
	row, err := db.New(s.st.Reader).GetChatSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return TestResult{}, ErrNotConfigured
	}
	if err != nil {
		return TestResult{}, fmt.Errorf("chat: read chat settings: %w", err)
	}
	hook, err := s.open(row)
	if err != nil {
		return TestResult{}, err
	}
	status, postErr := s.post(ctx, hook, message{Text: TestText}, "")
	if err := ctx.Err(); err != nil {
		return TestResult{}, err
	}
	outcome := TestFailed
	if postErr == nil && status >= 200 && status < 300 {
		outcome = TestOK
	}
	at := s.timestamp()
	write, cancel := jobs.FinalWriteContext(ctx)
	defer cancel()
	n, err := db.New(s.st.Writer).RecordChatTest(write, db.RecordChatTestParams{
		LastTestAt:     sql.NullString{String: at, Valid: true},
		LastTestStatus: sql.NullString{String: outcome, Valid: true},
		UpdatedAt:      at,
		WebhookUrlCt:   row.WebhookUrlCt,
	})
	if err != nil {
		return TestResult{}, fmt.Errorf("chat: record test: %w", err)
	}
	if n != 1 {
		return TestResult{}, ErrWebhookChanged
	}
	if postErr != nil {
		return TestResult{}, postErr
	}
	row.LastTestAt = sql.NullString{String: at, Valid: true}
	row.LastTestStatus = sql.NullString{String: outcome, Valid: true}
	row.UpdatedAt = at
	return TestResult{HTTPStatus: status, Settings: settingsFrom(row, hook)}, nil
}

// Send posts m to the stored webhook, threaded by the week of its payload,
// and is the chat channel's notify.Sender. Google Chat answering 2xx is
// success; 429 or 5xx, or no answer at all, is an error the outbox
// retries; any other status, or no webhook stored, wraps
// notify.ErrUndeliverable so the row fails at once.
func (s *Service) Send(ctx context.Context, m notify.Message) error {
	row, err := db.New(s.st.Reader).GetChatSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %w", notify.ErrUndeliverable, ErrNotConfigured)
	}
	if err != nil {
		return fmt.Errorf("chat: read chat settings: %w", err)
	}
	hook, err := s.open(row)
	if err != nil {
		return err
	}
	msg, err := s.messageOf(m.Payload)
	if err != nil {
		return fmt.Errorf("%w: chat: %w", notify.ErrUndeliverable, err)
	}
	var thread string
	if m.Payload.Week != "" {
		week, err := store.ParseLocalDate(m.Payload.Week)
		if err != nil {
			return fmt.Errorf("%w: chat: payload week: %w", notify.ErrUndeliverable, err)
		}
		thread = ThreadKey(week)
	}
	status, err := s.post(ctx, hook, msg, thread)
	switch {
	case err != nil:
		return err
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("chat: Google Chat answered status %d", status)
	default:
		return fmt.Errorf("%w: chat: Google Chat refused the message with status %d", notify.ErrUndeliverable, status)
	}
}

// ThreadKey is the Chat thread of the week starting on week, such as
// "mealquest-week-2026-W42": every message about a week goes in one thread.
func ThreadKey(week store.LocalDate) string {
	return "mealquest-week-" + household.PeriodKey(week)
}

// message is the body of a Google Chat webhook post.
type message struct {
	Text    string   `json:"text"`
	CardsV2 []cardV2 `json:"cardsV2,omitempty"`
}

type cardV2 struct {
	CardID string `json:"cardId"`
	Card   card   `json:"card"`
}

type card struct {
	Header   cardHeader    `json:"header"`
	Sections []cardSection `json:"sections"`
}

type cardHeader struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
}

type cardSection struct {
	Widgets []cardWidget `json:"widgets"`
}

type cardWidget struct {
	ButtonList buttonList `json:"buttonList"`
}

type buttonList struct {
	Buttons []button `json:"buttons"`
}

type button struct {
	Text    string  `json:"text"`
	OnClick onClick `json:"onClick"`
}

type onClick struct {
	OpenLink openLink `json:"openLink"`
}

type openLink struct {
	URL string `json:"url"`
}

// messageOf is the Chat message of p: its Text and, for a plan or Supply
// run, a card whose "Open" button links to p.URL under the public URL.
func (s *Service) messageOf(p notify.Payload) (message, error) {
	msg := message{Text: Text(p)}
	if !carded[p.Event] {
		return msg, nil
	}
	c, err := s.openCard(string(p.Event), p.Title, p.Body, p.URL)
	if err != nil {
		return message{}, err
	}
	msg.CardsV2 = []cardV2{c}
	return msg, nil
}

// openCard is a card headed title and subtitle whose "Open" button links
// to the app path under the public URL.
func (s *Service) openCard(id, title, subtitle, path string) (cardV2, error) {
	ref, err := url.Parse(path)
	if err != nil || ref.Scheme != "" || ref.Host != "" || !strings.HasPrefix(path, "/") {
		return cardV2{}, fmt.Errorf("payload url %q is not an app path", path)
	}
	link := s.publicURL.ResolveReference(ref)
	return cardV2{
		CardID: id,
		Card: card{
			Header: cardHeader{Title: title, Subtitle: subtitle},
			Sections: []cardSection{{Widgets: []cardWidget{{ButtonList: buttonList{Buttons: []button{{
				Text:    "Open",
				OnClick: onClick{OpenLink: openLink{URL: link.String()}},
			}}}}}}},
		},
	}, nil
}

// PostSupplyRun posts the Supply run of the week starting on week, named
// name, with groups, to the stored webhook in the week's thread: its
// SupplyRunText and a card whose "Open" button links to that week's Supply
// run. No webhook stored is ErrNotConfigured; a post Google Chat does not
// answer wraps ErrUnreachable, and one it answers with a status other than
// 2xx is a *RefusedError.
func (s *Service) PostSupplyRun(ctx context.Context, week store.LocalDate, name string, groups []SupplyRunGroup) error {
	row, err := db.New(s.st.Reader).GetChatSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotConfigured
	}
	if err != nil {
		return fmt.Errorf("chat: read chat settings: %w", err)
	}
	hook, err := s.open(row)
	if err != nil {
		return err
	}
	path := "/plan/shopping?" + url.Values{"week": {week.String()}}.Encode()
	c, err := s.openCard("supply_run", "Supply run · "+name, "", path)
	if err != nil {
		return err
	}
	status, err := s.post(ctx, hook, message{Text: SupplyRunText(name, groups), CardsV2: []cardV2{c}}, ThreadKey(week))
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &RefusedError{Status: status}
	}
	return nil
}

// post sends msg to hook, in thread when it is not empty, and returns the
// status Google Chat answered. Its error never quotes the webhook URL.
func (s *Service) post(ctx context.Context, hook Webhook, msg message, thread string) (int, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("chat: encode message: %w", err)
	}
	target, err := url.Parse(hook.String())
	if err != nil {
		return 0, errors.New("chat: stored webhook URL does not parse")
	}
	if thread != "" {
		q := target.Query()
		q.Set("threadKey", thread)
		q.Set("messageReplyOption", replyOption)
		target.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("chat: build request to the webhook")
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := s.client.Do(req)
	if err != nil {
		// A *url.Error quotes the request URL, the webhook; keep only its
		// cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, fmt.Errorf("%w: %w", ErrUnreachable, ue.Err)
		}
		return 0, ErrUnreachable
	}
	defer resp.Body.Close()
	// The status is the outcome; the body is read only so the connection
	// can be reused.
	io.CopyN(io.Discard, resp.Body, maxDrainBytes)
	return resp.StatusCode, nil
}

// open decrypts and validates the stored webhook. Its error never quotes
// the URL.
func (s *Service) open(row db.ChatSetting) (Webhook, error) {
	raw, err := s.box.Decrypt(row.WebhookUrlCt)
	if err != nil {
		return Webhook{}, fmt.Errorf("chat: open stored webhook: %w", err)
	}
	hook, err := ParseWebhook(string(raw))
	if err != nil {
		return Webhook{}, fmt.Errorf("chat: stored webhook URL is invalid: %w", err)
	}
	return hook, nil
}

func (s *Service) timestamp() string {
	return store.Timestamp{Time: s.now().UTC().Truncate(time.Millisecond)}.String()
}

func settingsFrom(row db.ChatSetting, hook Webhook) Settings {
	masked := hook.Masked()
	return Settings{
		SpaceLabel:     nullString(row.SpaceLabel),
		URLMasked:      &masked,
		LastTestAt:     nullString(row.LastTestAt),
		LastTestStatus: nullString(row.LastTestStatus),
	}
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
