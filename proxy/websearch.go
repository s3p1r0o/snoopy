package main

// websearch.go backs the web_search tool with a search engine the operator
// configures: a SearXNG instance (self-hosted, its JSON API enabled), the
// Brave Search API, or DuckDuckGo's keyless HTML page (only when asked for).
// Engine endpoints are operator-chosen, so they are called with their own
// client (a local SearXNG is the usual setup), not the fetcher; like the
// fetcher, that client presents Chrome's TLS fingerprint, which DuckDuckGo's
// bot defences inspect.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/imroc/req/v3"
)

const (
	defaultSearchResults = 8
	maxSearchResults     = 10
	searchTimeout        = 15 * time.Second
	braveEndpoint        = "https://api.search.brave.com/res/v1/web/search"
	ddgEndpoint          = "https://html.duckduckgo.com/html/"
	// ddgMinInterval spaces DuckDuckGo searches: it publishes no API for web
	// results and answers bursts with a challenge page.
	ddgMinInterval = 2 * time.Second
)

type searchResult struct {
	Title, URL, Snippet, Published string
}

// newSearchClient builds the client all search engines use: like the fetcher,
// it speaks Chrome's TLS handshake, since bot walls also fingerprint requests
// to search engines (DuckDuckGo's, at least, do). Auto-reading the body is off
// so callers keep their size caps.
func newSearchClient() *req.Client {
	return req.C().
		SetTimeout(searchTimeout).
		SetTLSFingerprintChrome().
		DisableAutoReadResponse()
}

// searchEngine runs one web search.
type searchEngine interface {
	name() string
	search(ctx context.Context, query string, limit int) ([]searchResult, error)
}

// newSearchEngine picks the engine: kind "searxng", "brave" or "duckduckgo",
// or "" to use whichever is configured (SearXNG first, then Brave). It returns
// nil, nil when none is: DuckDuckGo, which scrapes a page, is never implied.
func newSearchEngine(kind, searxngURL, braveKey string) (searchEngine, error) {
	client := newSearchClient()
	if kind == "" {
		switch {
		case searxngURL != "":
			kind = "searxng"
		case braveKey != "":
			kind = "brave"
		default:
			return nil, nil
		}
	}
	switch kind {
	case "searxng":
		base, err := url.Parse(searxngURL)
		if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
			return nil, fmt.Errorf("search engine searxng needs an http(s) base URL (--searxng-url / RILL_SEARXNG_URL), got %q", searxngURL)
		}
		return &searxngEngine{base: base, client: client}, nil
	case "brave":
		if braveKey == "" {
			return nil, fmt.Errorf("search engine brave needs BRAVE_SEARCH_API_KEY")
		}
		return &braveEngine{key: braveKey, endpoint: braveEndpoint, client: client}, nil
	case "duckduckgo":
		return &duckduckgoEngine{endpoint: ddgEndpoint, client: client, minInterval: ddgMinInterval}, nil
	}
	return nil, fmt.Errorf("unknown search engine %q (want searxng, brave or duckduckgo)", kind)
}

type searxngEngine struct {
	base   *url.URL
	client *req.Client
}

func (e *searxngEngine) name() string { return "searxng" }

func (e *searxngEngine) search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	u := e.base.JoinPath("search")
	u.RawQuery = url.Values{"q": {query}, "format": {"json"}}.Encode()
	var body struct {
		Results []struct {
			URL, Title, Content string
			PublishedDate       string `json:"publishedDate"`
		} `json:"results"`
	}
	// The proxy is the instance's only client: saying so keeps SearXNG's bot
	// detection from logging a missing-client-address error on every query.
	header := http.Header{"X-Real-Ip": {"127.0.0.1"}}
	if err := getJSON(ctx, e.client, u.String(), header, &body); err != nil {
		if strings.Contains(err.Error(), "status 403") {
			err = fmt.Errorf("%w (enable the JSON format: `search: formats: [html, json]` in SearXNG's settings.yml)", err)
		}
		return nil, err
	}
	out := make([]searchResult, 0, limit)
	for _, r := range body.Results {
		if len(out) == limit {
			break
		}
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Published: r.PublishedDate})
	}
	return out, nil
}

type braveEngine struct {
	key, endpoint string
	client        *req.Client
}

func (e *braveEngine) name() string { return "brave" }

