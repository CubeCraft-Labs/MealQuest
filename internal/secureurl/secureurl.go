// Package secureurl holds the one transport rule for URLs that carry
// credentials or session cookies: HTTPS, or plain HTTP to a loopback host for
// local development and tests.
package secureurl

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// loopbackHosts are the only hosts plain HTTP is allowed for, compared
// case-insensitively. url.URL's Hostname strips the brackets from "[::1]".
var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// Check reports an error unless u is an absolute https:// URL, or an http://
// URL whose host is localhost, 127.0.0.1 or ::1.
func Check(u *url.URL) error {
	switch {
	case u.Hostname() == "":
		return errors.New("URL has no host")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && loopbackHosts[strings.ToLower(u.Hostname())]:
		return nil
	case u.Scheme == "http":
		return errors.New("plain http:// is allowed only for localhost, 127.0.0.1 or [::1]; use https://")
	default:
		return fmt.Errorf("unsupported scheme %q (want https://)", u.Scheme)
	}
}
