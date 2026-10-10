package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// writeMCPConfig writes a config file and returns its path.
func writeMCPConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseMCPConfigStdioAndHTTP(t *testing.T) {
	path := writeMCPConfig(t, `{"mcpServers":{
		"git": {"command":"uvx","args":["mcp-server-git"],"env":{"NO_COLOR":"1"},"enabled":false},
		"rill": {"type":"http","url":"https://mcp.example.com/mcp","headers":{"Authorization":"Bearer x"}}
	}}`)
	servers, err := parseMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %d, want only rill (git is disabled)", len(servers))
	}
	s := servers[0].spec
	if s.name != "rill" || s.url != "https://mcp.example.com/mcp" || s.headers["Authorization"] != "Bearer x" || !s.enabled {
		t.Fatalf("parsed = %+v", s)
	}

	// A stdio server parses with its args; enabled defaults to true.
	path = writeMCPConfig(t, `{"mcpServers":{"git":{"command":"uvx","args":["mcp-server-git"],"env":{"NO_COLOR":"1"}}}}`)
	servers, err = parseMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("enabled defaults to true; servers = %d", len(servers))
	}
	if s = servers[0].spec; s.command != "uvx" || s.name != "git" || len(s.args) != 1 || s.env["NO_COLOR"] != "1" || !s.enabled {
		t.Fatalf("parsed = %+v", s)
	}

	// A server with enabled:false parses but stays out of the enabled list.
	path = writeMCPConfig(t, `{"mcpServers":{"off":{"command":"x","enabled":false}}}`)
	servers, err = parseMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Fatalf("enabled:false must be filtered; servers = %d", len(servers))
	}
}

func TestParseMCPConfigErrors(t *testing.T) {
	path := writeMCPConfig(t, "not json")
	if _, err := parseMCPConfig(path); err == nil {
		t.Fatal("unparsable file must error")
	}
	for _, bad := range []string{
		`{}`,
		`{"mcpServers":{"bad name":{"command":"x"}}}`,
		`{"mcpServers":{"a":{"type":"ssh","url":"https://x"}}}`,
		`{"mcpServers":{"a":{"type":"http"}}}`,
		`{"mcpServers":{"a":{"type":"http","url":"ftp://x"}}}`,
		`{"mcpServers":{"a":{}}}`,
	} {
		path := writeMCPConfig(t, bad)
		if _, err := parseMCPConfig(path); err == nil {
			t.Errorf("parseMCPConfig(%s) accepted", bad)
		}
	}

	// An empty mcpServers map is a valid, idle config.
	empty := writeMCPConfig(t, `{"mcpServers":{}}`)
	servers, err := parseMCPConfig(empty)
	if err != nil || servers != nil {
		t.Fatalf("empty mcpServers = (%v, %v)", servers, err)
	}
	// The empty path stands for MCP off.
	if servers, err = parseMCPConfig(""); servers != nil || err != nil {
		t.Fatalf("empty path = (%v, %v), want (nil, nil)", servers, err)
	}
}

func TestComposeMCPToolName(t *testing.T) {
	tool := composeMCPToolName("github", "list_issues")
	if tool != "github__list_issues" {
		t.Fatalf("name = %q", tool)
	}
	// A server+tool pair landing exactly on the OpenAI limit is kept whole.
	if got := composeMCPToolName(strings.Repeat("s", 30), strings.Repeat("n", 32)); len(got) != maxToolName {
		t.Fatalf("len = %d, want %d", len(got), maxToolName)
	}
	// An over-long pair is truncated to the limit.
	if got := composeMCPToolName(strings.Repeat("s", 40), strings.Repeat("n", 40)); len(got) != maxToolName {
		t.Fatalf("len = %d, want %d", len(got), maxToolName)
	}
}

// mcpTestServer is an in-process MCP server with two tools.
func mcpTestServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "echo its input",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echoed: " + in.Text}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "boom", Description: "always fails"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deliberate failure"}}, IsError: true}, nil, nil
		})
	return s
}

