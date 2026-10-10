package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeCompleter records the last request and serves a canned response or error.
type fakeCompleter struct {
	req  *CompleteRequest
	resp *CompleteResponse
	err  error
}

func (f *fakeCompleter) complete(ctx context.Context, req *CompleteRequest) (*CompleteResponse, error) {
	f.req = req
	return f.resp, f.err
}

func newTestHandler(comp *fakeCompleter, cfg serveConfig) http.Handler {
	return newServeHandler(cfg, comp)
}

func decodeError(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("response is not JSON: %s", body)
	}
	errObj, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error envelope: %s", body)
	}
	return errObj
}

// TestModelsReturnsSingleModel pins the /v1/models response shape.
func TestModelsReturnsSingleModel(t *testing.T) {
	h := newTestHandler(&fakeCompleter{}, serveConfig{})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "list" || len(body.Data) != 1 {
		t.Fatalf("unexpected body: %s", rec.Body.Bytes())
	}
	if body.Data[0].ID != "rill-assistant" || body.Data[0].Object != "model" || body.Data[0].OwnedBy != "rill" {
		t.Fatalf("unexpected model: %+v", body.Data[0])
	}
}

// TestChatCompletionEchosUpstream pins the non-streaming completion response shape and usage relay.
func TestChatCompletionEchosUpstream(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{
		Message:      &Message{Role: "assistant", Blocks: []Block{{Text: "pong"}}},
		InputTokens:  13,
		OutputTokens: 4,
		Provider:     "openai",
	}}
	h := newTestHandler(comp, serveConfig{})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"anything-else","messages":[{"role":"user","content":"ping"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.Bytes())
	}
	var got completionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Object != "chat.completion" || got.Model != "anything-else" {
		t.Fatalf("unexpected envelope: %+v", got)
	}
	if got.ID == "" || got.Created == 0 || !strings.HasPrefix(got.ID, "chatcmpl-") {
		t.Fatalf("missing id/created: %+v", got)
	}
	if len(got.Choices) != 1 || got.Choices[0].Index != 0 {
		t.Fatalf("unexpected choices: %+v", got.Choices)
	}
	if got.Choices[0].Message.Content != "pong" || got.Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected choice: %+v", got.Choices[0])
	}
	if got.Usage.PromptTokens != 13 || got.Usage.CompletionTokens != 4 || got.Usage.TotalTokens != 17 {
		t.Fatalf("unexpected usage: %+v", got.Usage)
	}
	// The client's model is ignored; the upstream receives the translated message.
	if comp.req == nil || len(comp.req.Messages) != 1 || comp.req.Messages[0].Role != "user" || comp.req.Messages[0].Blocks[0].Text != "ping" {
		t.Fatalf("unexpected upstream request: %+v", comp.req)
	}
}

// TestChatCompletionToolCalls pins the tool_calls response translation and finish reason.
func TestChatCompletionToolCalls(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{
		Message: &Message{Role: "assistant", Blocks: []Block{
			{ToolCall: &ToolCall{ID: "call_1", Name: "weather", Input: `{"city":"Paris"}`}},
		}},
	}}
	h := newTestHandler(comp, serveConfig{})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"rill-assistant","messages":[{"role":"user","content":"weather?"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.Bytes())
	}
	var got completionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	c := got.Choices[0]
	if c.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", c.FinishReason)
	}
	tc := c.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "weather" || tc.Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
}

