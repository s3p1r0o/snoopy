package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scriptedCompleter answers with its responses in order and records every request.
type scriptedCompleter struct {
	reqs  []*CompleteRequest
	resps []*CompleteResponse
}

func (s *scriptedCompleter) complete(_ context.Context, req *CompleteRequest) (*CompleteResponse, error) {
	s.reqs = append(s.reqs, req)
	if len(s.resps) == 0 {
		return nil, errors.New("no scripted response left")
	}
	resp := s.resps[0]
	s.resps = s.resps[1:]
	return resp, nil
}

func callTurn(calls ...*ToolCall) *CompleteResponse {
	msg := &Message{Role: "assistant"}
	for _, c := range calls {
		msg.Blocks = append(msg.Blocks, Block{ToolCall: c})
	}
	return &CompleteResponse{Message: msg, InputTokens: 10, OutputTokens: 1}
}

func textTurn(text string) *CompleteResponse {
	return &CompleteResponse{Message: &Message{Role: "assistant", Blocks: []Block{{Text: text}}}, InputTokens: 20, OutputTokens: 5}
}

func noBudget([]Message, []Tool) error { return nil }

func testWebTools(t *testing.T, names ...string) (*webTools, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><head><title>Info</title></head><body><p>Breaking news from the page.</p></body></html>"))
	}))
	t.Cleanup(srv.Close)
	owned := map[string]bool{}
	for _, n := range names {
		owned[n] = true
	}
	return &webTools{owned: owned, fetcher: newFetcher(true)}, srv
}

