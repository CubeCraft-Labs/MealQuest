// Package recipes fetches and holds real recipe pages that ground dinners.
package recipes

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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

	mu sync.Mutex
	// lastSent is when each host's latest request was let through; a
	// request that is never sent hands its slot back.
	lastSent map[string]time.Time
	// released is closed, then replaced, whenever a slot is handed back,
	// waking requests waiting for their turn.
	released chan struct{}
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
		lastSent: map[string]time.Time{},
		released: make(chan struct{}),
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
// within CacheTTL. Only 2xx responses are returned and cached. The
// returned Body is the caller's own copy.
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
	page := e.page
	page.Body = bytes.Clone(page.Body)
	return page, true
}

// store caches a copy of page under key, evicting the oldest entry when
// full.
func (f *Fetcher) store(key string, page Page) {
	page.Body = bytes.Clone(page.Body)
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

// waitTurn blocks until a request to host may be sent, keeping requests to
// one host at least HostInterval apart. A wait holds no reservation, and a
// request ending with ctx before its turn takes no slot. The request let
// through takes the host's slot and must call release if it is not sent,
// so that it delays no other request.
func (f *Fetcher) waitTurn(ctx context.Context, host string) (release func(), err error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f.mu.Lock()
		now := f.now()
		last, sent := f.lastSent[host]
		wait := last.Add(HostInterval).Sub(now)
		if !sent || wait <= 0 {
			f.lastSent[host] = now
			f.mu.Unlock()
			return func() { f.release(host, now, last, sent) }, nil
		}
		released := f.released
		f.mu.Unlock()
		if err := f.waitOrRelease(ctx, wait, released); err != nil {
			return nil, err
		}
	}
}

// release hands back the slot taken at taken for a request that was not
// sent, restoring the host's previous send time, and wakes waiting requests.
func (f *Fetcher) release(host string, taken, prev time.Time, hadPrev bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.lastSent[host].Equal(taken) {
		// A later request already holds the slot.
		return
	}
	if hadPrev {
		f.lastSent[host] = prev
	} else {
		delete(f.lastSent, host)
	}
	close(f.released)
	f.released = make(chan struct{})
}

// waitOrRelease sleeps for d, returning early without error when a slot is
// handed back so the caller can look again.
func (f *Fetcher) waitOrRelease(ctx context.Context, d time.Duration, released <-chan struct{}) error {
	sleepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-released:
			cancel()
		case <-sleepCtx.Done():
		}
	}()
	err := f.sleep(sleepCtx, d)
	select {
	case <-released:
		return nil
	default:
		return err
	}
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
// redirect hops included, before sending it. A request that fails before
// its headers are written was not sent and hands its slot back.
type limitedTransport struct {
	f    *Fetcher
	next http.RoundTripper
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	release, err := t.f.waitTurn(req.Context(), strings.ToLower(req.URL.Hostname()))
	if err != nil {
		return nil, err
	}
	var sent atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { sent.Store(true) }}
	resp, err := t.next.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err != nil && !sent.Load() {
		release()
	}
	return resp, err
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

// blockedIPv4 are the IPv4 special-purpose ranges that are not globally
// reachable (IANA IPv4 Special-Purpose Address Registry). IPv4-mapped IPv6
// addresses are unmapped before the check, so these cover them too.
var blockedIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("10.0.0.0/8"),      // RFC 1918
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("127.0.0.0/8"),     // loopback
	netip.MustParsePrefix("169.254.0.0/16"),  // link-local
	netip.MustParsePrefix("172.16.0.0/12"),   // RFC 1918
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation, TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast (deprecated)
	netip.MustParsePrefix("192.168.0.0/16"),  // RFC 1918
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation, TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation, TEST-NET-3
	netip.MustParsePrefix("224.0.0.0/4"),     // multicast
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast
}

// globalIPv6 is the only IPv6 range dialed: global unicast. Everything
// outside it is refused, including ::, ::1, IPv4-compatible and
// IPv4-translated forms, NAT64 (64:ff9b::/96), discard-only (100::/64),
// unique local (fc00::/7), link-local (fe80::/10) and multicast (ff00::/8).
var globalIPv6 = netip.MustParsePrefix("2000::/3")

// blockedIPv6 are the special-purpose ranges inside global unicast that are
// not public or embed an arbitrary IPv4 address.
var blockedIPv6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/23"),     // IETF protocol assignments, Teredo, benchmarking
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("2002::/16"),     // 6to4, embeds any IPv4
	netip.MustParsePrefix("3fff::/20"),     // documentation
}

// IsPublic reports whether ip is a public unicast address the Fetcher may
// dial. IPv4 addresses are public unless in a special-purpose range; IPv6
// addresses must be global unicast and outside its special-purpose ranges.
func IsPublic(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap().WithZone("")
	blocked := blockedIPv4
	if ip.Is6() {
		if !globalIPv6.Contains(ip) {
			return false
		}
		blocked = blockedIPv6
	}
	for _, p := range blocked {
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