// TestChatSendsToolResultAsUserMessage pins the tool-role translation and tool-call argument pass-through.
func TestChatSendsToolResultAsUserMessage(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{Message: &Message{Role: "assistant", Blocks: []Block{{Text: "ok"}}}}}
	h := newTestHandler(comp, serveConfig{})
	body := `{"model":"rill-assistant","messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"12C sunny"}
	],"tools":[{"type":"function","function":{"name":"weather","description":"get weather","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.Bytes())
	}
	if len(comp.req.Messages) != 3 {
		t.Fatalf("message count = %d: %+v", len(comp.req.Messages), comp.req.Messages)
	}
	// tool role arrives wrapped as a user message carrying a tool_result block.
	toolMsg := comp.req.Messages[2]
	if toolMsg.Role != "user" || toolMsg.Blocks[0].ToolResult == nil ||
		toolMsg.Blocks[0].ToolResult.ID != "call_1" || toolMsg.Blocks[0].ToolResult.Content != "12C sunny" {
		t.Fatalf("unexpected tool result translation: %+v", toolMsg)
	}
	// assistant tool_calls arrive as tool_call blocks with arguments passed through.
	assistantMsg := comp.req.Messages[1]
	if assistantMsg.Blocks[0].ToolCall == nil ||
		assistantMsg.Blocks[0].ToolCall.Name != "weather" || assistantMsg.Blocks[0].ToolCall.Input != `{"city":"Paris"}` ||
		assistantMsg.Blocks[0].ToolCall.ID != "call_1" {
		t.Fatalf("unexpected assistant tool call translation: %+v", assistantMsg)
	}
	if len(comp.req.Tools) != 1 || comp.req.Tools[0].Name != "weather" || comp.req.Tools[0].Description != "get weather" ||
		comp.req.Tools[0].InputSchema != `{"type":"object"}` {
		t.Fatalf("unexpected tools translation: %+v", comp.req.Tools)
	}
}

// TestChatInvalidBodyYieldsBadRequestEnvelope pins the 400 envelope for malformed JSON.
func TestChatInvalidBodyYieldsBadRequestEnvelope(t *testing.T) {
	h := newTestHandler(&fakeCompleter{}, serveConfig{})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	errObj := decodeError(t, rec.Body.Bytes())
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("type = %v, want invalid_request_error: %s", errObj["type"], rec.Body.Bytes())
	}
	msg, _ := errObj["message"].(string)
	if !strings.HasPrefix(msg, "invalid request body:") {
		t.Fatalf("message = %q", msg)
	}
}

// TestChatUnsupportedRoleRejected pins the 400 for unsupported roles.
func TestChatUnsupportedRoleRejected(t *testing.T) {
	h := newTestHandler(&fakeCompleter{}, serveConfig{})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"rill-assistant","messages":[{"role":"developer","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	errObj := decodeError(t, rec.Body.Bytes())
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("type = %v, want invalid_request_error", errObj["type"])
	}
}

// TestChatImageContentRejected pins the clean rejection of unsupported content types.
func TestChatImageContentRejected(t *testing.T) {
	h := newTestHandler(&fakeCompleter{}, serveConfig{})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"rill-assistant","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	errObj := decodeError(t, rec.Body.Bytes())
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("type = %v, want invalid_request_error", errObj["type"])
	}
}

// TestChatUpstreamFailureRelayed pins the 502 envelope for upstream errors.
func TestChatUpstreamFailureRelayed(t *testing.T) {
	comp := &fakeCompleter{err: fmt.Errorf("connection refused")}
	h := newTestHandler(comp, serveConfig{})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"rill-assistant","messages":[{"role":"user","content":"ping"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	errObj := decodeError(t, rec.Body.Bytes())
	if errObj["type"] != "api_error" {
		t.Fatalf("type = %v, want api_error", errObj["type"])
	}
	if msg, _ := errObj["message"].(string); !strings.HasPrefix(msg, "upstream completion failed:") {
		t.Fatalf("message = %q", msg)
	}
}

// TestChatContextLengthExceeded pins the local input-size guard's envelope and code.
func TestChatContextLengthExceeded(t *testing.T) {
	h := newTestHandler(&fakeCompleter{}, serveConfig{maxInputTokens: 10})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":"rill-assistant","messages":[{"role":"user","content":"%s"}]}`, strings.Repeat("a", 400))))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	errObj := decodeError(t, rec.Body.Bytes())
	if errObj["type"] != "invalid_request_error" || errObj["code"] != "context_length_exceeded" {
		t.Fatalf("unexpected error: %s", rec.Body.Bytes())
	}
}

