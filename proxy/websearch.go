package main

// websearch.go backs the web_search tool with a search engine the operator
// configures: a SearXNG instance (self-hosted, its JSON API enabled) or the
// Brave Search API. Engine endpoints are operator-chosen, so they are called
// with a plain client (a local SearXNG is the usual setup), not the fetcher.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSearchResults = 8
	maxSearchResults     = 10
	searchTimeout        = 15 * time.Second
	braveEndpoint        = "https://api.search.brave.com/res/v1/web/search"
)

type searchResult struct {
	Title, URL, Snippet, Published string
}

// searchEngine runs one web search.
type searchEngine interface {
	name() string
	search(ctx context.Context, query string, limit int) ([]searchResult, error)
}

// newSearchEngine picks the engine: kind "searxng" or "brave", or "" to use
// whichever is configured (SearXNG first). It returns nil, nil when none is.
func newSearchEngine(kind, searxngURL, braveKey string) (searchEngine, error) {
	client := &http.Client{Timeout: searchTimeout}
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
	}
	return nil, fmt.Errorf("unknown search engine %q (want searxng or brave)", kind)
}

type searxngEngine struct {
	base   *url.URL
	client *http.Client
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
	if err := getJSON(ctx, e.client, u.String(), nil, &body); err != nil {
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
	client        *http.Client
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

// getJSON GETs rawURL and decodes its JSON body into v.
func getJSON(ctx context.Context, client *http.Client, rawURL string, header http.Header, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
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
