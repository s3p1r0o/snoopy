package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.215.14":    true,
		"2606:4700::1111":  true,
		"127.0.0.1":        false,
		"::1":              false,
		"10.1.2.3":         false,
		"172.16.0.1":       false,
		"192.168.1.1":      false,
		"169.254.169.254":  false, // cloud metadata
		"100.64.0.1":       false, // CGNAT
		"0.0.0.0":          false,
		"fd00::1":          false, // ULA
		"fe80::1":          false,
		"::ffff:127.0.0.1": false, // IPv4-mapped loopback
		"224.0.0.1":        false,
		// IPv6 forms that embed an IPv4 address are judged by that address.
		"64:ff9b::a00:1":     false, // NAT64 of 10.0.0.1
		"64:ff9b::7f00:1":    false, // NAT64 of 127.0.0.1
		"64:ff9b::5db8:d70e": true,  // NAT64 of 93.184.215.14
		"64:ff9b:1::1":       false, // local-use NAT64 prefix
		"2002:7f00:1::1":     false, // 6to4 of 127.0.0.1
		"2002:5db8:d70e::1":  true,  // 6to4 of 93.184.215.14
		"::7f00:1":           false, // IPv4-compatible 127.0.0.1
		// Reserved IPv4 and deprecated IPv6 ranges.
		"0.1.2.3":    false,
		"198.18.0.1": false,
		"240.0.0.1":  false,
		"fec0::1":    false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestFetchRefusesPrivateTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer srv.Close()
	f := newFetcher(false)
	// The loopback listener, by IP and through a hostname that resolves to it.
	for _, target := range []string{srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)} {
		if _, err := f.get(context.Background(), target); !errors.Is(err, errBlockedAddress) {
			t.Errorf("get(%s) error = %v, want errBlockedAddress", target, err)
		}
	}
}

func TestFetchRejectsNonHTTPURLs(t *testing.T) {
	f := newFetcher(true)
	for _, target := range []string{"file:///etc/passwd", "ftp://example.com/x", "/relative", "example.com"} {
		if _, err := f.get(context.Background(), target); err == nil || !strings.Contains(err.Error(), "only absolute http(s) URLs") {
			t.Errorf("get(%q) error = %v, want an invalid URL error", target, err)
		}
	}
}

func TestFetchCapsDecompressedBody(t *testing.T) {
	// A small gzip body that inflates past the cap must be cut after decompression.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(bytes.Repeat([]byte("a"), maxFetchBodyBytes+1000))
	zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gz.Bytes())
	}))
	defer srv.Close()
	p, err := newFetcher(true).get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Truncated || len(p.Body) != maxFetchBodyBytes {
		t.Fatalf("truncated=%v len=%d, want a cut at %d", p.Truncated, len(p.Body), maxFetchBodyBytes)
	}
	if p.ContentType != "text/plain" {
		t.Fatalf("content type = %q", p.ContentType)
	}
}

func TestFetchStopsRedirectLoops(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()
	if _, err := newFetcher(true).get(context.Background(), srv.URL+"/a"); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("error = %v, want a redirect limit error", err)
	}
}

func TestFetchPresentsAsChromeAndKeepsErrorStatuses(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		http.NotFound(w, r)
	}))
	defer srv.Close()
	p, err := newFetcher(true).get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != http.StatusNotFound {
		t.Fatalf("status = %d", p.Status)
	}
	if ua := got.Get("User-Agent"); !strings.Contains(ua, "Chrome/155.0.0.0") || !strings.HasPrefix(ua, "Mozilla/5.0 (Windows NT 10.0; Win64; x64)") {
		t.Errorf("User-Agent = %q", ua)
	}
	for name, want := range map[string]string{
		"Sec-Ch-Ua":          `"Google Chrome";v="155"`,
		"Sec-Ch-Ua-Platform": `"Windows"`,
		"Accept-Language":    "en-US",
	} {
		if !strings.Contains(got.Get(name), want) {
			t.Errorf("%s = %q, want it to contain %q", name, got.Get(name), want)
		}
	}
}