func TestWebToolsRunServerCallsThenAnswer(t *testing.T) {
	wt, srv := testWebTools(t, toolWebFetch, toolWebCrawl, toolWebSearch)
	comp := &scriptedCompleter{resps: []*CompleteResponse{
		callTurn(&ToolCall{ID: "c1", Name: toolWebFetch, Input: `{"url":"` + srv.URL + `"}`}),
		textTurn("Here is the news."),
	}}
	client := []Tool{{Name: "bash"}, {Name: toolWebFetch, Description: "client's own fetch"}, {Name: toolWebSearch}}
	resp, err := wt.complete(context.Background(), comp, &CompleteRequest{
		Messages: []Message{{Role: "user", Blocks: []Block{{Text: "news?"}}}},
		Tools:    client,
	}, noBudget)
	if err != nil {
		t.Fatal(err)
	}
	// The client's web_fetch and web_search are replaced; no engine, so no web_search is offered.
	if got := strings.Join(toolNames(comp.reqs[0].Tools), ","); got != "bash,web_fetch,web_crawl" {
		t.Fatalf("offered tools = %s", got)
	}
	if comp.reqs[0].Tools[1].Description == "client's own fetch" {
		t.Fatal("the client's web_fetch definition reached the model")
	}
	second := comp.reqs[1].Messages
	result := second[len(second)-1].Blocks[0].ToolResult
	if result == nil || result.ID != "c1" || !strings.Contains(result.Content, "Breaking news from the page.") || !strings.HasPrefix(result.Content, untrustedNote) {
		t.Fatalf("tool result = %+v", result)
	}
	// Usage reflects the client's own context (first round's input) and the
	// visible answer (last round's output), not the server-side rounds.
	if resp.Message.Blocks[0].Text != "Here is the news." || resp.InputTokens != 10 || resp.OutputTokens != 5 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestWebToolsReturnMixedTurnsToClient(t *testing.T) {
	wt, _ := testWebTools(t, toolWebFetch)
	comp := &scriptedCompleter{resps: []*CompleteResponse{
		callTurn(&ToolCall{ID: "s", Name: toolWebFetch, Input: `{"url":"https://example.com"}`}, &ToolCall{ID: "b", Name: "bash", Input: `{}`}),
	}}
	resp, err := wt.complete(context.Background(), comp, &CompleteRequest{Tools: []Tool{{Name: "bash"}}}, noBudget)
	if err != nil {
		t.Fatal(err)
	}
	if len(comp.reqs) != 1 || len(resp.Message.Blocks) != 1 || resp.Message.Blocks[0].ToolCall.Name != "bash" {
		t.Fatalf("want only the client call back after one round: reqs=%d blocks=%+v", len(comp.reqs), resp.Message.Blocks)
	}
}

func TestWebToolsReportUnavailableAndBadArguments(t *testing.T) {
	wt, _ := testWebTools(t, toolWebSearch, toolWebFetch)
	comp := &scriptedCompleter{resps: []*CompleteResponse{
		callTurn(&ToolCall{ID: "a", Name: toolWebSearch, Input: `{"query":"x"}`}, &ToolCall{ID: "b", Name: toolWebFetch, Input: `{"url":`}),
		textTurn("ok"),
	}}
	if _, err := wt.complete(context.Background(), comp, &CompleteRequest{}, noBudget); err != nil {
		t.Fatal(err)
	}
	results := comp.reqs[1].Messages[len(comp.reqs[1].Messages)-1].Blocks
	if !strings.Contains(results[0].ToolResult.Content, "no search engine is configured") {
		t.Errorf("search result = %q", results[0].ToolResult.Content)
	}
	if !strings.Contains(results[1].ToolResult.Content, "invalid arguments") {
		t.Errorf("fetch result = %q", results[1].ToolResult.Content)
	}
}

func TestWebToolsStopAfterMaxRounds(t *testing.T) {
	wt, srv := testWebTools(t, toolWebFetch)
	var resps []*CompleteResponse
	for range maxToolRounds + 1 {
		resps = append(resps, callTurn(&ToolCall{ID: "c", Name: toolWebFetch, Input: `{"url":"` + srv.URL + `"}`}))
	}
	comp := &scriptedCompleter{resps: resps}
	resp, err := wt.complete(context.Background(), comp, &CompleteRequest{Tools: []Tool{{Name: "bash"}}}, noBudget)
	if err != nil {
		t.Fatal(err)
	}
	last := comp.reqs[len(comp.reqs)-1]
	if len(comp.reqs) != maxToolRounds+1 || strings.Join(toolNames(last.Tools), ",") != "bash" {
		t.Fatalf("rounds=%d last tools=%v", len(comp.reqs), toolNames(last.Tools))
	}
	if len(resp.Message.Blocks) != 0 {
		t.Fatalf("server calls of the final round must be dropped: %+v", resp.Message.Blocks)
	}
}

func TestWebToolsCheckBudgetEveryRound(t *testing.T) {
	wt, srv := testWebTools(t, toolWebFetch)
	comp := &scriptedCompleter{resps: []*CompleteResponse{
		callTurn(&ToolCall{ID: "c", Name: toolWebFetch, Input: `{"url":"` + srv.URL + `"}`}),
		textTurn("never sent"),
	}}
	calls := 0
	budget := func([]Message, []Tool) error {
		calls++
		if calls == 2 {
			return errors.New("too big")
		}
		return nil
	}
	if _, err := wt.complete(context.Background(), comp, &CompleteRequest{}, budget); err == nil || err.Error() != "too big" {
		t.Fatalf("err = %v", err)
	}
	if len(comp.reqs) != 1 {
		t.Fatalf("the over-budget round reached upstream: %d requests", len(comp.reqs))
	}
}

func TestWebToolsOffNilIsPassThrough(t *testing.T) {
	var wt *webTools
	tools := []Tool{{Name: toolWebFetch}}
	if got := wt.clientTools(tools); len(got) != 1 || wt.offered() != nil {
		t.Fatalf("a nil *webTools must leave tools untouched: %v %v", got, wt.offered())
	}
}

// failingEngine always fails, like an unreachable SearXNG.
type failingEngine struct{}

func (failingEngine) name() string { return "failing" }
func (failingEngine) search(context.Context, string, int) ([]searchResult, error) {
	return nil, errors.New("connection refused")
}

func TestWebToolsReportEngineFailuresAndEmptyPages(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><div id="app"></div><script>render()</script></body></html>`))
	}))
	defer empty.Close()
	wt, _ := testWebTools(t, toolWebSearch, toolWebFetch)
	wt.engine = failingEngine{}
	search := wt.run(context.Background(), &ToolCall{Name: toolWebSearch, Input: `{"query":"q"}`})
	if !strings.Contains(search, "web_search failed: connection refused") {
		t.Errorf("search = %q", search)
	}
	fetch := wt.run(context.Background(), &ToolCall{Name: toolWebFetch, Input: `{"url":"` + empty.URL + `"}`})
	if !strings.Contains(fetch, "no readable text") {
		t.Errorf("fetch = %q", fetch)
	}
}
