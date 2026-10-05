package recipes

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubResolver answers each host from a list of answers, one per lookup;
// the last answer repeats.
type stubResolver struct {
	mu      sync.Mutex
	answers map[string][][]string
	calls   map[string]int
}

func (r *stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq, ok := r.answers[host]
	if !ok {
		return nil, fmt.Errorf("stub: no such host %s", host)
	}
	n := r.calls[host]
	r.calls[host]++
	if n >= len(seq) {
		n = len(seq) - 1
	}
	var out []net.IPAddr
	for _, s := range seq[n] {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

func (r *stubResolver) lookups(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

// testEnv is a Fetcher whose public test addresses all reach one httptest
// TLS server, and which records every address it dials.
type testEnv struct {
	f        *Fetcher
	resolver *stubResolver
	server   *httptest.Server

	mu     sync.Mutex
	dialed []string
}

// publicIP is a documentation address the tests treat as a public host.
const publicIP = "203.0.113.10"

func newTestEnv(t *testing.T, handler http.Handler, answers map[string][][]string) *testEnv {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	env := &testEnv{
		resolver: &stubResolver{answers: answers, calls: map[string]int{}},
		server:   server,
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		env.mu.Lock()
		env.dialed = append(env.dialed, address)
		env.mu.Unlock()
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(host, "203.0.113.") {
			var d net.Dialer
			return d.DialContext(ctx, network, server.Listener.Addr().String())
		}
		return nil, fmt.Errorf("test dial: no route to %s", address)
	}
	env.f = newFetcher(env.resolver, dial, &tls.Config{RootCAs: pool})
	// Real requests in these tests are not rate-limited unless a test
	// installs a fake clock.
	env.f.sleep = func(context.Context, time.Duration) error { return nil }
	return env
}

func (e *testEnv) dials() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.dialed...)
}

func publicAnswers() map[string][][]string {
	return map[string][][]string{"example.com": {{publicIP}}}
}

func TestFetchReturnsPage(t *testing.T) {
	t.Parallel()
	var gotUA string
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>tacos</html>")
	}), publicAnswers())

	page, err := env.f.Fetch(t.Context(), "https://example.com/tacos")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(page.Body) != "<html>tacos</html>" || page.ContentType != "text/html" || page.URL != "https://example.com/tacos" {
		t.Errorf("page = %+v", page)
	}
	if gotUA != UserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, UserAgent)
	}
	if got := env.dials(); len(got) != 1 || got[0] != publicIP+":443" {
		t.Errorf("dialed %v, want [%s:443]", got, publicIP)
	}
}

func TestFetchRejectsNonHTTPS(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.NotFoundHandler(), publicAnswers())
	for _, raw := range []string{"http://example.com/tacos", "ftp://example.com/", "example.com/tacos", "https:///tacos"} {
		_, err := env.f.Fetch(t.Context(), raw)
		if err == nil {
			t.Errorf("Fetch(%q) succeeded", raw)
		}
		if strings.HasPrefix(raw, "http://") && !errors.Is(err, ErrNotHTTPS) {
			t.Errorf("Fetch(%q) error = %v, want ErrNotHTTPS", raw, err)
		}
	}
	if got := env.dials(); len(got) != 0 {
		t.Errorf("dialed %v, want nothing", got)
	}
	if n := env.resolver.lookups("example.com"); n != 0 {
		t.Errorf("resolved example.com %d times, want 0", n)
	}
}

// blockedIPs has an address in every refused range, including IPv4-mapped
// forms of the IPv4 ranges.
var blockedIPs = []string{
	"127.0.0.1", "127.255.255.254", "::1", "::ffff:127.0.0.1", // loopback
	"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1", // RFC 1918
	"::ffff:10.1.2.3", "::ffff:172.20.0.1", "::ffff:192.168.0.10",
	"169.254.169.254", "::ffff:169.254.169.254", "fe80::1", // link-local
	"100.64.0.1", "100.127.255.254", "::ffff:100.100.0.1", // carrier-grade NAT
	"224.0.0.1", "239.255.255.250", "ff02::1", "::ffff:224.0.0.251", // multicast
	"0.0.0.0", "0.1.2.3", "::", "::ffff:0.0.0.0", // unspecified
	"fc00::1", "fd12:3456:789a::1", // unique local
	"255.255.255.255", "240.0.0.1", "fec0::1",
	"64:ff9b::a00:1", "2002:a00:1::1", "2001:0:a00:1::1", "::a00:1",
}

func TestIsPublic(t *testing.T) {
	t.Parallel()
	for _, s := range blockedIPs {
		if IsPublic(netip.MustParseAddr(s)) {
			t.Errorf("IsPublic(%s) = true, want false", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "203.0.113.10", "100.63.255.255", "100.128.0.1", "172.32.0.1", "2606:4700::1111", "::ffff:8.8.8.8"} {
		if !IsPublic(netip.MustParseAddr(s)) {
			t.Errorf("IsPublic(%s) = false, want true", s)
		}
	}
	if IsPublic(netip.Addr{}) {
		t.Error("IsPublic(zero Addr) = true, want false")
	}
}

func TestFetchRefusesEachBlockedRange(t *testing.T) {
	t.Parallel()
	for _, ip := range blockedIPs {
		t.Run(ip, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, http.NotFoundHandler(), map[string][][]string{"example.com": {{ip}}})
			_, err := env.f.Fetch(t.Context(), "https://example.com/")
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("Fetch error = %v, want ErrBlockedAddress", err)
			}
			if got := env.dials(); len(got) != 0 {
				t.Errorf("dialed %v, want nothing", got)
			}
		})
	}
}

