package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseWebTools(t *testing.T) {
	for spec, want := range map[string]string{
		"":             "",
		"none":         "",
		"all":          "web_crawl,web_fetch,web_search",
		"fetch":        "web_fetch",
		" Fetch,crawl": "web_crawl,web_fetch",
	} {
		owned, err := parseWebTools(spec)
		if err != nil {
			t.Fatalf("%q: %v", spec, err)
		}
		var got []string
		for _, name := range []string{toolWebCrawl, toolWebFetch, toolWebSearch} {
			if owned[name] {
				got = append(got, name)
			}
		}
		if strings.Join(got, ",") != want {
			t.Errorf("parseWebTools(%q) = %v, want %s", spec, got, want)
		}
	}
	if _, err := parseWebTools("fetch,browse"); err == nil {
		t.Error("an unknown tool must be rejected")
	}
}

func TestNewWebTools(t *testing.T) {
	if wt, err := newWebTools("none", "", "", "", nil); wt != nil || err != nil {
		t.Fatalf("none: %v %v", wt, err)
	}
	wt, err := newWebTools("all", "", "", "", nil)
	if err != nil || strings.Join(toolNames(wt.offered()), ",") != "web_fetch,web_crawl" {
		t.Fatalf("all without engine: %v %v", wt, err)
	}
	if !wt.owned[toolWebSearch] {
		t.Fatal("all must own web_search even without an engine, to hide the client's")
	}
	if _, err := newWebTools("search,fetch", "", "", "", nil); err == nil || !strings.Contains(err.Error(), "needs a search engine") {
		t.Fatalf("explicit search without engine: err = %v", err)
	}
	wt, err = newWebTools("search", "", "http://127.0.0.1:8888", "", nil)
	if err != nil || strings.Join(toolNames(wt.offered()), ",") != "web_search" {
		t.Fatalf("search with searxng: %v %v", wt, err)
	}
}

func TestChatRunsWebToolsServerSide(t *testing.T) {
	wt, srv := testWebTools(t, toolWebFetch)
	comp := &scriptedCompleter{resps: []*CompleteResponse{
		callTurn(&ToolCall{ID: "c1", Name: toolWebFetch, Input: `{"url":"` + srv.URL + `"}`}),
		textTurn("Summary of the page."),
	}}
	h := newServeHandler(serveConfig{web: wt}, comp)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"read it"}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.Bytes())
	}
	var got completionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if c := got.Choices[0]; c.Message.Content != "Summary of the page." || c.FinishReason != "stop" || len(c.Message.ToolCalls) != 0 {
		t.Fatalf("choice = %+v", c)
	}
}

// slowCompleter answers after delay, with resp or err.
type slowCompleter struct {
	delay time.Duration
	resp  *CompleteResponse
	err   error
}

func (s slowCompleter) complete(ctx context.Context, _ *CompleteRequest) (*CompleteResponse, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.resp, s.err
}

func streamBody(t *testing.T, comp completer, cfg serveConfig, content string) *httptest.ResponseRecorder {
	t.Helper()
	old := sseKeepAlive
	sseKeepAlive = 10 * time.Millisecond
	t.Cleanup(func() { sseKeepAlive = old })
	rec := httptest.NewRecorder()
	newServeHandler(cfg, comp).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"`+content+`"}]}`)))
	return rec
}

func TestStreamKeepsLongCompletionsAlive(t *testing.T) {
	rec := streamBody(t, slowCompleter{delay: 80 * time.Millisecond, resp: textTurn("done")}, serveConfig{web: &webTools{}}, "hi")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, ": keep-alive\n\n") ||
		!strings.Contains(body, `"content":"done"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("status %d body:\n%s", rec.Code, body)
	}
	if strings.Index(body, `"role":"assistant"`) > strings.Index(body, ": keep-alive") {
		t.Fatalf("the role chunk must open the stream before keep-alives:\n%s", body)
	}
}

func TestStreamReportsLateFailuresInBand(t *testing.T) {
	rec := streamBody(t, slowCompleter{delay: 50 * time.Millisecond, err: errors.New("boom")}, serveConfig{web: &webTools{}}, "hi")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"error":{"message":"upstream completion failed: boom","type":"api_error"}`) ||
		!strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("status %d body:\n%s", rec.Code, body)
	}
}

func TestStreamFastFailuresStayHTTPErrors(t *testing.T) {
	rec := streamBody(t, &fakeCompleter{}, serveConfig{maxInputTokens: 10}, strings.Repeat("a", 400))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "context_length_exceeded") {
		t.Fatalf("status %d body: %s", rec.Code, rec.Body.String())
	}
}

func TestStreamStopsWhenTheClientLeaves(t *testing.T) {
	old := sseKeepAlive
	sseKeepAlive = 10 * time.Millisecond
	t.Cleanup(func() { sseKeepAlive = old })
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	rec := httptest.NewRecorder()
	newServeHandler(serveConfig{web: &webTools{}}, slowCompleter{delay: 5 * time.Second, resp: textTurn("late")}).ServeHTTP(rec, req)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the handler kept running %v after the client left", took)
	}
	if strings.Contains(rec.Body.String(), "late") {
		t.Fatalf("a completion was written after the client left:\n%s", rec.Body.String())
	}
}

func TestStreamWithoutWebToolsKeepsTheOriginalBehaviour(t *testing.T) {
	// The bare proxy streams nothing until the completion is known, and a
	// late upstream failure is still an HTTP 502, exactly as before --web-tools.
	rec := streamBody(t, slowCompleter{delay: 50 * time.Millisecond, resp: textTurn("done")}, serveConfig{}, "hi")
	if body := rec.Body.String(); strings.Contains(body, "keep-alive") || !strings.Contains(body, `"content":"done"`) {
		t.Fatalf("bare stream changed:\n%s", body)
	}
	rec = streamBody(t, slowCompleter{delay: 50 * time.Millisecond, err: errors.New("boom")}, serveConfig{}, "hi")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("late failure: status %d body %s", rec.Code, rec.Body.String())
	}
}
