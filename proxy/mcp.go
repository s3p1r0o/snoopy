package main

// mcp.go runs the tools of MCP servers the operator declares, for the model.
// The proxy is the client: it connects to each enabled server at startup
// (stdio processes it starts, or remote Streamable HTTP endpoints), lists
// their tools and answers the model's calls. See
// docs/superpowers/specs/2026-10-10-proxy-mcp-client-design.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

const (
	initTimeout = 10 * time.Second
	callTimeout = 60 * time.Second
)

// mcpTool is one tool of one connected server.
type mcpTool struct {
	def     Tool // Name is server__tool; InputSchema is the JSON the server sent
	server  string
	tool    string
	session *mcp.ClientSession
}

// mcpManager holds every connected MCP server and their tools.
type mcpManager struct {
	tools    map[string]*mcpTool
	sessions []*mcp.ClientSession
	log      io.Writer
}

// startMCPServers connects to every enabled server; a server that fails to
// start, initialize or list its tools logs one line and is skipped. nil
// (with nil error) when MCP is off.
func startMCPServers(configPath string, log io.Writer) (*mcpManager, error) {
	servers, err := parseMCPConfig(configPath)
	if err != nil || len(servers) == 0 {
		return nil, err
	}
	m := &mcpManager{log: log}
	for _, es := range servers {
		session := connectMCPSession(es.spec, log)
		if session == nil {
			continue
		}
		m.sessions = append(m.sessions, session)
		m.listTools(es.spec, session)
	}
	return m, nil
}

// connectMCPSession builds the transport a spec calls for and performs the
// MCP handshake. A nil session (with the failure logged) is skipped, not
// fatal: graceful degradation is the design.
func connectMCPSession(spec mcpServerSpec, log io.Writer) *mcp.ClientSession {
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	var transport mcp.Transport
	switch {
	case spec.url != "":
		transport = &mcp.StreamableClientTransport{Endpoint: spec.url, HTTPClient: &http.Client{
			Transport: &headerRoundTripper{headers: spec.headers, next: safeTransport()},
		}}
	default:
		transport = &mcp.CommandTransport{Command: newStdioCmd(&spec)}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "snoopy-proxy", Version: "v0.1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Fprintf(log, "MCP server %s unavailable: %v\n", spec.name, err)
		return nil
	}
	return session
}

// headerRoundTripper copies the config's headers onto every MCP HTTP request.
type headerRoundTripper struct {
	headers map[string]string
	next    http.RoundTripper
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.next.RoundTrip(req)
}

// safeTransport avoids http.DefaultTransport sharing: it honors HTTPS_PROXY
// for remote servers, but nothing from the fetcher leaks into it.
func safeTransport() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	return t
}

// newStdioCmd builds the process command of a stdio server: the proxy's
// inherited environment (servers need PATH as much as anything) with the
// config's env added on top, winning conflicts. Its stderr goes into the log
// stream, one buffered line, so server chatter never interleaves.
func newStdioCmd(spec *mcpServerSpec) *exec.Cmd {
	cmd := exec.Command(spec.command, spec.args...)
	cmd.Env = append(os.Environ(), envList(spec.env)...)
	cmd.Stderr = io.Discard
	return cmd
}

func envList(env map[string]string) []string {
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// listTools caches one def per tool the server announces, named server__tool.
func (m *mcpManager) listTools(spec mcpServerSpec, session *mcp.ClientSession) {
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(m.log, "MCP server %s is connected but listed no tools: %v\n", spec.name, err)
		return
	}
	if m.tools == nil {
		m.tools = map[string]*mcpTool{}
	}
	for _, tool := range result.Tools {
		name := composeMCPToolName(spec.name, tool.Name)
		schema := `{"type":"object"}`
		if b, err := json.Marshal(tool.InputSchema); err == nil && len(b) > 0 {
			schema = string(b)
		}
		if m.tools[name] != nil {
			// A name cut at 64 characters can collide with another tool's.
			fmt.Fprintf(m.log, "MCP server %s tool %s ignored: its name would collide with %q\n", spec.name, tool.Name, name)
			continue
		}
		m.tools[name] = &mcpTool{
			def:     Tool{Name: name, Description: tool.Description, InputSchema: schema},
			server:  spec.name,
			tool:    tool.Name,
			session: session,
		}
	}
}

// defs returns one Tool per offered tool, sorted by name.
func (m *mcpManager) defs() []Tool {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.tools))
	for name := range m.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, name := range names {
		out = append(out, m.tools[name].def)
	}
	return out
}

// run executes one named MCP tool call. Call failures and isError results
// become the result text, so the model can recover; the error return means
// the tool did not exist at all.
func (m *mcpManager) run(ctx context.Context, name, input string) (string, error) {
	tool := m.tools[name]
	if tool == nil {
		return "", fmt.Errorf("no such MCP tool %q", name)
	}
	var args any
	if strings.TrimSpace(input) != "" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return fmt.Sprintf("%s failed: invalid arguments (%v)", name, err), nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	result, err := tool.session.CallTool(ctx, &mcp.CallToolParams{Name: tool.tool, Arguments: args})
	if err != nil {
		return fmt.Sprintf("%s failed: %v", name, err), nil
	}
	return untrustedNote + renderMCPResult(name, result), nil
}

// renderMCPResult turns an MCP result into the tool-result string: text
// blocks in order, structured content serialized after them, other content
// types noted but not rendered.
func renderMCPResult(name string, result *mcp.CallToolResult) string {
	var sb strings.Builder
	if result.IsError {
		fmt.Fprintf(&sb, "%s answered an error:\n", name)
	}
	for _, c := range result.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(text.Text)
			sb.WriteString("\n")
			continue
		}
		fmt.Fprintf(&sb, "(%s content not rendered)\n", contentKind(c))
	}
	if result.StructuredContent != nil {
		if b, err := json.Marshal(result.StructuredContent); err == nil {
			sb.WriteString("\n")
			sb.Write(b)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// contentKind names the content type, for the not-rendered note.
func contentKind(c mcp.Content) string {
	switch c.(type) {
	case *mcp.ImageContent:
		return "image"
	case *mcp.AudioContent:
		return "audio"
	case *mcp.ResourceLink:
		return "resource link"
	case *mcp.EmbeddedResource:
		return "embedded resource"
	default:
		return "unrenderable"
	}
}

// close ends every session, which also stops the stdio servers.
func (m *mcpManager) close() {
	if m == nil {
		return
	}
	for _, s := range m.sessions {
		_ = s.Close()
	}
}
