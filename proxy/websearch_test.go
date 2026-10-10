package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "rts info" {
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
	e := &braveEngine{key: "secret", endpoint: srv.URL, client: http.DefaultClient}
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
