package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewSearchEngineSelection(t *testing.T) {
	for _, tc := range []struct {
		kind, searx, brave, want string
		wantErr                  bool
	}{
		{"", "", "", "", false}, // none configured: web_search unavailable
		{"", "http://localhost:8888", "k", "searxng", false},
		{"", "", "k", "brave", false},
		{"brave", "http://localhost:8888", "k", "brave", false},
		{"searxng", "", "", "", true},
		{"searxng", "localhost:8888", "", "", true},
		{"brave", "", "", "", true},
		{"duckduckgo", "", "", "duckduckgo", false}, // keyless, only when asked
		{"bing", "", "", "", true},
	} {
		e, err := newSearchEngine(tc.kind, tc.searx, tc.brave)
		if (err != nil) != tc.wantErr {
			t.Errorf("%+v: err = %v", tc, err)
			continue
		}
		got := ""
		if e != nil {
			got = e.name()
		}
		if got != tc.want {
			t.Errorf("%+v: engine = %q, want %q", tc, got, tc.want)
		}
	}
}

func TestSearxngSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// X-Real-IP keeps SearXNG's bot detection from logging an error per query.
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "rts info" || r.Header.Get("X-Real-IP") != "127.0.0.1" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(`{"results":[
			{"url":"https://www.rts.ch/info/1","title":"One","content":"first","publishedDate":"2026-10-10"},
			{"url":"https://www.rts.ch/info/2","title":"Two","content":"second"},
			{"url":"https://www.rts.ch/info/3","title":"Three","content":"third"}]}`))
	}))
	defer srv.Close()
	e, _ := newSearchEngine("searxng", srv.URL, "")
	rs, err := e.search(context.Background(), "rts info", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].URL != "https://www.rts.ch/info/1" || rs[0].Published != "2026-10-10" {
		t.Fatalf("results = %+v", rs)
	}
}

func TestSearxngJSONDisabledHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	e, _ := newSearchEngine("searxng", srv.URL, "")
	if _, err := e.search(context.Background(), "x", 3); err == nil || !strings.Contains(err.Error(), "formats: [html, json]") {
		t.Fatalf("err = %v", err)
	}
}

func TestBraveSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "secret" || r.URL.Query().Get("count") != "3" {
			t.Errorf("unexpected request %s %v", r.URL, r.Header)
		}
		w.Write([]byte(`{"web":{"results":[{"title":"A","url":"https://a.example","description":"aa","age":"2 hours ago"}]}}`))
	}))
	defer srv.Close()
	e := &braveEngine{key: "secret", endpoint: srv.URL, client: newSearchClient()}
	rs, err := e.search(context.Background(), "q", 3)
	if err != nil || len(rs) != 1 || rs[0].Snippet != "aa" || rs[0].Published != "2 hours ago" {
		t.Fatalf("rs=%+v err=%v", rs, err)
	}
}

func TestFormatResults(t *testing.T) {
	got := formatResults("q", []searchResult{{Title: "A", URL: "https://a.example", Snippet: "aa", Published: "today"}})
	want := "Results for \"q\":\n\n1. [A](https://a.example) (today)\n   aa"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if formatResults("q", nil) != `No results for "q".` {
		t.Fatal("empty results")
	}
}

// ddgPage mimics html.duckduckgo.com's results page: a direct link, a
// "/l/?uddg=" redirect link, and an ad that must be skipped.
const ddgPage = `<html><body><div id="links" class="results">
<div class="result results_links results_links_deep web-result "><div class="links_main links_deep result__body">
 <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://www.rts.ch/info/">RTS Info</a></h2>
 <a class="result__snippet" href="https://www.rts.ch/info/">Suivez l&#x27;actualité en direct</a></div></div>
<div class="result results_links results_links_deep result--ad "><div class="links_main links_deep result__body">
 <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://ads.example/">Buy now</a></h2>
 <a class="result__snippet" href="https://ads.example/">An ad</a></div></div>
<div class="result results_links results_links_deep web-result "><div class="links_main links_deep result__body">
 <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Ffr.wikipedia.org%2Fwiki%2FRTS_Info&amp;rut=abc">RTS Info - Wikipédia</a></h2>
 <a class="result__snippet">Chaîne d&#x27;information</a></div></div>
</div></body></html>`

func ddgServer(t *testing.T, handler http.HandlerFunc) *duckduckgoEngine {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &duckduckgoEngine{endpoint: srv.URL, client: newSearchClient()}
}

func TestDuckDuckGoSearch(t *testing.T) {
	e := ddgServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("q") != "rts info" || !strings.Contains(r.UserAgent(), "Chrome/155.0.0.0") {
			t.Errorf("unexpected request %s q=%q ua=%q", r.Method, r.FormValue("q"), r.UserAgent())
		}
		w.Write([]byte(ddgPage))
	})
	rs, err := e.search(context.Background(), "rts info", 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []searchResult{
		{Title: "RTS Info", URL: "https://www.rts.ch/info/", Snippet: "Suivez l'actualité en direct"},
		{Title: "RTS Info - Wikipédia", URL: "https://fr.wikipedia.org/wiki/RTS_Info", Snippet: "Chaîne d'information"},
	}
	if len(rs) != len(want) || rs[0] != want[0] || rs[1] != want[1] {
		t.Fatalf("results = %+v", rs)
	}
	if rs, _ := e.search(context.Background(), "rts info", 1); len(rs) != 1 {
		t.Fatalf("limit not applied: %d results", len(rs))
	}
}

func TestDuckDuckGoReportsRateLimits(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"202": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) },
		"challenge": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`<html><body><form id="challenge-form" action="/anomaly"></form></body></html>`))
		},
	} {
		if _, err := ddgServer(t, handler).search(context.Background(), "q", 3); !errors.Is(err, errDDGRateLimited) {
			t.Errorf("%s: err = %v, want errDDGRateLimited", name, err)
		}
	}
}

func TestDuckDuckGoSpacesSearches(t *testing.T) {
	var starts []time.Time
	e := ddgServer(t, func(w http.ResponseWriter, r *http.Request) {
		starts = append(starts, time.Now())
		w.Write([]byte(ddgPage))
	})
	e.minInterval = 100 * time.Millisecond
	for range 2 {
		if _, err := e.search(context.Background(), "q", 3); err != nil {
			t.Fatal(err)
		}
	}
	// Slots are exactly minInterval apart; arrival times at the server jitter
	// by a fraction of a millisecond (unthrottled, they would be ~1 ms apart).
	if gap := starts[1].Sub(starts[0]); gap < 90*time.Millisecond {
		t.Fatalf("searches only %v apart", gap)
	}
	// A wait that would outlast the caller's context gives up at once.
	e.minInterval = time.Hour
	e.search(context.Background(), "q", 3)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := e.search(ctx, "q", 3); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context deadline", err)
	}
}
