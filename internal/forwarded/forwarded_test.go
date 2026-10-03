package forwarded

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestParseTrusted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    []string
		wantErr string
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		{in: "10.60.1.33", want: []string{"10.60.1.33/32"}},
		{in: "10.60.1.33, 172.16.0.0/12", want: []string{"10.60.1.33/32", "172.16.0.0/12"}},
		{in: "2001:db8::1,2001:db8::/32", want: []string{"2001:db8::1/128", "2001:db8::/32"}},
		{in: "traefik", wantErr: "invalid IP address"},
		{in: "10.60.1.33:443", wantErr: "invalid IP address"},
		{in: "10.60.1.0/33", wantErr: "invalid CIDR"},
		{in: "10.60.1.33/24", wantErr: "did you mean 10.60.1.0/24"},
		{in: "10.60.1.33,,10.60.1.34", wantErr: "empty entry"},
		{in: "10.60.1.33,", wantErr: "empty entry"},
		{in: "fe80::1%eth0", wantErr: "zone"},
		{in: "::ffff:10.60.1.33", wantErr: "IPv4-mapped"},
		{in: "0.0.0.0/0", wantErr: "every peer"},
		{in: "::/0", wantErr: "every peer"},
	}
	for _, tt := range tests {
		got, err := ParseTrusted(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseTrusted(%q) error = %v, want one containing %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTrusted(%q) error = %v", tt.in, err)
			continue
		}
		var gotStr []string
		for _, p := range got {
			gotStr = append(gotStr, p.String())
		}
		if !slices.Equal(gotStr, tt.want) {
			t.Errorf("ParseTrusted(%q) = %v, want %v", tt.in, gotStr, tt.want)
		}
	}
}

func TestTrustedContainsMappedIPv4(t *testing.T) {
	t.Parallel()
	trusted := mustTrusted(t, "10.60.1.33")
	if !trusted.Contains(netip.MustParseAddr("::ffff:10.60.1.33")) {
		t.Error("IPv4-mapped form of a trusted address is not trusted")
	}
	if Trusted(nil).Contains(netip.MustParseAddr("10.60.1.33")) {
		t.Error("an empty set trusts a peer")
	}
}

func mustTrusted(t *testing.T, s string) Trusted {
	t.Helper()
	tr, err := ParseTrusted(s)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestResolve(t *testing.T) {
	t.Parallel()
	// 10.60.1.33 is Traefik; 10.60.2.0/24 is an inner proxy tier.
	trusted := mustTrusted(t, "10.60.1.33,10.60.2.0/24")
	tests := []struct {
		name       string
		peer       string
		tls        bool
		xff        []string
		xfp        []string
		wantIP     string
		wantScheme string
	}{
		{
			name: "untrusted peer, no headers",
			peer: "198.51.100.9:5000", wantIP: "198.51.100.9", wantScheme: "http",
		},
		{
			name: "untrusted peer over TLS",
			peer: "198.51.100.9:5000", tls: true, wantIP: "198.51.100.9", wantScheme: "https",
		},
		{
			name: "untrusted peer spoofing both headers is ignored",
			peer: "198.51.100.9:5000", xff: []string{"203.0.113.7"}, xfp: []string{"https"},
			wantIP: "198.51.100.9", wantScheme: "http",
		},
		{
			name: "untrusted peer's malformed headers are ignored, not rejected",
			peer: "198.51.100.9:5000", xff: []string{"not-an-ip"}, xfp: []string{"gopher"},
			wantIP: "198.51.100.9", wantScheme: "http",
		},
		{
			name: "trusted peer: client from X-Forwarded-For, scheme from X-Forwarded-Proto",
			peer: "10.60.1.33:41000", xff: []string{"203.0.113.7"}, xfp: []string{"https"},
			wantIP: "203.0.113.7", wantScheme: "https",
		},
		{
			name: "trusted peer seen as IPv4-mapped IPv6",
			peer: "[::ffff:10.60.1.33]:41000", xff: []string{"203.0.113.7"}, xfp: []string{"https"},
			wantIP: "203.0.113.7", wantScheme: "https",
		},
		{
			name: "trusted peer without headers is itself the client",
			peer: "10.60.1.33:41000", wantIP: "10.60.1.33", wantScheme: "http",
		},
		{
			name: "rightmost untrusted address wins over a client-supplied prefix",
			peer: "10.60.1.33:41000", xff: []string{"1.2.3.4, 203.0.113.7"}, xfp: []string{"https"},
			wantIP: "203.0.113.7", wantScheme: "https",
		},
		{
			name: "trusted proxies in the chain are skipped",
			peer: "10.60.1.33:41000", xff: []string{"1.2.3.4, 203.0.113.7, 10.60.2.5"}, xfp: []string{"https"},
			wantIP: "203.0.113.7", wantScheme: "https",
		},
		{
			name: "several header lines form one chain",
			peer: "10.60.1.33:41000", xff: []string{"1.2.3.4", "203.0.113.7, 10.60.2.5"},
			wantIP: "203.0.113.7", wantScheme: "http",
		},
		{
			name: "garbage left of the client is not examined",
			peer: "10.60.1.33:41000", xff: []string{"spoofed, 203.0.113.7"},
			wantIP: "203.0.113.7", wantScheme: "http",
		},
		{
			name: "all trusted: leftmost is the client",
			peer: "10.60.1.33:41000", xff: []string{"10.60.2.5, 10.60.2.6"},
			wantIP: "10.60.2.5", wantScheme: "http",
		},
		{
			name: "IPv6 client",
			peer: "10.60.1.33:41000", xff: []string{"2001:db8::7"}, xfp: []string{"HTTPS"},
			wantIP: "2001:db8::7", wantScheme: "https",
		},
		{
			name: "trusted peer may downgrade to http",
			peer: "10.60.1.33:41000", tls: true, xfp: []string{"http"},
			wantIP: "10.60.1.33", wantScheme: "http",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := request(tt.peer, tt.xff, tt.xfp)
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			o, err := Resolve(r, trusted)
			if err != nil {
				t.Fatalf("Resolve error = %v", err)
			}
			if o.ClientIP.String() != tt.wantIP || o.Scheme != tt.wantScheme {
				t.Errorf("Resolve = %s %s, want %s %s", o.ClientIP, o.Scheme, tt.wantIP, tt.wantScheme)
			}
		})
	}
}

