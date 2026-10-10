package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
