// Package safehttp provides an HTTP client that only talks to public
// HTTP(S) endpoints. Every hop — initial request, redirect and DNS answer —
// is checked so server-side requests cannot reach loopback, private,
// link-local or device metadata addresses.
package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrBlocked reports an address or URL that is not allowed.
	ErrBlocked = errors.New("safehttp: destination is not a public HTTP(S) address")
	// ErrTooLarge reports a response body over the caller's limit.
	ErrTooLarge = errors.New("safehttp: response exceeds size limit")
)

// Resolver is the subset of net.Resolver needed for DNS checks. It is an
// interface so tests can supply deterministic answers.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Options configures a Client.
type Options struct {
	// AllowPrivate disables the private-network guard. It exists for tests and
	// must not be enabled in production.
	AllowPrivate bool
	// AllowFakeIP permits hostname dials to LazyCat's fake-IP DNS range
	// (198.18.0.0/15) on ports 80 and 443. The platform resolver rewrites
	// public podcast and API hostnames into that range and proxies the
	// connection; without this the SSRF guard would reject every such feed.
	// It does not relax the guard for private, loopback, link-local, metadata
	// or literal IP addresses.
	AllowFakeIP bool
	// Timeout bounds the time to response headers. Callers should also set a
	// context deadline for the full operation.
	Timeout   time.Duration
	UserAgent string
	Resolver  Resolver
}

// Client wraps http.Client with address validation and bounded reads.
type Client struct {
	opts Options
	http *http.Client
}

// New builds a client. Proxy support is deliberately disabled: an environment
// proxy must not become an SSRF escape hatch.
func New(opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "Boai/1.0 (+podcast library)"
	}
	c := &Client{opts: opts}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           c.dialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: opts.Timeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
		ForceAttemptHTTP2:     true,
		ExpectContinueTimeout: time.Second,
	}
	c.http = &http.Client{Transport: transport, CheckRedirect: c.checkRedirect}
	return c
}

// AllowPrivate reports whether the client is in test mode.
func (c *Client) AllowPrivate() bool { return c.opts.AllowPrivate }

// Do validates the URL and delegates to the underlying client. The response
// body is not read; callers must close it.
//
// Idempotent requests are retried twice on transient transport failures.
// Podcast CDNs behind fake-IP proxies occasionally reset a connection or
// return EOF before headers; a fresh DNS lookup and connection usually
// succeeds. Policy errors and caller cancellation are never retried.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("safehttp: request has no URL")
	}
	if _, err := ValidateURL(req.URL.String(), c.opts.AllowPrivate); err != nil {
		return nil, err
	}
	attempts := 1
	if (req.Method == "" || req.Method == http.MethodGet || req.Method == http.MethodHead) &&
		(req.Body == nil || req.Body == http.NoBody) {
		attempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		clone := req.Clone(req.Context())
		if clone.Header.Get("User-Agent") == "" {
			clone.Header.Set("User-Agent", c.opts.UserAgent)
		}
		resp, err := c.http.Do(clone)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == attempts-1 || !retryableTransportError(err) {
			break
		}
		delay := 200 * time.Millisecond
		if attempt == 1 {
			delay = 600 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, lastErr
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func retryableTransportError(err error) bool {
	if err == nil || errors.Is(err, ErrBlocked) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if strings.Contains(err.Error(), "too many redirects") {
		return false
	}
	return true
}

// Get fetches a URL with a bounded body.
func (c *Client) Get(ctx context.Context, rawURL string, maxBytes int64) (status int, header http.Header, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	return c.Fetch(req, maxBytes)
}

// Fetch executes a request and reads at most maxBytes. A 304 response has an
// empty body and is not an error.
func (c *Client) Fetch(req *http.Request, maxBytes int64) (status int, header http.Header, body []byte, err error) {
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	body, readErr := ReadAll(resp.Body, maxBytes)
	if readErr != nil {
		return resp.StatusCode, resp.Header, nil, readErr
	}
	return resp.StatusCode, resp.Header, body, nil
}

// ReadAll is io.ReadAll with a hard size limit.
func ReadAll(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("safehttp: size limit must be positive")
	}
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// ValidateURL checks scheme, credentials, port and literal-address policy.
// Hostnames are validated again after DNS resolution at dial time.
func ValidateURL(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid URL", ErrBlocked)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: only HTTP(S) is supported", ErrBlocked)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: URLs with credentials are not allowed", ErrBlocked)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: missing host", ErrBlocked)
	}
	if strings.Contains(host, "%") {
		return nil, fmt.Errorf("%w: zone identifiers are not allowed", ErrBlocked)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%w: invalid port", ErrBlocked)
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if !allowPrivate && !IsPublicIP(ip) {
			return nil, fmt.Errorf("%w: %s is not public", ErrBlocked, ip)
		}
		return u, nil
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if !allowPrivate && (lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".home.arpa")) {
		return nil, fmt.Errorf("%w: local hostnames are not allowed", ErrBlocked)
	}
	return u, nil
}