// TestAuthGate pins the bearer-key gate on chat completions when a key is configured.
func TestAuthGate(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{Message: &Message{Role: "assistant", Blocks: []Block{{Text: "pong"}}}}}
	h := newTestHandler(comp, serveConfig{key: "secret"})
	body := `{"model":"rill-assistant","messages":[{"role":"user","content":"ping"}]}`

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: status = %d, want 401", rec.Code)
	}
	if ww := rec.Header().Get("WWW-Authenticate"); ww != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q", ww)
	}
	decodeError(t, rec.Body.Bytes())

	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key: status = %d, want 200", rec.Code)
	}

	// Without a configured key the endpoint stays open.
	h = newTestHandler(comp, serveConfig{})
	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no key configured: status = %d, want 200", rec.Code)
	}
}

// TestStreamingSingleChunk pins the SSE shape: role delta, one full-content delta, finish, [DONE].
func TestStreamingSingleChunk(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{
		Message: &Message{Role: "assistant", Blocks: []Block{{Text: "hello world"}}},
	}}
	h := newTestHandler(comp, serveConfig{})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"say hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.Bytes())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	raw := rec.Body.String()
	var sse []string
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			sse = append(sse, data)
		}
	}
	if len(sse) != 4 || sse[3] != "[DONE]" {
		t.Fatalf("expected 3 chunks + [DONE], got %d events:\n%s", len(sse), raw)
	}
	var first, mid, last struct {
		Object  string `json:"object"`
		Choices []struct {
			Delta map[string]any `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(sse[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Object != "chat.completion.chunk" || first.Choices[0].Delta["role"] != "assistant" {
		t.Fatalf("unexpected role chunk: %s", sse[0])
	}
	if err := json.Unmarshal([]byte(sse[1]), &mid); err != nil {
		t.Fatal(err)
	}
	if mid.Choices[0].Delta["content"] != "hello world" {
		t.Fatalf("unexpected content chunk: %s", sse[1])
	}
	if err := json.Unmarshal([]byte(sse[2]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Choices[0].Delta["role"] != nil {
		t.Fatalf("finish chunk should carry no delta: %s", sse[2])
	}
}

// TestHealthzProbesOK serves a liveness probe. (New endpoint; fails until implemented.)
func TestHealthzProbesOK(t *testing.T) {
	h := newTestHandler(&fakeCompleter{}, serveConfig{})
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestModelsBehindKey requires the bearer key on /v1/models when one is configured.
// (New; /v1/models was open before.)
func TestModelsBehindKey(t *testing.T) {
	h := newTestHandler(&fakeCompleter{}, serveConfig{key: "secret"})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: status = %d, want 401", rec.Code)
	}
	decodeError(t, rec.Body.Bytes())

	req = httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key: status = %d, want 200", rec.Code)
	}
}

// TestChatRequestBodyTooLarge rejects bodies above the configured cap with a 413 envelope.
func TestChatRequestBodyTooLarge(t *testing.T) {
	cfg := serveConfig{maxRequestBodyBytes: 16}
	h := newTestHandler(&fakeCompleter{}, cfg)
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"rill-assistant","messages":[{"role":"user","content":"this body is definitely longer than sixteen characters"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	errObj := decodeError(t, rec.Body.Bytes())
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("type = %v, want invalid_request_error: %s", errObj["type"], rec.Body.Bytes())
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "request body too large") {
		t.Fatalf("message = %q", msg)
	}
}

// TestRequestLogging writes one access-log line per request to the configured log writer.
func TestRequestLogging(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{Message: &Message{Role: "assistant", Blocks: []Block{{Text: "pong"}}}}}
	cfg := serveConfig{}
	var buf bytes.Buffer
	wrapped := newRequestLogger(newTestHandler(comp, cfg), &buf)

	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"ping"}]}`)))

	got := buf.String()
	if got == "" {
		t.Fatal("no log output")
	}
	for _, want := range []string{"GET", "/healthz", "200", "POST", "/v1/chat/completions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q:\n%s", want, got)
		}
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one line per request, got %d:\n%s", len(lines), got)
	}
}

