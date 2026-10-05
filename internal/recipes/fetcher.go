// Package recipes fetches and holds real recipe pages that ground dinners.
package recipes

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// UserAgent identifies MealQuest to the recipe sites it fetches.
	UserAgent = "MealQuest/1.0 (household meal planner; recipe fetcher)"
	// MaxBodyBytes caps a fetched body. A larger page is an error, never
	// silently truncated: a cut-off recipe page would parse as a wrong one.
	MaxBodyBytes = 2 << 20
	// MaxRedirects is how many redirects a fetch follows; each hop is
	// re-checked for scheme and address.
	MaxRedirects = 3
	// Timeout bounds a whole fetch: rate-limit waits, every redirect hop
	// and reading the body.
	Timeout = 10 * time.Second
	// HostInterval is the least time between two requests to one host.
	HostInterval = time.Second
	// CacheTTL is how long a fetched page is served from the cache.
	CacheTTL = time.Hour
	// CacheSize is how many pages the cache holds; the oldest is evicted
	// first.
	CacheSize = 64
)

var (
	// ErrNotHTTPS is returned for a URL, or a redirect, that is not
	// https://.
	ErrNotHTTPS = errors.New("recipes: only https:// URLs are fetched")
	// ErrBlockedAddress is returned when a host resolves to an address
	// that is not public, such as loopback or a private network.
	ErrBlockedAddress = errors.New("recipes: address is not public")
	// ErrTooManyRedirects is returned after MaxRedirects redirects.
	ErrTooManyRedirects = errors.New("recipes: too many redirects")
	// ErrTooLarge is returned for a body over MaxBodyBytes.
	ErrTooLarge = errors.New("recipes: body exceeds size cap")
	// ErrTimeout is returned when a fetch runs past Timeout.
	ErrTimeout = errors.New("recipes: fetch timed out")
)

// StatusError is returned for a final response whose status is not 2xx.
type StatusError struct {
	URL    string
	Status int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("recipes: GET %s: HTTP %d", e.URL, e.Status)
}

// Page is a fetched page.
type Page struct {
	// URL is the final URL, after redirects.
	URL         string
	ContentType string
	Body        []byte
}

// Resolver looks up a host's addresses; *net.Resolver satisfies it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// dialFunc dials an address that has already passed the public-address
// check.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Fetcher retrieves recipe pages without reaching private networks. Its
// dialer resolves each host itself, refuses the connection unless every
// answer is a public address, and dials the checked address, so a second DNS
// answer cannot swap in a private one. Keep-alives are off: every request,
// redirect hops included, dials and checks afresh. Proxies from the
// environment are not used, since a proxy would dial on the Fetcher's
// behalf. A Fetcher is safe for concurrent use.
type Fetcher struct {
	client   *http.Client
	resolver Resolver
	dial     dialFunc
	// timeout bounds a fetch; tests shorten it.
	timeout time.Duration
	// now is the clock; tests replace it.
	now func() time.Time
	// sleep waits d or until ctx is done; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	nextSlot map[string]time.Time
	cache    map[string]cacheEntry
}

type cacheEntry struct {
	page    Page
	fetched time.Time
}

// NewFetcher returns a Fetcher that resolves through the system resolver.
func NewFetcher() *Fetcher {
	return newFetcher(net.DefaultResolver, (&net.Dialer{}).DialContext, nil)
}

// newFetcher returns a Fetcher over resolver and dial; tlsConfig, when not
// nil, replaces the default TLS settings so tests can trust their servers.
func newFetcher(resolver Resolver, dial dialFunc, tlsConfig *tls.Config) *Fetcher {
	f := &Fetcher{
		resolver: resolver,
		dial:     dial,
		timeout:  Timeout,
		now:      time.Now,
		sleep:    sleep,
		nextSlot: map[string]time.Time{},
		cache:    map[string]cacheEntry{},
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         f.dialPublic,
		TLSClientConfig:     tlsConfig,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: Timeout,
	}
	f.client = &http.Client{
		Transport:     &limitedTransport{f: f, next: transport},
		CheckRedirect: checkRedirect,
	}
	return f
}

// Fetch returns the page at rawURL, from the cache when it was fetched
// within CacheTTL. Only 2xx responses are returned and cached.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Page, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Page{}, fmt.Errorf("recipes: parse URL: %w", err)
	}
	if err := checkURL(u); err != nil {
		return Page{}, err
	}
	key := u.String()
	if page, ok := f.cached(key); ok {
		return page, nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	page, err := f.get(fetchCtx, key)
	if err != nil {
		if ctx.Err() == nil && errors.Is(fetchCtx.Err(), context.DeadlineExceeded) {
			return Page{}, fmt.Errorf("%w after %s: %s", ErrTimeout, f.timeout, key)
		}
		return Page{}, err
	}
	f.store(key, page)
	return page, nil
}

