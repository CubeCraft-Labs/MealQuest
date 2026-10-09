package chat

import (
	"strings"
	"testing"
)

const (
	testKey   = "AIzaSyDdI0hCZtE6vySjMm-WEfRq3CPzqKqqsHI"
	testToken = "q0Lz3kV9Yw8pXcRtN2bHsJdFgA7uEmKiOlP1nZxQ%3D"
	testSpace = "AAQA1b2C3d4"
	testHook  = "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages?key=" + testKey + "&token=" + testToken
)

func TestParseWebhookAccepts(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		testHook,
		"https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages?token=" + testToken + "&key=" + testKey,
		"https://chat.googleapis.com/v1/spaces/AAAA_x-y/messages?key=" + testKey + "&token=" + testToken,
	} {
		hook, err := ParseWebhook(raw)
		if err != nil {
			t.Errorf("ParseWebhook(%q) = %v, want accepted", raw, err)
			continue
		}
		if hook.String() != raw {
			t.Errorf("String() = %q, want %q", hook.String(), raw)
		}
	}
}

func TestParseWebhookRejects(t *testing.T) {
	t.Parallel()
	q := "?key=" + testKey + "&token=" + testToken
	for name, raw := range map[string]string{
		"empty":               "",
		"http":                "http://chat.googleapis.com/v1/spaces/" + testSpace + "/messages" + q,
		"upper-case scheme":   "HTTPS://chat.googleapis.com/v1/spaces/" + testSpace + "/messages" + q,
		"lookalike suffix":    "https://chat.googleapis.com.evil.example/v1/spaces/" + testSpace + "/messages" + q,
		"lookalike prefix":    "https://evilchat.googleapis.com/v1/spaces/" + testSpace + "/messages" + q,
		"other google host":   "https://www.googleapis.com/v1/spaces/" + testSpace + "/messages" + q,
		"upper-case host":     "https://CHAT.googleapis.com/v1/spaces/" + testSpace + "/messages" + q,
		"port":                "https://chat.googleapis.com:443/v1/spaces/" + testSpace + "/messages" + q,
		"user info":           "https://user@chat.googleapis.com/v1/spaces/" + testSpace + "/messages" + q,
		"trailing dot host":   "https://chat.googleapis.com./v1/spaces/" + testSpace + "/messages" + q,
		"no space":            "https://chat.googleapis.com/v1/spaces//messages" + q,
		"extra path":          "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages/x" + q,
		"other version":       "https://chat.googleapis.com/v2/spaces/" + testSpace + "/messages" + q,
		"members path":        "https://chat.googleapis.com/v1/spaces/" + testSpace + "/members" + q,
		"escaped path":        "https://chat.googleapis.com/v1/spaces/AA%2FQA/messages" + q,
		"dot segment":         "https://chat.googleapis.com/v1/spaces/../messages" + q,
		"no query":            "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages",
		"no key":              "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages?token=" + testToken,
		"no token":            "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages?key=" + testKey,
		"empty token":         "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages?key=" + testKey + "&token=",
		"short key":           "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages?key=abc&token=" + testToken,
		"repeated key":        "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages" + q + "&key=" + testKey,
		"extra parameter":     "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages" + q + "&threadKey=x",
		"bad escape in query": "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages" + q + "%zz",
		"fragment":            testHook + "#frag",
		"space":               testHook + " ",
		"newline":             testHook + "\n",
		"too long":            testHook + strings.Repeat("A", MaxWebhookURLLength),
		"not a URL":           "chat webhook please",
	} {
		_, err := ParseWebhook(raw)
		if err == nil {
			t.Errorf("%s: ParseWebhook(%q) accepted", name, raw)
			continue
		}
		for _, secret := range []string{testKey, testToken, testSpace} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error %q quotes the URL", name, err)
			}
		}
	}
}

func TestWebhookMasked(t *testing.T) {
	t.Parallel()
	hook, err := ParseWebhook(testHook)
	if err != nil {
		t.Fatal(err)
	}
	// The tail is of the token as it decodes: "%3D" is "=".
	want := "https://chat.googleapis.com/v1/spaces/" + testSpace + "/messages?key=…&token=…ZxQ="
	if got := hook.Masked(); got != want {
		t.Errorf("Masked() = %q, want %q", got, want)
	}
	if m := hook.Masked(); strings.Contains(m, testKey) || strings.Contains(m, testToken) || strings.Contains(m, testToken[:8]) {
		t.Errorf("Masked() = %q holds the key or token", m)
	}
}
