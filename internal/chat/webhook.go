package chat

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	// webhookPrefix is how every accepted webhook URL starts, compared
	// exactly: no other scheme, host spelling, port or user info.
	webhookPrefix = "https://chat.googleapis.com/v1/spaces/"
	// webhookHost is the only host a webhook URL may name.
	webhookHost = "chat.googleapis.com"
	// MaxWebhookURLLength bounds an accepted webhook URL.
	MaxWebhookURLLength = 2048
	// MinCredentialLength is the shortest key or token accepted, so the
	// masked URL's token tail never gives away most of it.
	MinCredentialLength = 16
	// maskedTail is how many trailing characters of the token the masked
	// URL shows.
	maskedTail = 4
)

// webhookPath is the path of a space's incoming webhook; the group is the
// space id.
var webhookPath = regexp.MustCompile(`^/v1/spaces/([A-Za-z0-9_-]{1,128})/messages$`)

// Webhook is a validated Google Chat incoming webhook URL. Its String is
// the full URL, a secret: it is only ever sealed for storage or sent to
// Google Chat, never logged, shown or put in an error.
type Webhook struct {
	raw   string
	space string
	token string
}

// ParseWebhook validates raw as
// https://chat.googleapis.com/v1/spaces/{space}/messages?key=…&token=…,
// with exactly the query parameters key and token, each once and at least
// MinCredentialLength characters. Its error never quotes raw.
func ParseWebhook(raw string) (Webhook, error) {
	switch {
	case raw == "":
		return Webhook{}, errors.New("is required")
	case len(raw) > MaxWebhookURLLength:
		return Webhook{}, fmt.Errorf("must be at most %d characters", MaxWebhookURLLength)
	case strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return Webhook{}, errors.New("must not contain spaces or control characters")
	case !strings.HasPrefix(raw, webhookPrefix):
		return Webhook{}, errors.New("must be a Google Chat incoming webhook URL starting with " + webhookPrefix)
	case strings.Contains(raw, "#"):
		return Webhook{}, errors.New("must not have a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Webhook{}, errors.New("is not a valid URL")
	}
	if u.Scheme != "https" || u.Host != webhookHost || u.User != nil || u.Opaque != "" || u.RawPath != "" {
		return Webhook{}, errors.New("must be a Google Chat incoming webhook URL starting with " + webhookPrefix)
	}
	m := webhookPath.FindStringSubmatch(u.Path)
	if m == nil {
		return Webhook{}, errors.New("path must be /v1/spaces/{space}/messages")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Webhook{}, errors.New("query is not valid")
	}
	for name, values := range q {
		if name != "key" && name != "token" {
			return Webhook{}, errors.New("query must hold only key and token")
		}
		if len(values) != 1 {
			return Webhook{}, fmt.Errorf("query must hold %s once", name)
		}
	}
	for _, name := range []string{"key", "token"} {
		v := q.Get(name)
		if v == "" {
			return Webhook{}, fmt.Errorf("query must hold a %s", name)
		}
		if len(v) < MinCredentialLength {
			return Webhook{}, fmt.Errorf("%s must be at least %d characters", name, MinCredentialLength)
		}
	}
	return Webhook{raw: raw, space: m[1], token: q.Get("token")}, nil
}

// String returns the full webhook URL, a secret.
func (w Webhook) String() string {
	return w.raw
}

// Masked returns the URL with its key hidden and only the last few
// characters of its token, such as
// "https://chat.googleapis.com/v1/spaces/AAQA1234/messages?key=…&token=…x3Dk".
func (w Webhook) Masked() string {
	// MinCredentialLength bytes are at least maskedTail runes.
	token := []rune(w.token)
	return webhookPrefix + w.space + "/messages?key=…&token=…" + string(token[len(token)-maskedTail:])
}