func (f *Fetcher) get(ctx context.Context, rawURL string) (Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Page{}, fmt.Errorf("recipes: build request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := f.client.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	final := resp.Request.URL.String()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Page{}, &StatusError{URL: final, Status: resp.StatusCode}
	}
	if resp.ContentLength > MaxBodyBytes {
		return Page{}, fmt.Errorf("%w: %s declares %d bytes", ErrTooLarge, final, resp.ContentLength)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return Page{}, fmt.Errorf("recipes: read %s: %w", final, err)
	}
	if len(body) > MaxBodyBytes {
		return Page{}, fmt.Errorf("%w: %s is over %d bytes", ErrTooLarge, final, MaxBodyBytes)
	}
	return Page{URL: final, ContentType: resp.Header.Get("Content-Type"), Body: body}, nil
}

// cached returns the page cached under key if it is younger than CacheTTL.
func (f *Fetcher) cached(key string) (Page, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache[key]
	if !ok {
		return Page{}, false
	}
	if f.now().Sub(e.fetched) >= CacheTTL {
		delete(f.cache, key)
		return Page{}, false
	}
	return e.page, true
}

// store caches page under key, evicting the oldest entry when full.
func (f *Fetcher) store(key string, page Page) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.cache[key]; !ok && len(f.cache) >= CacheSize {
		var oldest string
		var oldestAt time.Time
		for k, e := range f.cache {
			if oldest == "" || e.fetched.Before(oldestAt) {
				oldest, oldestAt = k, e.fetched
			}
		}
		delete(f.cache, oldest)
	}
	f.cache[key] = cacheEntry{page: page, fetched: f.now()}
}

// waitTurn blocks until host's next request slot, keeping requests to one
// host at least HostInterval apart.
func (f *Fetcher) waitTurn(ctx context.Context, host string) error {
	f.mu.Lock()
	now := f.now()
	slot := f.nextSlot[host]
	if slot.Before(now) {
		slot = now
	}
	f.nextSlot[host] = slot.Add(HostInterval)
	f.mu.Unlock()
	if wait := slot.Sub(now); wait > 0 {
		return f.sleep(ctx, wait)
	}
	return nil
}

// dialPublic resolves address's host once, refuses unless every answer is
// public, then dials the checked addresses in turn.
func (f *Fetcher) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("recipes: dial address %q: %w", address, err)
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		answers, err := f.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("recipes: resolve %s: %w", host, err)
		}
		for _, a := range answers {
			ip, ok := netip.AddrFromSlice(a.IP)
			if !ok {
				return nil, fmt.Errorf("recipes: resolve %s: malformed address %v", host, a.IP)
			}
			addrs = append(addrs, ip)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("recipes: resolve %s: no addresses", host)
	}
	for _, ip := range addrs {
		if !IsPublic(ip) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip)
		}
	}
	var errs []error
	for _, ip := range addrs {
		conn, err := f.dial(ctx, network, net.JoinHostPort(ip.Unmap().WithZone("").String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// limitedTransport waits for each request's per-host rate-limit slot,
// redirect hops included, before sending it.
type limitedTransport struct {
	f    *Fetcher
	next http.RoundTripper
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.f.waitTurn(req.Context(), strings.ToLower(req.URL.Hostname())); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

// checkURL refuses anything but an absolute https:// URL with a host.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("%w: %q", ErrNotHTTPS, u.Redacted())
	}
	if u.Hostname() == "" {
		return fmt.Errorf("recipes: URL %q has no host", u.Redacted())
	}
	return nil
}

// checkRedirect applies checkURL to every hop and stops after
// MaxRedirects. The hop's address is checked when it is dialed.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return fmt.Errorf("%w: more than %d", ErrTooManyRedirects, MaxRedirects)
	}
	return checkURL(req.URL)
}

// blockedPrefixes are the non-public ranges. IPv4-mapped IPv6 addresses
// are unmapped before the check, so the IPv4 entries cover them too.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // unspecified, "this network"
	netip.MustParsePrefix("10.0.0.0/8"),      // RFC 1918
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("127.0.0.0/8"),     // loopback
	netip.MustParsePrefix("169.254.0.0/16"),  // link-local
	netip.MustParsePrefix("172.16.0.0/12"),   // RFC 1918
	netip.MustParsePrefix("192.168.0.0/16"),  // RFC 1918
	netip.MustParsePrefix("224.0.0.0/4"),     // multicast
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast
	netip.MustParsePrefix("::/96"),           // unspecified, loopback, IPv4-compatible
	netip.MustParsePrefix("fc00::/7"),        // unique local
	netip.MustParsePrefix("fe80::/10"),       // link-local
	netip.MustParsePrefix("fec0::/10"),       // site-local (deprecated)
	netip.MustParsePrefix("ff00::/8"),        // multicast
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64, embeds any IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("2002::/16"),       // 6to4, embeds any IPv4
	netip.MustParsePrefix("2001::/32"),       // Teredo, embeds any IPv4
	netip.MustParsePrefix("::ffff:0:0:0/96"), // IPv4-translated
}

// IsPublic reports whether ip is a public unicast address the Fetcher may
// dial.
func IsPublic(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap().WithZone("")
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// sleep waits d or until ctx is done, returning ctx's error if it is.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