// TestServeServerTimeouts pins the http.Server hardening set by newServeServer.
func TestServeServerTimeouts(t *testing.T) {
	srv := newServeServer(serveConfig{port: 0}, &fakeCompleter{})
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("http.Server timeouts not all set: header=%v read=%v write=%v idle=%v",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.Addr != ":0" {
		t.Fatalf("addr = %q, want :0", srv.Addr)
	}
}

// TestServeListenAddress pins the --address flag: empty keeps the legacy
// all-interfaces binding; set, it restricts the interface (e.g. loopback only).
func TestServeListenAddress(t *testing.T) {
	for _, tc := range []struct {
		address string
		port    int
		want    string
	}{
		{"", 8081, ":8081"},
		{"127.0.0.1", 8081, "127.0.0.1:8081"},
		{"127.0.0.1", 0, "127.0.0.1:0"},
	} {
		srv := newServeServer(serveConfig{address: tc.address, port: tc.port}, &fakeCompleter{})
		if srv.Addr != tc.want {
			t.Fatalf("address %q port %d: addr = %q, want %q", tc.address, tc.port, srv.Addr, tc.want)
		}
	}
}

// TestServerSystemPromptPrepended pins the server-level system prompt: when set,
// it is sent to the upstream as the first message, before any client message.
func TestServerSystemPromptPrepended(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{Message: &Message{Role: "assistant", Blocks: []Block{{Text: "ok"}}}}}
	h := newTestHandler(comp, serveConfig{systemPrompt: "Be terse."})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"system","content":"answer in French"},{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.Bytes())
	}
	got := comp.req.Messages
	if len(got) != 3 {
		t.Fatalf("message count = %d: %+v", len(got), got)
	}
	if got[0].Role != "system" || got[0].Blocks[0].Text != "Be terse." {
		t.Fatalf("first message = %+v, want server system prompt", got[0])
	}
	if got[1].Blocks[0].Text != "answer in French" {
		t.Fatalf("client system message misplaced: %+v", got[1])
	}
	if got[2].Blocks[0].Text != "hi" {
		t.Fatalf("user message misplaced: %+v", got[2])
	}
}

// TestServerSystemPromptEmptyInjectsNothing pins the default: an empty --system-prompt
// must leave the translated conversation byte-identical to a run without the flag.
func TestServerSystemPromptEmptyInjectsNothing(t *testing.T) {
	comp := &fakeCompleter{resp: &CompleteResponse{Message: &Message{Role: "assistant", Blocks: []Block{{Text: "ok"}}}}}
	h := newTestHandler(comp, serveConfig{}) // systemPrompt empty (default)
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"system","content":"client rule"},{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.Bytes())
	}
	got := comp.req.Messages
	if len(got) != 2 || got[0].Blocks[0].Text != "client rule" {
		t.Fatalf("unexpected messages: %+v", got)
	}
}

// TestResolveKeyPrecedence pins --key > env RILL_PROXY_KEY > empty, so secrets
// can be passed without landing on a command line visible in `ps`.
func TestResolveKeyPrecedence(t *testing.T) {
	if got := resolveKey("flag-value"); got != "flag-value" {
		t.Fatalf("resolveKey(flag) = %q, want \"flag-value\" (flag wins over env)", got)
	}
	t.Setenv("RILL_PROXY_KEY", "env-secret")
	if got := resolveKey(""); got != "env-secret" {
		t.Fatalf("resolveKey(\"\") = %q, want \"env-secret\"", got)
	}
	t.Setenv("RILL_PROXY_KEY", "")
	if got := resolveKey(""); got != "" {
		t.Fatalf("resolveKey(\"\") with empty env = %q, want \"\"", got)
	}
}

