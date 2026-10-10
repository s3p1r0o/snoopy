package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// crawlSite serves a small site: / links to /a, /b, /private/x and an external
// page; /a links deeper to /a/deep; robots.txt disallows /private.
func crawlSite(t *testing.T, robots string) *httptest.Server {
	t.Helper()
	pages := map[string]string{
		"/":          `<a href="/a">A</a> <a href="b#top">B</a> <a href="/private/x">P</a> <a href="https://elsewhere.example/">E</a>`,
		"/a":         `<p>page a</p><a href="/a/deep">deeper</a>`,
		"/a/deep":    `<p>page deep</p>`,
		"/b":         `<p>page b</p>`,
		"/private/x": `<p>secret</p>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			if robots == "" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(robots))
			return
		}
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body>" + body + "</body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func crawlTest(t *testing.T, srv *httptest.Server, start string, opt crawlOptions) string {
	t.Helper()
	old := crawlDelay
	crawlDelay = 0
	t.Cleanup(func() { crawlDelay = old })
	out, err := newFetcher("test", true).crawl(context.Background(), srv.URL+start, opt)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCrawlHonoursRobotsAndHost(t *testing.T) {
	srv := crawlSite(t, "User-agent: *\nDisallow: /private\n")
	out := crawlTest(t, srv, "/", crawlOptions{maxPages: 10, maxDepth: 2})
	for _, want := range []string{"page a", "page b", "page deep", "1 skipped by robots.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"secret", "elsewhere.example/\n"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, out)
		}
	}
}

func TestCrawlLimitsDepthPagesAndPrefix(t *testing.T) {
	srv := crawlSite(t, "")
	if out := crawlTest(t, srv, "/", crawlOptions{maxPages: 10, maxDepth: 1}); strings.Contains(out, "page deep") {
		t.Errorf("depth 1 reached /a/deep:\n%s", out)
	}
	if out := crawlTest(t, srv, "/", crawlOptions{maxPages: 2, maxDepth: 2}); !strings.Contains(out, "(2 pages fetched") {
		t.Errorf("page limit not applied:\n%s", out)
	}
	out := crawlTest(t, srv, "/a", crawlOptions{maxPages: 10, maxDepth: 2, pathPrefix: "/a"})
	if !strings.Contains(out, "page deep") || strings.Contains(out, "page b") {
		t.Errorf("path prefix not applied:\n%s", out)
	}
}

func TestCrawlUnreachableRobotsDisallowsAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("<p>content</p>"))
	}))
	defer srv.Close()
	out := crawlTest(t, srv, "/", crawlOptions{maxPages: 5, maxDepth: 1})
	if !strings.Contains(out, "(0 pages fetched, 1 skipped by robots.txt)") {
		t.Fatalf("out:\n%s", out)
	}
}

func TestCrawlFollowsAStartRedirectToAnotherHost(t *testing.T) {
	// apex -> www style: the start URL redirects to another host, whose pages
	// and robots.txt the crawl must then use.
	target := crawlSite(t, "User-agent: *\nDisallow: /private\n")
	apex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(apex.Close)
	out := crawlTest(t, apex, "/", crawlOptions{maxPages: 10, maxDepth: 2})
	for _, want := range []string{"page a", "page b", "1 skipped by robots.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
