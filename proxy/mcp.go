package main

// mcp.go runs the tools of MCP servers the operator declares, for the model.
// The proxy is the client: it connects to each enabled server at startup
// (stdio processes it starts, or remote Streamable HTTP endpoints), lists
// their tools and answers the model's calls. See
// docs/superpowers/specs/2026-10-10-proxy-mcp-client-design.md.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
)

const maxToolName = 64 // OpenAI-compatible limit on tool names

var serverNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// composeMCPToolName prefixes a server's tool so two servers cannot collide;
// the result stays within OpenAI's 64-character tool-name limit. Calls that
// collide after truncation are refused later, at list time (see listTools).
func composeMCPToolName(server, tool string) string {
	name := server + "__" + tool
	return name[:min(len(name), maxToolName)]
}

// mcpServerSpec is one declared server after parsing.
type mcpServerSpec struct {
	name    string
	command string // stdio: program to exec; "" for an http server
	args    []string
	env     map[string]string
	url     string // http(s): Streamable endpoint; "" for stdio
	headers map[string]string
	enabled bool
}

type enabledServer struct {
	spec mcpServerSpec
}

// parseMCPConfig reads the --mcp-config file (see the design doc for the
// format) and returns the enabled servers. An empty path means MCP is off.
func parseMCPConfig(path string) ([]enabledServer, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("invalid --mcp-config %s: %w", path, err)
	}
	if file.Servers == nil {
		return nil, fmt.Errorf("invalid --mcp-config %s: no mcpServers object", path)
	}
	var out []enabledServer
	for name, raw := range file.Servers {
		if !serverNameRE.MatchString(name) {
			return nil, fmt.Errorf("invalid --mcp-config: server name %q (want [a-zA-Z0-9_-])", name)
		}
		var shape struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Enabled *bool             `json:"enabled"`
		}
		if err := json.Unmarshal(raw, &shape); err != nil {
			return nil, fmt.Errorf("invalid --mcp-config: server %q: %w", name, err)
		}
		s := mcpServerSpec{
			name:    name,
			args:    shape.Args,
			env:     shape.Env,
			headers: shape.Headers,
			enabled: shape.Enabled == nil || *shape.Enabled,
		}
		switch {
		case shape.Type != "" && shape.Type != "http":
			return nil, fmt.Errorf("invalid --mcp-config: server %q: unknown type %q (want a stdio command or \"type\": \"http\")", name, shape.Type)
		case shape.Type == "http":
			u, err := url.Parse(shape.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("invalid --mcp-config: server %q: url must be an absolute http(s) URL, got %q", name, shape.URL)
			}
			s.url = shape.URL
		case shape.Command != "":
			s.command = shape.Command
		default:
			return nil, fmt.Errorf("invalid --mcp-config: server %q: needs \"command\" or \"type\": \"http\"", name)
		}
		if s.enabled {
			out = append(out, enabledServer{spec: s})
		}
	}
	return out, nil
}
