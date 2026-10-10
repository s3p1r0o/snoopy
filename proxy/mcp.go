package main

// mcp.go runs the tools of MCP servers the operator declares, for the model.
// The proxy is the client: it connects to each enabled server at startup
// (stdio processes it starts, or remote Streamable HTTP endpoints), lists
// their tools and answers the model's calls. See
// docs/superpowers/specs/2026-10-10-proxy-mcp-client-design.md.

import (
	"bufio"
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
	"sync"
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
	// A JSON object has no preserved order (stdlib map iteration is random):
	// sort by name so the connect order, the log lines and /debug/tools'
	// serverStatuses come out the same on every boot of the same config.
	sort.Slice(out, func(i, j int) bool { return out[i].spec.name < out[j].spec.name })
	return out, nil
}

var (
	initTimeout = 10 * time.Second // connect + initialize + list tools, per server
	callTimeout = 60 * time.Second // one tool call; fixed by design
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
	// statuses has one entry per enabled server, in declaration order,
	// including the ones that failed to connect (Connected false).
	statuses []mcpServerStatus
}

// mcpServerStatus is the state of one declared MCP server, as the
// /debug/tools endpoint reports it. Targets (command lines, URLs, headers)
// are left out: the config file already holds them, headers may hold secrets.
type mcpServerStatus struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // stdio or http
	Connected bool     `json:"connected"`
	Tools     []string `json:"tools,omitempty"` // server__tool names, sorted
}

// mutexWriter makes one log stream safe for the writers that touch it
// concurrently: a stdio server's stderr scanner runs while the tool loop and
// the startup path write their own lines.
type mutexWriter struct {
	mu   sync.Mutex
	next io.Writer
}

func (w *mutexWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.next.Write(p)
}

// startMCPServers connects to every enabled server; a server that fails to
// start, initialize or list its tools logs one line and is skipped. nil
// (with nil error) when MCP is off.
func startMCPServers(configPath string, log io.Writer) (*mcpManager, error) {
	servers, err := parseMCPConfig(configPath)
	if err != nil || len(servers) == 0 {
		return nil, err
	}
	m := &mcpManager{log: &mutexWriter{next: log}}
	for _, es := range servers {
		kind := "stdio"
		if es.spec.url != "" {
			kind = "http"
		}
		m.statuses = append(m.statuses, mcpServerStatus{Name: es.spec.name, Kind: kind})
		session := connectMCPSession(es.spec, m.log)
		if session == nil {
			continue
		}
		m.statuses[len(m.statuses)-1].Connected = true
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
		transport = &mcp.CommandTransport{Command: newStdioCmd(&spec, log)}
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
// config's env added on top, winning conflicts, so an operator override of
// e.g. NO_COLOR cannot be silently shadowed by the proxy's own environment.
func newStdioCmd(spec *mcpServerSpec, log io.Writer) *exec.Cmd {
	cmd := exec.Command(spec.command, spec.args...)
	cmd.Env = baseEnvWith(spec.env)
	// Pipe the server's stderr into the log, one line at a time, prefixed:
	// a server whose bootstrap fails prints there, and the operator needs it.
	if pipe, err := cmd.StderrPipe(); err == nil {
		go scanStderr(pipe, log, spec.name)
	}
	return cmd
}

// scanStderr logs one prefixed line per stderr line of a stdio server.
func scanStderr(pipe io.ReadCloser, log io.Writer, name string) {
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for scanner.Scan() {
		if log != nil {
			fmt.Fprintf(log, "MCP stderr [%s]: %s\n", name, scanner.Text())
		}
	}
	pipe.Close()
}

// baseEnvWith returns os.Environ, with each of overrides' keys replaced
// (keys not in the inherited environment are appended).
func baseEnvWith(overrides map[string]string) []string {
	env := os.Environ()
	for k, v := range overrides {
		override := k + "=" + v
		replaced := false
		for i, e := range env {
			if strings.HasPrefix(e, k+"=") {
				env[i] = override
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, override)
		}
	}
	return env
}

// listTools caches one def per tool the server announces, named server__tool.
// Paging is followed to the last page: a server may cap each tools/list
// response (the SDK's own server caps at 1000), and tools past page one
// would silently vanish from what the model sees.
func (m *mcpManager) listTools(spec mcpServerSpec, session *mcp.ClientSession) {
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()
	if m.tools == nil {
		m.tools = map[string]*mcpTool{}
	}
	params := &mcp.ListToolsParams{}
	for {
		result, err := session.ListTools(ctx, params)
		if err != nil {
			fmt.Fprintf(m.log, "MCP server %s is connected but listed no tools: %v\n", spec.name, err)
			return
		}
		m.addToolDefs(spec, session, result.Tools)
		if result.NextCursor == "" {
			return
		}
		params.Cursor = result.NextCursor
	}
}

// addToolDefs caches the defs of one tools/list page.
func (m *mcpManager) addToolDefs(spec mcpServerSpec, session *mcp.ClientSession, tools []*mcp.Tool) {
	for _, tool := range tools {
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

// statuses reports one entry per enabled server, in declaration order, each
// with its tools from the connected set. nil (as nil, not an empty slice)
// when MCP is off, so the JSON omits the field.
func (m *mcpManager) serverStatuses() []mcpServerStatus {
	if m == nil {
		return nil
	}
	out := append([]mcpServerStatus(nil), m.statuses...)
	for i := range out {
		for _, tool := range m.tools {
			if tool.server == out[i].Name {
				out[i].Tools = append(out[i].Tools, tool.def.Name)
			}
		}
		sort.Strings(out[i].Tools)
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
	return capMCPResult(sb.String())
}

// capMCPResult bounds a rendered result like a fetched page is bounded:
// unbounded tool output would enter the conversation and can alone push a
// later request over the token budget, which the model could not recover
// from, since its own tool result caused it.
func capMCPResult(s string) string {
	if len(s) <= maxPageMarkdownChars {
		return s
	}
	return s[:maxPageMarkdownChars] + fmt.Sprintf("\n\n(result longer than %d characters; truncated)\n", maxPageMarkdownChars)
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
