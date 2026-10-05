package harness

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// redactionMarker replaces each credential redact removes.
const redactionMarker = "[REDACTED]"

var (
	// chatWebhookURL matches a Google Chat webhook URL, with or without
	// its scheme.
	chatWebhookURL = regexp.MustCompile(`(?i)(?:https?://)?chat\.googleapis\.com/[^\s"'<>]*`)
	// schemeURL matches a URL with a scheme, up to whitespace, a quote or
	// an angle bracket.
	schemeURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	// credentialHeader matches an Authorization or API-key header, or a
	// field so named, and its value up to the end of the line or a quote.
	// No word boundary is required before the name, so escaped text such
	// as a JSON "\nAuthorization: …" matches too.
	credentialHeader = regexp.MustCompile(`(?i)(` + credentialHeaderNames + `)(["']?[ \t]*[:=][ \t]*["']?)[^"'\r\n]*`)
)

// credentialHeaderNames are the header names whose values are credentials.
const credentialHeaderNames = `proxy-authorization|authorization|x-goog-api-key|x-api-key|api-key`

// credentialField matches a JSON object key naming a credential header.
var credentialField = regexp.MustCompile(`(?i)^(?:` + credentialHeaderNames + `)$`)

// credentialParams are the substrings that make a URL query or fragment
// parameter name key-like, compared in lower case.
var credentialParams = []string{"key", "token", "secret", "sig", "pass", "auth", "credential"}

// redact returns s with credentials removed: every occurrence of apiKey
// (the decrypted API key, or "" when there is none), Google Chat webhook
// URLs, URLs with a key-like query or fragment parameter or with user
// info, and the values of Authorization and API-key headers. Each becomes
// "[REDACTED]".
//
// When s is a JSON document its strings are redacted after decoding, so
// JSON escapes cannot hide a credential from the patterns, and it is
// encoded again. Any other s is redacted as text.
func redact(s, apiKey string) string {
	var doc any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if strings.TrimSpace(s) != "" && json.Valid([]byte(s)) && dec.Decode(&doc) == nil {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(redactJSON(doc, apiKey)); err != nil {
			// A decoded document always encodes; were it not to, it is
			// still redacted, as text.
			return redactText(s, apiKey)
		}
		// The encoding can join text into the key; Redact deletes it.
		return llm.Redact(strings.TrimSuffix(b.String(), "\n"), apiKey)
	}
	return redactText(s, apiKey)
}

// redactJSON returns the decoded JSON value v with every string and object
// key redacted, and the values of credential header fields replaced.
func redactJSON(v any, apiKey string) any {
	switch v := v.(type) {
	case string:
		return redact(v, apiKey)
	case []any:
		for i, e := range v {
			v[i] = redactJSON(e, apiKey)
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			if credentialField.MatchString(k) {
				e = redactionMarker
			}
			out[redact(k, apiKey)] = redactJSON(e, apiKey)
		}
		return out
	}
	return v
}

// redactText redacts s as plain text.
func redactText(s, apiKey string) string {
	s = llm.Redact(s, apiKey)
	s = chatWebhookURL.ReplaceAllLiteralString(s, redactionMarker)
	s = schemeURL.ReplaceAllStringFunc(s, func(u string) string {
		if hasCredential(u) {
			return redactionMarker
		}
		return u
	})
	s = credentialHeader.ReplaceAllString(s, "${1}${2}"+redactionMarker)
	// A removal can join text either side into the key; Redact deletes
	// it until none remains.
	return llm.Redact(s, apiKey)
}

// hasCredential reports whether URL u carries user info or a key-like
// query or fragment parameter. A parameter name that does not unescape
// counts as key-like, so it is never kept unexamined.
func hasCredential(u string) bool {
	_, rest, _ := strings.Cut(u, "://")
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	if strings.Contains(authority, "@") {
		return true
	}
	i := strings.IndexAny(rest, "?#")
	if i < 0 {
		return false
	}
	params := strings.FieldsFunc(rest[i+1:], func(r rune) bool { return strings.ContainsRune("&;?#", r) })
	for _, p := range params {
		name, _, _ := strings.Cut(p, "=")
		name, err := url.QueryUnescape(name)
		if err != nil {
			return true
		}
		name = strings.ToLower(name)
		for _, c := range credentialParams {
			if strings.Contains(name, c) {
				return true
			}
		}
	}
	return false
}
