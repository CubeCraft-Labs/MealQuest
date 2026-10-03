// Package forwarded resolves where a request really came from. The service
// runs behind a reverse proxy (Traefik), so the TCP peer is usually the proxy;
// the X-Forwarded-For and X-Forwarded-Proto headers it adds are believed only
// when the peer is a trusted proxy. From anyone else they are ignored, so a
// client cannot spoof its address or scheme.
package forwarded

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
)

// Header names honoured from trusted proxies.
const (
	HeaderFor   = "X-Forwarded-For"
	HeaderProto = "X-Forwarded-Proto"
)

// ErrInvalidHeader marks a malformed forwarded header from a trusted peer.
var ErrInvalidHeader = errors.New("invalid forwarded header")

// Trusted is the set of proxy addresses whose forwarded headers are honoured.
// The zero value trusts no peer.
type Trusted []netip.Prefix

// ParseTrusted parses a comma-separated list of IP addresses and CIDR
// prefixes. A blank list trusts no peer. Each entry must be a plain address
// or a prefix with no host bits set; zones, IPv4-mapped IPv6 forms and
// zero-length prefixes, which would trust every peer, are rejected.
func ParseTrusted(s string) (Trusted, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var t Trusted
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("empty entry in comma-separated list %q", s)
		}
		p, err := parseEntry(entry)
		if err != nil {
			return nil, err
		}
		t = append(t, p)
	}
	return t, nil
}

func parseEntry(entry string) (netip.Prefix, error) {
	var p netip.Prefix
	if strings.Contains(entry, "/") {
		var err error
		p, err = netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid CIDR %q", entry)
		}
		if p != p.Masked() {
			return netip.Prefix{}, fmt.Errorf("CIDR %q has host bits set (did you mean %s?)", entry, p.Masked())
		}
	} else {
		a, err := netip.ParseAddr(entry)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid IP address %q", entry)
		}
		if a.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("IP address %q must not have a zone", entry)
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	switch {
	case p.Addr().Is4In6():
		return netip.Prefix{}, fmt.Errorf("%q is IPv4-mapped IPv6; write the IPv4 form", entry)
	case p.Bits() == 0:
		return netip.Prefix{}, fmt.Errorf("%q would trust every peer", entry)
	}
	return p, nil
}

// Contains reports whether a is a trusted proxy address. IPv4-mapped IPv6
// addresses, as a dual-stack listener reports IPv4 peers, match their IPv4
// form.
func (t Trusted) Contains(a netip.Addr) bool {
	a = normalize(a)
	for _, p := range t {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func normalize(a netip.Addr) netip.Addr { return a.Unmap().WithZone("") }

// Origin is where a request came from.
type Origin struct {
	// ClientIP is the client's address.
	ClientIP netip.Addr
	// Scheme is "https" or "http": the scheme the client used.
	Scheme string
}

// Resolve works out r's Origin. When the TCP peer (r.RemoteAddr) is not in
// trusted, both forwarded headers are ignored: the client is the peer and the
// scheme is that of the connection.
//
// When the peer is trusted:
//   - X-Forwarded-For entries (across all its header lines, in order) are
//     walked right to left, skipping trusted proxy addresses; the first
//     untrusted address is the client. Entries left of it are client-supplied
//     and not examined. If every entry is trusted the leftmost is the client;
//     with no X-Forwarded-For the peer is the client.
//   - X-Forwarded-Proto, if present, must be a single "http" or "https" and
//     is the scheme; without it the connection's scheme is used.
//
// A malformed header from a trusted peer is an error wrapping
// ErrInvalidHeader; any other error means RemoteAddr is not an IP and port.
func Resolve(r *http.Request, trusted Trusted) (Origin, error) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return Origin{}, fmt.Errorf("peer address %q is not an IP and port", r.RemoteAddr)
	}
	o := Origin{ClientIP: normalize(peer.Addr()), Scheme: "http"}
	if r.TLS != nil {
		o.Scheme = "https"
	}
	if !trusted.Contains(o.ClientIP) {
		return o, nil
	}
	if values := r.Header.Values(HeaderFor); len(values) > 0 {
		o.ClientIP, err = clientFromChain(strings.Split(strings.Join(values, ","), ","), trusted)
		if err != nil {
			return Origin{}, err
		}
	}
	switch values := r.Header.Values(HeaderProto); len(values) {
	case 0:
	case 1:
		switch scheme := strings.ToLower(strings.TrimSpace(values[0])); scheme {
		case "http", "https":
			o.Scheme = scheme
		default:
			return Origin{}, fmt.Errorf("%w: %s %q is not http or https", ErrInvalidHeader, HeaderProto, values[0])
		}
	default:
		return Origin{}, fmt.Errorf("%w: %d %s headers, want one", ErrInvalidHeader, len(values), HeaderProto)
	}
	return o, nil
}

// clientFromChain returns the rightmost untrusted address in chain, or the
// leftmost address when all are trusted.
func clientFromChain(chain []string, trusted Trusted) (netip.Addr, error) {
	var client netip.Addr
	for i := len(chain) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return netip.Addr{}, fmt.Errorf("%w: %s entry %q is not an IP address", ErrInvalidHeader, HeaderFor, strings.TrimSpace(chain[i]))
		}
		client = normalize(a)
		if !trusted.Contains(client) {
			break
		}
	}
	return client, nil
}

type contextKey struct{}

// NewContext returns ctx carrying o.
func NewContext(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, contextKey{}, o)
}

// FromContext returns the Origin ctx carries; ok is false when the request
// was not resolved.
func FromContext(ctx context.Context) (o Origin, ok bool) {
	o, ok = ctx.Value(contextKey{}).(Origin)
	return o, ok
}

// ClientIP returns the resolved client address ctx carries.
func ClientIP(ctx context.Context) (netip.Addr, bool) {
	o, ok := FromContext(ctx)
	return o.ClientIP, ok
}

// Scheme returns the resolved request scheme ctx carries.
func Scheme(ctx context.Context) (string, bool) {
	o, ok := FromContext(ctx)
	return o.Scheme, ok
}

// LogAttr is the log attribute key for the resolved client address.
const LogAttr = "client_ip"

// LogHandler wraps h so every record logged with a resolved request's
// context carries the client address as LogAttr.
func LogHandler(h slog.Handler) slog.Handler { return logHandler{h} }

type logHandler struct{ slog.Handler }

func (h logHandler) Handle(ctx context.Context, rec slog.Record) error {
	if ip, ok := ClientIP(ctx); ok {
		rec = rec.Clone()
		rec.AddAttrs(slog.String(LogAttr, ip.String()))
	}
	return h.Handler.Handle(ctx, rec)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{h.Handler.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{h.Handler.WithGroup(name)}
}
