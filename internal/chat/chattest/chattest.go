// Package chattest provides a fake Google Chat incoming webhook for tests
// of the features that post to Google Chat: an httptest server that
// records each post and answers a status the test sets, reached through a
// transport that sends chat.googleapis.com requests to it.
package chattest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// Webhook is a valid Google Chat incoming webhook URL to store.
const Webhook = "https://chat.googleapis.com/v1/spaces/AAQA1b2C3d4/messages?key=AIzaSyDdI0hCZtE6vySjMm-WEfRq3CPzqKqqsHI&token=q0Lz3kV9Yw8pXcRtN2bHsJdFgA7uEmKiOlP1nZxQ%3D"

// Post is one message the fake received.
type Post struct {
	// Query is the request's query: key, token and any threading.
	Query url.Values
	// Body is the decoded JSON message.
	Body map[string]any
}

// Text is the post's text.
func (p Post) Text() string {
	s, _ := p.Body["text"].(string)
	return s
}

// Chat is a fake Google Chat endpoint. It answers 200 until SetStatus.
type Chat struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	posts  []Post
}

// New starts a fake Google Chat endpoint, closed when the test ends.
func New(t *testing.T) *Chat {
	t.Helper()
	c := &Chat{status: http.StatusOK}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("Google Chat post body %q: %v", data, err)
		}
		c.mu.Lock()
		c.posts = append(c.posts, Post{Query: r.URL.Query(), Body: body})
		status := c.status
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// Transport sends every request to the fake, as if it were
// chat.googleapis.com.
func (c *Chat) Transport() http.RoundTripper {
	return transport{c}
}

type transport struct{ c *Chat }

func (tr transport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(tr.c.srv.URL)
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host = target.Scheme, target.Host
	return http.DefaultTransport.RoundTrip(out)
}

// SetStatus makes the fake answer status from now on.
func (c *Chat) SetStatus(status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
}

// Close stops the fake, so posts get no answer.
func (c *Chat) Close() {
	c.srv.Close()
}

// Posts returns the messages received so far, in order.
func (c *Chat) Posts() []Post {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Post(nil), c.posts...)
}