// connectTestSession connects one client session to one in-process server.
// In-memory transports cover the whole protocol path of the manager without
// subprocesses; Task 4's tests exercise the real stdio transport.
func connectTestSession(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	clientC, serverC := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go server.Run(ctx, serverC)
	c, err := mcp.NewClient(&mcp.Implementation{Name: "proxy-test", Version: "v0.1.0"}, nil).Connect(ctx, clientC, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// newTestManager registers one in-memory server's tools under name "test",
// through the manager's own listTools path.
func newTestManager(t *testing.T, server *mcp.Server) *mcpManager {
	t.Helper()
	m := &mcpManager{log: &bytes.Buffer{}}
	session := connectTestSession(t, server)
	m.sessions = append(m.sessions, session)
	m.listTools(mcpServerSpec{name: "test"}, session)
	return m
}

func TestMCPDefsAndCalls(t *testing.T) {
	m := newTestManager(t, mcpTestServer())
	defs := m.defs()
	if len(defs) != 2 {
		t.Fatalf("defs = %d tools", len(defs))
	}
	// defs are sorted by name.
	if defs[0].Name != "test__boom" || defs[1].Name != "test__echo" {
		t.Fatalf("defs = %v, %v", defs[0].Name, defs[1].Name)
	}
	if defs[1].Description != "echo its input" {
		t.Fatalf("description = %q", defs[1].Description)
	}
	if !strings.Contains(defs[1].InputSchema, `"type":"object"`) {
		t.Fatalf("input schema = %q", defs[1].InputSchema)
	}

	out, err := m.run(context.Background(), "test__echo", `{"text":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, untrustedNote) {
		t.Fatalf("result is not untrusted-note prefixed: %q", out)
	}
	if !strings.Contains(out, "echoed: hi") {
		t.Fatalf("result = %q", out)
	}

	// An isError result is a result, not a Go error: the model sees the text.
	out, err = m.run(context.Background(), "test__boom", "{}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deliberate failure") || !strings.Contains(out, "test__boom answered an error") {
		t.Fatalf("isError result = %q", out)
	}
}

func TestMCPOutsideCalls(t *testing.T) {
	m := newTestManager(t, mcpTestServer())
	// An unknown tool is an error for dispatch, with no result text.
	out, err := m.run(context.Background(), "test__unknown", "{}")
	if err == nil || !strings.Contains(err.Error(), "no such MCP tool") {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Fatalf("out = %q", out)
	}
	// Arguments that are not JSON become a result: the model recovers.
	out, err = m.run(context.Background(), "test__echo", "not json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "failed") || strings.HasPrefix(out, untrustedNote) {
		t.Fatalf("invalid-arguments result = %q", out)
	}
}

func TestMCPTwoServersBothAppear(t *testing.T) {
	m := &mcpManager{log: &bytes.Buffer{}}
	for _, name := range []string{"alpha", "beta"} {
		session := connectTestSession(t, mcpTestServer())
		m.sessions = append(m.sessions, session)
		m.listTools(mcpServerSpec{name: name}, session)
	}
	got := map[string]bool{}
	for _, def := range m.defs() {
		got[def.Name] = true
	}
	if !got["alpha__echo"] || !got["beta__echo"] {
		t.Fatalf("def names = %v, want both namespaces", got)
	}
}

func TestMCPTruncationCollisionIgnored(t *testing.T) {
	// Two tools whose names land on the same 64-character prefix: a cut of
	// A...A + "a" and A...A + "b" after the prefix collide; the second must
	// be ignored and logged, never silently shadow the first.
	long := strings.Repeat("A", 62)
	s := mcp.NewServer(&mcp.Implementation{Name: "long", Version: "1.0.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: long + "a"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "first"}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: long + "b"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "second"}}}, nil, nil
		})
	log := &bytes.Buffer{}
	m := &mcpManager{log: log}
	session := connectTestSession(t, s)
	m.sessions = append(m.sessions, session)
	m.listTools(mcpServerSpec{name: "s"}, session)
	if len(m.tools) != 1 {
		t.Fatalf("tools = %d, want 1 (one collision ignored)", len(m.tools))
	}
	if !strings.Contains(log.String(), "collide") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestWebToolsOfferMCPTools(t *testing.T) {
	m := newTestManager(t, mcpTestServer())
	w := &webTools{owned: map[string]bool{}, mcp: m}
	seen := map[string]bool{}
	for _, def := range w.offered() {
		switch def.Name {
		case "test__echo", "test__boom":
			seen[def.Name] = true
		default:
			t.Errorf("unexpected offered tool %q", def.Name)
		}
	}
	if !seen["test__echo"] || !seen["test__boom"] {
		t.Fatalf("offered = %v", seen)
	}
	// The model's call to an MCP tool runs through the loop's run/dispatch,
	// with the TOOL log line the other server-side tools get.
	log := &bytes.Buffer{}
	w.log = log
	out := w.run(context.Background(), &ToolCall{ID: "t1", Name: "test__echo", Input: `{"text":"hi"}`})
	if !strings.Contains(out, "untrusted") || !strings.Contains(out, "echoed: hi") {
		t.Fatalf("run output = %q", out)
	}
	if !strings.Contains(log.String(), "TOOL test__echo") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestWebToolsMCPUnknownTool(t *testing.T) {
	// A call the loop knows nothing about still answers unknown tool —
	// with the MCP manager attached too.
	w := &webTools{owned: map[string]bool{}, mcp: &mcpManager{log: &bytes.Buffer{}}}
	out := w.run(context.Background(), &ToolCall{ID: "t2", Name: "noSuch", Input: "{}"})
	if !strings.Contains(out, "failed") && !strings.Contains(out, "unknown") {
		t.Fatalf("run output = %q", out)
	}
}

func TestStdioCommandBuildsEnvironment(t *testing.T) {
	t.Setenv("FOO", "inherited")
	spec := mcpServerSpec{name: "local", command: "sh", args: []string{"-c", "true"}, env: map[string]string{"FOO": "bar"}}
	cmd := newStdioCmd(&spec)
	if len(cmd.Args) != 3 || cmd.Args[0] != "sh" || cmd.Args[1] != "-c" {
		t.Fatalf("args = %v", cmd.Args)
	}
	var foundPath, foundFoo bool
	for _, e := range cmd.Env {
		if e == "FOO=bar" {
			foundFoo = true
		}
		if e == "FOO=inherited" {
			t.Errorf("config env must win over the inherited FOO, but FOO=inherited is present")
		}
		if strings.HasPrefix(e, "PATH=") {
			foundPath = true
		}
	}
	if !foundPath || !foundFoo {
		t.Fatalf("env must inherit PATH and add FOO=bar, got %v", cmd.Env)
	}
}

func TestStdioServerInitTimeout(t *testing.T) {
	// A server that never answers initialize: the manager logs and moves on.
	path := writeMCPConfig(t, `{"mcpServers":{"slow":{"command":"sh","args":["-c","sleep 30"]}}}`)
	savedInit := initTimeout
	initTimeout = 300 * time.Millisecond
	defer func() { initTimeout = savedInit }()
	var log bytes.Buffer
	m, err := startMCPServers(path, &log)
	if err != nil || m == nil {
		t.Fatalf("start = (%v, %v)", m, err)
	}
	if got := len(m.tools); got != 0 {
		t.Fatalf("tools = %d, want 0", got)
	}
	if !strings.Contains(log.String(), "unavailable") {
		t.Fatalf("log = %q", log.String())
	}
	// The (timed-out) helper process must not outlive the test.
	m.close()
}
