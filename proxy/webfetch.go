package main

// webfetch.go is the outbound HTTP of the proxy's server-side web tools
// (web_fetch, web_crawl). Every request goes through one fetcher, which:
//   - only fetches absolute http(s) URLs;
//   - refuses non-public addresses at connect time, on the address actually
//     dialed, so a hostname that resolves (or re-resolves) to a private,
//     loopback or link-local address cannot reach the proxy's own network;
//   - ignores HTTP(S)_PROXY, which would hide the real target from that check;
//   - follows at most maxRedirects redirects, each one re-checked on connect;
//   - caps the decoded body at maxFetchBodyBytes and flags the cut.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	maxFetchBodyBytes = 5 << 20
	fetchTimeout      = 20 * time.Second
	maxRedirects      = 5
)

// errBlockedAddress marks a connection refused by the public-address check.
var errBlockedAddress = errors.New("refusing to connect to a non-public address")

// nonPublic are ranges netip.Addr.IsGlobalUnicast and IsPrivate let through:
// CGNAT (RFC 6598), "this network", benchmarking, reserved IPv4, deprecated
// IPv6 site-local, and the local-use NAT64 prefix (RFC 8215), which only
// translates to internal addresses.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96") // RFC 6052 well-known prefix
	sixToFour = netip.MustParsePrefix("2002::/16")    // RFC 3056
	v4Compat  = netip.MustParsePrefix("::/96")        // deprecated IPv4-compatible
)

// publicAddr reports whether a connect target is a public unicast address:
// not loopback, link-local, multicast, unspecified, private (RFC 1918, ULA),
// nor in nonPublic. IPv6 forms that embed an IPv4 address (IPv4-mapped,
// NAT64, 6to4, IPv4-compatible) are judged by that IPv4 address, so a NAT64
// network cannot be used to reach private IPv4 services.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.Is6() {
		b := a.As16()
		switch {
		case nat64.Contains(a), v4Compat.Contains(a):
			return publicAddr(netip.AddrFrom4([4]byte(b[12:16])))
		case sixToFour.Contains(a):
			return publicAddr(netip.AddrFrom4([4]byte(b[2:6])))
		}
	}
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// fetcher performs the web tools' page requests.
type fetcher struct {
	client    *http.Client
	userAgent string
}

// newFetcher builds a fetcher; allowPrivate disables the public-address check
// and exists only for tests, whose pages are served from 127.0.0.1.
func newFetcher(userAgent string, allowPrivate bool) *fetcher {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !publicAddr(ap.Addr()) {
				return fmt.Errorf("%w (%s)", errBlockedAddress, address)
			}
			return nil
		},
	}
	return &fetcher{
		userAgent: userAgent,
		client: &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				Proxy:               nil,
				DialContext:         dialer.DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
				MaxIdleConns:        10,
				IdleConnTimeout:     30 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
				}
				return nil
			},
		},
	}
}

// page is one fetched response.
type page struct {
	URL         *url.URL // final URL, after redirects
	Status      int
	ContentType string // media type, lower-case, without parameters
	rawType     string // the Content-Type header, kept for charset decoding
	Body        []byte // at most maxFetchBodyBytes, decompressed
	Truncated   bool
}

// get fetches rawURL. Non-2xx statuses are returned as pages, not errors.
func (f *fetcher) get(ctx context.Context, rawURL string) (*page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid URL %q: only absolute http(s) URLs can be fetched", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.1")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes+1))
	if err != nil {
		return nil, err
	}
	p := &page{URL: resp.Request.URL, Status: resp.StatusCode, rawType: resp.Header.Get("Content-Type")}
	if len(body) > maxFetchBodyBytes {
		body, p.Truncated = body[:maxFetchBodyBytes], true
	}
	p.Body = body
	if mt, _, err := mime.ParseMediaType(p.rawType); err == nil {
		p.ContentType = strings.ToLower(mt)
	}
	return p, nil
}