func (e *braveEngine) search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	u, _ := url.Parse(e.endpoint)
	u.RawQuery = url.Values{"q": {query}, "count": {strconv.Itoa(limit)}}.Encode()
	var body struct {
		Web struct {
			Results []struct {
				Title, URL, Description string
				Age                     string `json:"age"`
			} `json:"results"`
		} `json:"web"`
	}
	header := http.Header{"X-Subscription-Token": {e.key}}
	if err := getJSON(ctx, e.client, u.String(), header, &body); err != nil {
		return nil, err
	}
	out := make([]searchResult, 0, limit)
	for _, r := range body.Web.Results {
		if len(out) == limit {
			break
		}
		out = append(out, searchResult{Title: r.Title, URL: r.URL, Snippet: r.Description, Published: r.Age})
	}
	return out, nil
}

// errDDGRateLimited reports DuckDuckGo's bot defences (HTTP 202 or its challenge page).
var errDDGRateLimited = errors.New("DuckDuckGo is rate-limiting this machine (HTTP 202 or challenge page): wait a minute, or use web_fetch on a known URL")

// duckduckgoEngine reads DuckDuckGo's keyless HTML results page
// (html.duckduckgo.com), first page only. DuckDuckGo publishes no API for web
// results and meets bursts with HTTP 202 or a challenge page, so searches are
// spaced by minInterval, even across concurrent requests, and those answers
// become errDDGRateLimited. Ads are skipped.
type duckduckgoEngine struct {
	endpoint    string
	client      *req.Client
	minInterval time.Duration

	mu   sync.Mutex
	next time.Time // earliest start of the next search
}

func (e *duckduckgoEngine) name() string { return "duckduckgo" }

// wait reserves the next search slot and sleeps until it, or until ctx ends.
func (e *duckduckgoEngine) wait(ctx context.Context) error {
	e.mu.Lock()
	at := time.Now()
	if e.next.After(at) {
		at = e.next
	}
	e.next = at.Add(e.minInterval)
	e.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(at)):
		return nil
	}
}

func (e *duckduckgoEngine) search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	if err := e.wait(ctx); err != nil {
		return nil, err
	}
	form := url.Values{"q": {query}, "b": {""}, "kl": {"wt-wt"}}
	resp, err := e.client.R().
		SetContext(ctx).
		SetHeaders(map[string]string{
			"Content-Type":    "application/x-www-form-urlencoded",
			"Referer":         "https://html.duckduckgo.com/",
			"User-Agent":      chromeUserAgent,
			"Accept-Language": browserHeaders["Accept-Language"],
		}).
		SetBodyString(form.Encode()).
		Post(e.endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusAccepted:
		return nil, errDDGRateLimited
	default:
		return nil, fmt.Errorf("search engine answered status %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if doc.Find("form#challenge-form").Length() > 0 {
		return nil, errDDGRateLimited
	}
	out := make([]searchResult, 0, limit)
	doc.Find("#links > div.web-result").Each(func(_ int, s *goquery.Selection) {
		if len(out) == limit || s.HasClass("result--ad") {
			return
		}
		a := s.Find("h2 a").First()
		href, _ := a.Attr("href")
		href, title := resultURL(href), strings.TrimSpace(a.Text())
		if href == "" || title == "" {
			return
		}
		out = append(out, searchResult{Title: title, URL: href, Snippet: strings.TrimSpace(s.Find(".result__snippet").First().Text())})
	})
	return out, nil
}

// resultURL returns where a result link points. DuckDuckGo writes most links
// directly, and some as a protocol-relative hop through its own site,
// //duckduckgo.com/l/?uddg=<destination>, whose destination is returned.
func resultURL(href string) string {
	link, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	if dest := link.Query().Get("uddg"); dest != "" && link.Path == "/l/" && strings.HasSuffix(link.Hostname(), "duckduckgo.com") {
		return dest
	}
	if link.Scheme == "" && link.Host != "" {
		link.Scheme = "https"
	}
	return link.String()
}

// getJSON GETs rawURL and decodes its JSON body into v.
func getJSON(ctx context.Context, client *req.Client, rawURL string, header http.Header, v any) error {
	r := client.R().SetContext(ctx).SetHeader("Accept", "application/json")
	for k, vs := range header {
		r.Headers[http.CanonicalHeaderKey(k)] = vs
	}
	resp, err := r.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("search engine answered status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// formatResults renders results as the Markdown list web_search returns.
func formatResults(query string, results []searchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results for %q.", query)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Results for %q:\n", query)
	for i, r := range results {
		fmt.Fprintf(&sb, "\n%d. [%s](%s)", i+1, strings.TrimSpace(r.Title), r.URL)
		if r.Published != "" {
			fmt.Fprintf(&sb, " (%s)", r.Published)
		}
		if s := strings.TrimSpace(r.Snippet); s != "" {
			fmt.Fprintf(&sb, "\n   %s", s)
		}
	}
	return sb.String()
}