func TestFetchRefusesBlockedLiteralHost(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.NotFoundHandler(), nil)
	for _, raw := range []string{"https://127.0.0.1/", "https://[::1]/", "https://[::ffff:10.0.0.1]/", "https://169.254.169.254/latest/meta-data/", "https://[fe80::1%25eth0]/"} {
		if _, err := env.f.Fetch(t.Context(), raw); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("Fetch(%q) error = %v, want ErrBlockedAddress", raw, err)
		}
	}
	if got := env.dials(); len(got) != 0 {
		t.Errorf("dialed %v, want nothing", got)
	}
}

func TestFetchRefusesMixedAnswer(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.NotFoundHandler(), map[string][][]string{"example.com": {{publicIP, "10.0.0.1"}}})
	if _, err := env.f.Fetch(t.Context(), "https://example.com/"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("Fetch error = %v, want ErrBlockedAddress", err)
	}
	if got := env.dials(); len(got) != 0 {
		t.Errorf("dialed %v, want nothing", got)
	}
}

func TestFetchRefusesRedirectToPrivateHost(t *testing.T) {
	t.Parallel()
	answers := map[string][][]string{
		"example.com":      {{publicIP}},
		"evil.example.com": {{"10.0.0.5"}},
	}
	for _, target := range []string{"https://evil.example.com/admin", "https://127.0.0.1/admin", "https://[::ffff:192.168.0.1]/"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, http.RedirectHandler(target, http.StatusFound), answers)
			_, err := env.f.Fetch(t.Context(), "https://example.com/tacos")
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("Fetch error = %v, want ErrBlockedAddress", err)
			}
			if got := env.dials(); len(got) != 1 || got[0] != publicIP+":443" {
				t.Errorf("dialed %v, want only the first hop", got)
			}
		})
	}
}

func TestFetchRefusesRedirectToHTTP(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.RedirectHandler("http://example.com/plain", http.StatusMovedPermanently), publicAnswers())
	if _, err := env.f.Fetch(t.Context(), "https://example.com/"); !errors.Is(err, ErrNotHTTPS) {
		t.Fatalf("Fetch error = %v, want ErrNotHTTPS", err)
	}
}

// chainHandler redirects /hop/N to /hop/N-1 until /hop/0, which serves a
// page.
var chainHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	var n int
	if _, err := fmt.Sscanf(r.URL.Path, "/hop/%d", &n); err != nil {
		http.NotFound(w, r)
		return
	}
	if n == 0 {
		fmt.Fprint(w, "arrived")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("https://recipes.example.com/hop/%d", n-1), http.StatusFound)
})

func TestFetchFollowsAtMostThreeRedirects(t *testing.T) {
	t.Parallel()
	answers := map[string][][]string{
		"example.com":         {{publicIP}},
		"recipes.example.com": {{"203.0.113.20"}},
	}
	env := newTestEnv(t, chainHandler, answers)
	page, err := env.f.Fetch(t.Context(), "https://example.com/hop/3")
	if err != nil {
		t.Fatalf("Fetch with 3 redirects: %v", err)
	}
	if page.URL != "https://recipes.example.com/hop/0" || string(page.Body) != "arrived" {
		t.Errorf("page = %+v", page)
	}
	// Every hop is dialed, so every hop's address is checked.
	if got := env.dials(); len(got) != 4 || got[0] != publicIP+":443" || got[3] != "203.0.113.20:443" {
		t.Errorf("dialed %v, want 4 checked hops", got)
	}

	if _, err := env.f.Fetch(t.Context(), "https://example.com/hop/4"); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("Fetch with 4 redirects error = %v, want ErrTooManyRedirects", err)
	}
}

// TestFetchRefusesDNSSwitchToPrivate covers DNS rebinding: the host's first
// answer is public and every later one private. The first fetch dials the
// address it checked; the next lookup's private answer is refused, and the
// private address is never dialed.
func TestFetchRefusesDNSSwitchToPrivate(t *testing.T) {
	t.Parallel()
	answers := map[string][][]string{"example.com": {{publicIP}, {"127.0.0.1"}}}
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}), answers)

	if _, err := env.f.Fetch(t.Context(), "https://example.com/one"); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if n := env.resolver.lookups("example.com"); n != 1 {
		t.Fatalf("first Fetch resolved %d times, want 1 (checked IP must be dialed IP)", n)
	}
	_, err := env.f.Fetch(t.Context(), "https://example.com/two")
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("second Fetch error = %v, want ErrBlockedAddress", err)
	}
	if got := env.dials(); len(got) != 1 || got[0] != publicIP+":443" {
		t.Errorf("dialed %v, want only %s:443", got, publicIP)
	}
}

