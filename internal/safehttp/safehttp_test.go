package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		allowPrivate bool
		wantErr      bool
	}{
		{name: "public https", raw: "https://example.com/feed.xml"},
		{name: "public ipv4", raw: "http://1.1.1.1/rss"},
		{name: "loopback blocked", raw: "http://127.0.0.1:8090/feed.xml", wantErr: true},
		{name: "ipv6 loopback blocked", raw: "http://[::1]/feed", wantErr: true},
		{name: "metadata blocked", raw: "http://169.254.169.254/latest/meta-data", wantErr: true},
		{name: "private blocked", raw: "http://192.168.1.10/feed", wantErr: true},
		{name: "localhost blocked", raw: "http://localhost:8080/feed", wantErr: true},
		{name: "dot local blocked", raw: "http://printer.local/feed", wantErr: true},
		{name: "wrong scheme", raw: "ftp://example.com/feed", wantErr: true},
		{name: "credentials blocked", raw: "https://user:secret@example.com/feed", wantErr: true},
		{name: "bad port", raw: "https://example.com:99999/feed", wantErr: true},
		{name: "zone id blocked", raw: "http://[fe80::1%25eth0]/feed", wantErr: true},
		{name: "private allowed in test mode", raw: "http://127.0.0.1:8090/feed.xml", allowPrivate: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateURL(test.raw, test.allowPrivate)
			if test.wantErr && !errors.Is(err, ErrBlocked) {
				t.Fatalf("want ErrBlocked, got %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"1.1.1.1", true},
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"172.20.0.1", false},
		{"192.168.0.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"224.0.0.1", false},
		{"0.0.0.0", false},
		{"::1", false},
		{"fc00::1", false},
		{"fe80::1", false},
		{"ff02::1", false},
	}
	for _, test := range tests {
		if got := IsPublicIP(net.ParseIP(test.ip)); got != test.want {
			t.Errorf("IsPublicIP(%s) = %v, want %v", test.ip, got, test.want)
		}
	}
	if IsPublicIP(nil) {
		t.Fatal("nil IP must not be public")
	}
}

func TestReadAllLimit(t *testing.T) {
	data, err := ReadAll(strings.NewReader("1234567890"), 5)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if data != nil {
		t.Fatalf("data must be discarded on overflow, got %q", data)
	}
	ok, err := ReadAll(strings.NewReader("12345"), 5)
	if err != nil || string(ok) != "12345" {
		t.Fatalf("ReadAll within limit = %q, %v", ok, err)
	}
}

type fakeResolver struct {
	ips []net.IPAddr
}

func (f fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return f.ips, nil
}

func TestClientBlocksPrivateDNSAnswer(t *testing.T) {
	client := New(Options{
		Resolver: fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
	})
	req, err := http.NewRequest(http.MethodGet, "http://public-looking.example/feed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked for private DNS answer, got %v", err)
	}
}

func TestClientAllowPrivateAndBoundedFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
			return
		}
		w.Header().Set("ETag", `"fixture"`)
		_, _ = w.Write([]byte("feed body"))
	}))
	defer server.Close()

	client := New(Options{
		AllowPrivate: true,
		Resolver:     fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
	})
	status, header, body, err := client.Get(context.Background(), server.URL+"/feed", 64)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || string(body) != "feed body" || header.Get("ETag") != `"fixture"` {
		t.Fatalf("unexpected fetch: %d %q %q", status, body, header.Get("ETag"))
	}
	if _, _, _, err := client.Get(context.Background(), server.URL+"/big", 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

type flakyRoundTripper struct {
	calls    int
	failures int
}

func (f *flakyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, io.ErrUnexpectedEOF
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    req,
	}, nil
}

func TestClientRetriesTransientTransportErrors(t *testing.T) {
	client := New(Options{})
	transport := &flakyRoundTripper{failures: 2}
	client.http.Transport = transport
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/feed.xml", nil)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("retried request = %v, %v", resp, err)
	}
	resp.Body.Close()
	if transport.calls != 3 {
		t.Fatalf("calls = %d, want 3", transport.calls)
	}

	client.http.Transport = &flakyRoundTripper{failures: 5}
	if _, err := client.Do(req); err == nil {
		t.Fatal("persistent transport failure should surface")
	}
	if transport := client.http.Transport.(*flakyRoundTripper); transport.calls != 3 {
		t.Fatalf("calls = %d, want 3 attempts", transport.calls)
	}
	if retryableTransportError(fmt.Errorf("wrapped: %w", ErrBlocked)) {
		t.Fatal("policy errors must not be retried")
	}
}

func TestFakeIPDialPolicy(t *testing.T) {
	strict := New(Options{})
	lazycat := New(Options{AllowFakeIP: true})
	tests := []struct {
		name string
		c    *Client
		host string
		port string
		ip   string
		want bool
	}{
		{name: "public", c: strict, host: "example.com", port: "443", ip: "1.1.1.1", want: true},
		{name: "private blocked", c: strict, host: "example.com", port: "443", ip: "10.0.0.1"},
		{name: "fake blocked by default", c: strict, host: "cdn.example", port: "443", ip: "198.19.4.10"},
		{name: "fake allowed on 443", c: lazycat, host: "cdn.example", port: "443", ip: "198.19.4.10", want: true},
		{name: "fake allowed on 80", c: lazycat, host: "cdn.example", port: "80", ip: "198.18.0.2", want: true},
		{name: "fake blocked on other port", c: lazycat, host: "cdn.example", port: "8080", ip: "198.19.4.10"},
		{name: "fake blocked for literal host", c: lazycat, host: "198.19.4.10", port: "443", ip: "198.19.4.10"},
		{name: "private still blocked", c: lazycat, host: "cdn.example", port: "443", ip: "192.168.1.1"},
		{name: "metadata still blocked", c: lazycat, host: "cdn.example", port: "80", ip: "169.254.169.254"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ip := net.ParseIP(test.ip)
			if got := test.c.dialIPAllowed(test.host, test.port, ip); got != test.want {
				t.Fatalf("dialIPAllowed(%q, %q, %s) = %v, want %v", test.host, test.port, test.ip, got, test.want)
			}
		})
	}
}

func TestClientFollowsLongRedirectChains(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		remaining, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		if remaining > 0 {
			http.Redirect(w, r, "/hop/"+strconv.Itoa(remaining-1), http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("audio"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := New(Options{
		AllowPrivate: true,
		Resolver:     fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}},
	})
	req, err := http.NewRequest(http.MethodGet, server.URL+"/hop/10", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("ten redirects should be followed: %v", err)
	}
	resp.Body.Close()

	req, err = http.NewRequest(http.MethodGet, server.URL+"/hop/12", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("unbounded redirect chain should be rejected")
	} else if !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("err = %v, want redirect limit", err)
	}
}