// dialIPAllowed decides whether one resolved address may be dialed. Fake-IP
// answers are accepted only for hostname requests on the two web ports, so a
// redirect or user-supplied literal address cannot use the allowance to probe
// the benchmark range.
func (c *Client) dialIPAllowed(host, port string, ip net.IP) bool {
	if c.opts.AllowPrivate {
		return true
	}
	if IsPublicIP(ip) {
		return true
	}
	if !c.opts.AllowFakeIP || net.ParseIP(host) != nil || (port != "80" && port != "443") {
		return false
	}
	return IsFakeIP(ip)
}

func (c *Client) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := c.opts.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("safehttp: resolve %s: %w", host, err)
	}
	if c.opts.AllowFakeIP {
		// In LazyCat's fake-IP network only IPv4 answers go through the
		// platform proxy; IPv6 answers are real addresses that the container
		// often cannot route. Trying IPv4 first avoids a long failed dial.
		sort.SliceStable(ips, func(i, j int) bool {
			return ips[i].IP.To4() != nil && ips[j].IP.To4() == nil
		})
	}
	var lastErr error
	tried := 0
	for _, item := range ips {
		if !c.dialIPAllowed(host, port, item.IP) {
			lastErr = fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, item.IP)
			continue
		}
		tried++
		conn, dialErr := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(
			ctx, network, net.JoinHostPort(item.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if tried == 0 && lastErr == nil {
		lastErr = fmt.Errorf("%w: %s has no addresses", ErrBlocked, host)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%w: %s has no usable address", ErrBlocked, host)
	}
	return nil, lastErr
}

// maxRedirects bounds redirect loops while leaving room for podcast analytics
// chains, which can pass through ten tracking hosts before the audio CDN.
// Every hop is still validated before it is followed.
const maxRedirects = 12

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("safehttp: too many redirects")
	}
	if _, err := ValidateURL(req.URL.String(), c.opts.AllowPrivate); err != nil {
		return err
	}
	return nil
}

var deniedV4 = mustCIDRs(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"255.255.255.255/32",
)

var deniedV6 = mustCIDRs(
	"::/128",
	"::1/128",
	"64:ff9b::/96",
	"100::/64",
	"2001:db8::/32",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

func mustCIDRs(items ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(items))
	for _, item := range items {
		_, network, err := net.ParseCIDR(item)
		if err != nil {
			panic("safehttp: bad CIDR " + item)
		}
		out = append(out, network)
	}
	return out
}

var fakeIPNet = mustCIDRs("198.18.0.0/15")[0]

// IsFakeIP reports whether ip is in the RFC 2544 benchmark range that LazyCat
// and similar fake-IP DNS resolvers assign to proxied public hostnames.
func IsFakeIP(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && fakeIPNet.Contains(v4)
}

// IsPublicIP reports whether ip is a routable public unicast address.
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		for _, network := range deniedV4 {
			if network.Contains(v4) {
				return false
			}
		}
		return true
	}
	for _, network := range deniedV6 {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}