func TestFetchSizeCap(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := MaxBodyBytes
		if r.URL.Path != "/exact" {
			size++
		}
		if r.URL.Path == "/declared" {
			w.Header().Set("Content-Length", fmt.Sprint(size))
		}
		w.Write([]byte(strings.Repeat("a", size)))
	}), publicAnswers())

	page, err := env.f.Fetch(t.Context(), "https://example.com/exact")
	if err != nil {
		t.Fatalf("Fetch at the cap: %v", err)
	}
	if len(page.Body) != MaxBodyBytes {
		t.Errorf("body is %d bytes, want %d", len(page.Body), MaxBodyBytes)
	}
	for _, path := range []string{"/declared", "/chunked"} {
		if _, err := env.f.Fetch(t.Context(), "https://example.com"+path); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Fetch %s error = %v, want ErrTooLarge", path, err)
		}
	}
}

func TestFetchTimeout(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow-body" {
			w.Write([]byte("start"))
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	}), publicAnswers())
	env.f.timeout = 100 * time.Millisecond

	for _, path := range []string{"/slow-headers", "/slow-body"} {
		start := time.Now()
		_, err := env.f.Fetch(t.Context(), "https://example.com"+path)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("Fetch %s error = %v, want ErrTimeout", path, err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("Fetch %s took %s", path, elapsed)
		}
	}
	if Timeout != 10*time.Second {
		t.Errorf("Timeout = %s, want 10s", Timeout)
	}
}

func TestFetchCallerCancelIsNotTimeout(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, http.NotFoundHandler(), publicAnswers())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := env.f.Fetch(ctx, "https://example.com/")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrTimeout) {
		t.Fatalf("Fetch error = %v, want context.Canceled", err)
	}
}

// fakeClock is a clock whose sleep advances it and records the wait.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	return nil
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

func installClock(f *Fetcher) *fakeClock {
	c := &fakeClock{t: time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)}
	f.now = c.now
	f.sleep = c.sleep
	return c
}

func TestFetchCachesByURL(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	hits := map[string]int{}
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		n := hits[r.URL.Path]
		mu.Unlock()
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "%s #%d", r.URL.Path, n)
	}), publicAnswers())
	clock := installClock(env.f)

	fetch := func(path string) string {
		t.Helper()
		page, err := env.f.Fetch(t.Context(), "https://example.com"+path)
		if err != nil {
			t.Fatalf("Fetch %s: %v", path, err)
		}
		return string(page.Body)
	}
	if got := fetch("/a"); got != "/a #1" {
		t.Errorf("first /a = %q", got)
	}
	if got := fetch("/a"); got != "/a #1" {
		t.Errorf("cached /a = %q, want the first response", got)
	}
	if got := fetch("/b"); got != "/b #1" {
		t.Errorf("/b = %q, want its own response", got)
	}
	clock.advance(CacheTTL)
	if got := fetch("/a"); got != "/a #2" {
		t.Errorf("/a after CacheTTL = %q, want a refetch", got)
	}

	for range 2 {
		var se *StatusError
		if _, err := env.f.Fetch(t.Context(), "https://example.com/missing"); !errors.As(err, &se) || se.Status != http.StatusNotFound {
			t.Fatalf("Fetch /missing error = %v, want StatusError 404", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["/missing"] != 2 {
		t.Errorf("/missing was requested %d times, want 2 (errors are not cached)", hits["/missing"])
	}
}

func TestCacheEvictsOldest(t *testing.T) {
	t.Parallel()
	f := newFetcher(&stubResolver{}, nil, nil)
	clock := installClock(f)
	for i := range CacheSize + 1 {
		f.store(fmt.Sprintf("https://example.com/%d", i), Page{})
		clock.advance(time.Second)
	}
	if len(f.cache) != CacheSize {
		t.Errorf("cache holds %d pages, want %d", len(f.cache), CacheSize)
	}
	if _, ok := f.cached("https://example.com/0"); ok {
		t.Error("oldest page was not evicted")
	}
	if _, ok := f.cached(fmt.Sprintf("https://example.com/%d", CacheSize)); !ok {
		t.Error("newest page is missing")
	}
}

func TestFetchRateLimitsPerHost(t *testing.T) {
	t.Parallel()
	answers := map[string][][]string{
		"example.com":         {{publicIP}},
		"recipes.example.com": {{"203.0.113.20"}},
	}
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}), answers)
	clock := installClock(env.f)

	for _, raw := range []string{"https://example.com/1", "https://recipes.example.com/1", "https://example.com/2", "https://example.com/3"} {
		if _, err := env.f.Fetch(t.Context(), raw); err != nil {
			t.Fatalf("Fetch %s: %v", raw, err)
		}
	}
	// example.com/2 waits a full interval; example.com/3 waits another.
	// recipes.example.com has its own slot and never waits.
	got := clock.waits()
	if len(got) != 2 || got[0] != HostInterval || got[1] != HostInterval {
		t.Errorf("waits = %v, want [%s %s]", got, HostInterval, HostInterval)
	}
}