// TestVersionFlag prints the version on --version and exits before dialing upstream.
func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	runServe([]string{"--version"}, &out, &errOut)
	if got := strings.TrimSpace(out.String()); got != "proxy "+version {
		t.Fatalf("output = %q, want %q", got, "proxy "+version)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

// TestDebugToolsReportsOfferedToolsAndMCPState pins the /debug/tools
// endpoint: bearer-key protected like /v1/models, the full offered-tool
// list with a source per tool, and one entry per declared MCP server with
// its connection state (targets — URLs, command lines, headers — stay out:
// the config file holds them, headers may hold secrets).
func TestDebugToolsReportsOfferedToolsAndMCPState(t *testing.T) {
	path := writeMCPConfig(t, fmt.Sprintf(`{"mcpServers":{"sh":{"command":%q,"env":{"SNOOPY_MCP_STDIO_HELPER":"echo"}}}}`, os.Args[0]))
	var log bytes.Buffer
	m, err := startMCPServers(path, &log)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	web, err := newWebTools("fetch", "", "", "", &log, m)
	if err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(&fakeCompleter{}, serveConfig{key: "secret", web: web, mcp: m})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/tools", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: status = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest("GET", "/debug/tools", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with key: status = %d, want 200; body = %s", rec.Code, rec.Body.Bytes())
	}
	var body struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Source      string          `json:"source"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		MCPServers []mcpServerStatus `json:"mcpServers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %s", rec.Body.Bytes())
	}
	var webFetch, shEcho *struct {
		Name, Description, Source string
	}
	for i := range body.Tools {
		switch body.Tools[i].Name {
		case "web_fetch":
			tool := struct{ Name, Description, Source string }{body.Tools[i].Name, body.Tools[i].Description, body.Tools[i].Source}
			webFetch = &tool
		case "sh__echo":
			tool := struct{ Name, Description, Source string }{body.Tools[i].Name, body.Tools[i].Description, body.Tools[i].Source}
			shEcho = &tool
		}
	}
	if webFetch == nil {
		t.Fatalf("tools = %s: web_fetch missing", rec.Body.Bytes())
	}
	if webFetch.Source != "web" {
		t.Fatalf("web_fetch source = %q, want web", webFetch.Source)
	}
	if shEcho == nil {
		t.Fatalf("tools = %s: sh__echo missing", rec.Body.Bytes())
	}
	if shEcho.Source != "mcp" {
		t.Fatalf("sh__echo source = %q, want mcp", shEcho.Source)
	}
	if len(body.MCPServers) != 1 || body.MCPServers[0].Name != "sh" || body.MCPServers[0].Kind != "stdio" || !body.MCPServers[0].Connected {
		t.Fatalf("mcpServers = %+v", body.MCPServers)
	}
	if len(body.MCPServers[0].Tools) != 2 || body.MCPServers[0].Tools[0] != "sh__boom" || body.MCPServers[0].Tools[1] != "sh__echo" {
		t.Fatalf("mcpServers[0].tools = %v", body.MCPServers[0].Tools)
	}

	// No web tools and no MCP: empty lists, still 200.
	h = newTestHandler(&fakeCompleter{}, serveConfig{key: "secret"})
	req = httptest.NewRequest("GET", "/debug/tools", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bare proxy: status = %d, want 200; body = %s", rec.Code, rec.Body.Bytes())
	}
	var bare map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &bare); err != nil {
		t.Fatalf("bare body is not JSON: %s", rec.Body.Bytes())
	}
	if bare["tools"] == nil || string(bare["tools"]) != "[]" {
		t.Fatalf("bare tools = %s, want []", bare["tools"])
	}
}