func TestResolveRejectsMalformedHeadersFromTrustedPeer(t *testing.T) {
	t.Parallel()
	trusted := mustTrusted(t, "10.60.1.33")
	for _, tt := range []struct {
		name     string
		xff, xfp []string
	}{
		{"X-Forwarded-For not an IP", []string{"not-an-ip"}, nil},
		{"X-Forwarded-For with a port", []string{"203.0.113.7:5000"}, nil},
		{"X-Forwarded-For empty entry", []string{"203.0.113.7, "}, nil},
		{"X-Forwarded-For empty", []string{""}, nil},
		{"X-Forwarded-Proto unknown scheme", nil, []string{"ftp"}},
		{"X-Forwarded-Proto list", nil, []string{"https, http"}},
		{"X-Forwarded-Proto repeated", nil, []string{"https", "https"}},
	} {
		_, err := Resolve(request("10.60.1.33:41000", tt.xff, tt.xfp), trusted)
		if !errors.Is(err, ErrInvalidHeader) {
			t.Errorf("%s: Resolve error = %v, want ErrInvalidHeader", tt.name, err)
		}
	}
}

func TestResolveBadPeerAddress(t *testing.T) {
	t.Parallel()
	r := request("@", nil, nil)
	_, err := Resolve(r, nil)
	if err == nil || errors.Is(err, ErrInvalidHeader) {
		t.Errorf("Resolve error = %v, want a peer address error", err)
	}
}

func request(peer string, xff, xfp []string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	for _, v := range xff {
		r.Header.Add(HeaderFor, v)
	}
	for _, v := range xfp {
		r.Header.Add(HeaderProto, v)
	}
	return r
}

func TestContextHelpers(t *testing.T) {
	t.Parallel()
	if _, ok := ClientIP(context.Background()); ok {
		t.Error("ClientIP ok on an unresolved context")
	}
	if _, ok := Scheme(context.Background()); ok {
		t.Error("Scheme ok on an unresolved context")
	}
	ctx := NewContext(context.Background(), Origin{ClientIP: netip.MustParseAddr("203.0.113.7"), Scheme: "https"})
	if ip, ok := ClientIP(ctx); !ok || ip.String() != "203.0.113.7" {
		t.Errorf("ClientIP = %s %v, want 203.0.113.7 true", ip, ok)
	}
	if s, ok := Scheme(ctx); !ok || s != "https" {
		t.Errorf("Scheme = %q %v, want https true", s, ok)
	}
}

func TestLogHandler(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(LogHandler(slog.NewTextHandler(&buf, nil))).With("component", "test")
	ctx := NewContext(context.Background(), Origin{ClientIP: netip.MustParseAddr("203.0.113.7"), Scheme: "https"})

	logger.InfoContext(ctx, "resolved")
	logger.InfoContext(context.Background(), "unresolved")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("logged %d lines, want 2: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "client_ip=203.0.113.7") || !strings.Contains(lines[0], "component=test") {
		t.Errorf("resolved line = %q, want client_ip and the With attribute", lines[0])
	}
	if strings.Contains(lines[1], "client_ip") {
		t.Errorf("unresolved line = %q, want no client_ip", lines[1])
	}
}
