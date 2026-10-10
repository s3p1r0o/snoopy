package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// serveConfig carries the resolved configuration for one serve run.
type serveConfig struct {
	port                int
	address             string // bind host or address prefix; empty keeps the legacy all-interfaces ":port"
	key                 string
	adminURL            string
	token               string
	systemPrompt        string
	maxInputTokens      int
	maxRequestBodyBytes int64
	web                 *webTools // nil: no server-side web tools
}

// maxRequestBodyBytes is the default local cap on request bodies. It matches the
// gRPC send-size allowance and exists to fail fast on oversized bodies instead of
// reading them whole into the decoder.
const maxRequestBodyBytes = 64 << 20

// listenAddr renders cfg.address and cfg.port as the http.Server bind address.
func listenAddr(cfg serveConfig) string {
	if cfg.address == "" {
		return fmt.Sprintf(":%d", cfg.port)
	}
	return fmt.Sprintf("%s:%d", cfg.address, cfg.port)
}

// newServeServer builds the hardened http.Server for one serve run.
func newServeServer(cfg serveConfig, comp completer) *http.Server {
	return &http.Server{
		Addr:              listenAddr(cfg),
		Handler:           newRequestLogger(newServeHandler(cfg, comp), os.Stdout),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute, // large anonymous chats take tens of seconds upstream
		WriteTimeout:      10 * time.Minute, // SSE responses hold the connection open until [DONE]
		IdleTimeout:       2 * time.Minute,
	}
}

// completer sends one Complete request to the upstream Rill AI service.
type completer interface {
	complete(ctx context.Context, req *CompleteRequest) (*CompleteResponse, error)
}

// runServe exposes the upstream model behind Rill Cloud's AI service as an
// OpenAI-compatible HTTP endpoint, usable as a drop-in "base_url" proxy from
// any client that speaks the OpenAI chat API.
func runServe(args []string, stdout, stderr io.Writer) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 8081, "Port to listen on")
	address := fs.String("address", "", "Interface to bind (example 127.0.0.1); empty keeps the default all-interfaces binding")
	key := fs.String("key", "", "Bearer key that clients must pass (env fallback RILL_PROXY_KEY; optional: the endpoint is open if unset)")
	adminURL := fs.String("url", envDefault("RILL_ADMIN_URL", "https://admin.rilldata.com"), "Base URL for the admin API")
	token := fs.String("token", os.Getenv("RILL_ADMIN_TOKEN"), "Bearer token for the admin API (optional: without one, calls are anonymous)")
	systemPrompt := fs.String("system-prompt", envDefault("RILL_SYSTEM_PROMPT", ""), "System message sent to the upstream before every conversation's own system messages (default: none sent)")
	maxInputTokens := fs.Int("max-input-tokens", envDefaultInt("RILL_AI_SERVE_MAX_INPUT_TOKENS", maxUpstreamInputTokens), "Reject chat requests whose estimated input exceeds this many tokens (0 disables the limit)")
	webToolsSpec := fs.String("web-tools", envDefault("RILL_WEB_TOOLS", ""), "Server-side web tools run for the model: all, none, or a comma list of search,fetch,crawl (default none)")
	searchEngine := fs.String("search-engine", envDefault("RILL_SEARCH_ENGINE", ""), "web_search backend: searxng or brave (default: whichever is configured; Brave reads BRAVE_SEARCH_API_KEY)")
	searxngURL := fs.String("searxng-url", envDefault("RILL_SEARXNG_URL", ""), "SearXNG base URL for web_search (its JSON format must be enabled)")
	mcpConfig := fs.String("mcp-config", envDefault("RILL_MCP_CONFIG", ""), "JSON file declaring MCP servers (mcpServers, stdio command or type http); their tools are run server-side")
	showVersion := fs.Bool("version", false, "Print the version and exit")
	_ = fs.Parse(args)

	if *showVersion {
		fmt.Fprintf(stdout, "proxy %s\n", version)
		return
	}

	cfg := serveConfig{
		port:           *port,
		address:        *address,
		key:            resolveKey(*key),
		adminURL:       *adminURL,
		token:          *token,
		systemPrompt:   *systemPrompt,
		maxInputTokens: *maxInputTokens,
	}

	mcpMgr, err := startMCPServers(*mcpConfig, stdout)
	if err != nil {
		fatalf(stderr, "%v", err)
	}
	// Stops the stdio servers with the proxy (<= 5s terminate grace each).
	defer mcpMgr.close()
	web, err := newWebTools(*webToolsSpec, *searchEngine, *searxngURL, os.Getenv("BRAVE_SEARCH_API_KEY"), stdout, mcpMgr)
	if err != nil {
		fatalf(stderr, "%v", err)
	}
	cfg.web = web
	if names := toolNames(mcpMgr.defs()); len(names) > 0 {
		fmt.Fprintf(stdout, "MCP tools run by the proxy: %s.\n", strings.Join(names, ", "))
	}
	// The startup "Web tools" line lists the proxy's own tools only; MCP
	// tools get their own line below.
	names := make([]string, 0)
	mcpSet := map[string]bool{}
	if mcpMgr != nil {
		for name := range mcpMgr.tools {
			mcpSet[name] = true
		}
	}
	for _, name := range toolNames(web.offered()) {
		if !mcpSet[name] {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		fmt.Fprintf(stdout, "Web tools run by the proxy: %s.\n", strings.Join(names, ", "))
	}

	if cfg.key == "" {
		fmt.Fprintf(stderr, "Warning: no --key configured: the /v1 endpoints are open to whoever can reach the port.\n")
	}
	if cfg.token == "" {
		fmt.Fprintf(stdout, "Serving an OpenAI-compatible proxy at http://127.0.0.1:%d, calling %s anonymously (no token).\n", cfg.port, cfg.adminURL)
	}

	conn, err := dialRill(cfg.adminURL, cfg.token)
	if err != nil {
		fatalf(stderr, "%v", err)
	}
	defer conn.Close()

	cfg.maxRequestBodyBytes = maxRequestBodyBytes
	srv := newServeServer(cfg, grpcCompleter{conn})

	// Serve until a termination signal, then drain connections before exiting.
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-done:
		fatalf(stderr, "server exited: %v", err)
	case <-stop:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(stderr, "shutdown: %v\n", err)
	}
}

// version is stamped at build time (go build -ldflags "-X main.version=...")
// and falls back to a plain unknown marker when built via plain `go build`.
var version = "unknown"

const maxUpstreamInputTokens = 272000

// newRequestLogger writes one access-log line per request (method, path, status,
// duration) to w, so a systemd/Kubernetes-style log collector can parse it.
func newRequestLogger(next http.Handler, w io.Writer) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if w == nil {
			next.ServeHTTP(rw, r)
			return
		}
		rec := statusRecorder{ResponseWriter: rw}
		next.ServeHTTP(&rec, r)
		fmt.Fprintf(w, "%s %s %d %v\n", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

// statusRecorder records the status code written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.wrote = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush lets SSE handlers flush through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// newServeHandler builds the endpoint's HTTP handler around the given upstream completer.
func newServeHandler(cfg serveConfig, comp completer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		if authFailed(w, r, cfg.key) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []map[string]any{{
				"id":       "rill-assistant",
				"object":   "model",
				"owned_by": "rill",
			}},
		})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		// The endpoint serves only the Rill assistant; the client's model field is accepted but ignored.
		if authFailed(w, r, cfg.key) {
			return
		}
		var req chatCompletionRequest
		if cfg.maxRequestBodyBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, cfg.maxRequestBodyBytes)
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				openAIErrorCode(w, http.StatusRequestEntityTooLarge, "",
					"request body too large: exceeds the limit of %d bytes", mbe.Limit)
				return
			}
			openAIError(w, http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		messages, err := rillMessages(req.Messages)
		if err != nil {
			openAIError(w, http.StatusBadRequest, "%s", err)
			return
		}
		// The server-level system prompt leads the conversation; the client's own
		// system messages follow it verbatim.
		if cfg.systemPrompt != "" {
			messages = append([]Message{{Role: "system", Blocks: []Block{{Text: cfg.systemPrompt}}}}, messages...)
		}
		creq := &CompleteRequest{Messages: messages, Tools: rillTools(req.Tools)}
		// Every upstream request is checked; a limit of zero disables the check,
		// and the upstream then enforces its own configured limit.
		checkBudget := func(messages []Message, tools []Tool) error {
			if estimate := estimateInputTokens(messages, tools); cfg.maxInputTokens > 0 && estimate > cfg.maxInputTokens {
				return contextLengthError{limit: cfg.maxInputTokens, estimate: estimate}
			}
			return nil
		}
		run := func(ctx context.Context) (*CompleteResponse, error) {
			if cfg.web == nil {
				if err := checkBudget(creq.Messages, creq.Tools); err != nil {
					return nil, err
				}
				return comp.complete(ctx, creq)
			}
			return cfg.web.complete(ctx, comp, creq, checkBudget)
		}
		if req.Stream && cfg.web != nil {
			streamCompletion(w, r.Context(), req.Model, run)
			return
		}
		resp, err := run(r.Context())
		if err != nil {
			writeCompletionError(w, err)
			return
		}
		if req.Stream {
			// Without web tools the stream is written once the completion is
			// known, as before --web-tools existed.
			startSSE(w, req.Model).finish(resp)
			return
		}
		writeJSON(w, http.StatusOK, completionFromUpstream(req.Model, resp))
	})
	return mux
}

// contextLengthError reports a request whose estimated input exceeds --max-input-tokens.
type contextLengthError struct{ limit, estimate int }

func (e contextLengthError) Error() string {
	return fmt.Sprintf("estimated input tokens exceed the configured limit of %d tokens; your messages resulted in about %d tokens. Please reduce the length of the messages.", e.limit, e.estimate)
}

// writeCompletionError writes a failed completion in the OpenAI error envelope.
func writeCompletionError(w http.ResponseWriter, err error) {
	var cle contextLengthError
	if errors.As(err, &cle) {
		openAIErrorCode(w, http.StatusBadRequest, "context_length_exceeded", "%s", cle.Error())
		return
	}
	openAIError(w, http.StatusBadGateway, "upstream completion failed: %v", err)
}

// sseKeepAlive is the longest a streamed request goes without a byte; tests shorten it.
var sseKeepAlive = 15 * time.Second

// streamCompletion answers a stream:true request when web tools are on. A
// completion that settles within sseKeepAlive is answered as without them
// (errors as HTTP statuses); a longer one (a server-side tool loop) gets its
// stream opened early and SSE keep-alive comments until it settles, so
// idle-timeouts do not cut it.
func streamCompletion(w http.ResponseWriter, ctx context.Context, model string, run func(context.Context) (*CompleteResponse, error)) {
	type result struct {
		resp *CompleteResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := run(ctx)
		done <- result{resp, err}
	}()
	tick := time.NewTicker(sseKeepAlive)
	defer tick.Stop()
	var sse *sseWriter
	for {
		select {
		case res := <-done:
			switch {
			case sse == nil && res.err != nil:
				writeCompletionError(w, res.err)
			case res.err != nil:
				var cle contextLengthError
				if errors.As(res.err, &cle) {
					sse.fail(cle.Error())
				} else {
					sse.fail(fmt.Sprintf("upstream completion failed: %v", res.err))
				}
			default:
				if sse == nil {
					sse = startSSE(w, model)
				}
				sse.finish(res.resp)
			}
			return
		case <-tick.C:
			if sse == nil {
				sse = startSSE(w, model)
			}
			sse.keepAlive()
		}
	}
}

// dialRill opens a gRPC connection to Rill's admin service, with an optional bearer token
// (which this tool deliberately leaves empty by default: the endpoint is served anonymously).
func dialRill(adminURL, token string) (*grpc.ClientConn, error) {
	uri, err := url.Parse(adminURL)
	if err != nil {
		return nil, err
	}
	if uri.Port() == "" {
		uri.Host = uri.Host + ":443"
	}
	opts := []grpc.DialOption{
		grpc.WithUserAgent("snoopy-proxy"),
		grpc.WithDefaultServiceConfig(`{"methodConfig": [{
			"name": [{}],
			"retryPolicy": {
				"maxAttempts": 5,
				"initialBackoff": ".1s",
				"maxBackoff": "5s",
				"backoffMultiplier": 5,
				"retryableStatusCodes": ["UNAVAILABLE"]
			}
		}]}`),
		grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, "")),
		// Allow large sends: the stock 4MB default is a client-side limit that would
		// reject locally before the server's own receive limit is ever consulted.
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64 << 20)),
	}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(bearerAuth{token: token}))
	}
	return grpc.NewClient(uri.Host, opts...)
}

type bearerAuth struct{ token string }

func (t bearerAuth) GetRequestMetadata(ctx context.Context, in ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + t.token}, nil
}

func (bearerAuth) RequireTransportSecurity() bool { return true }

// grpcCompleter adapts a *grpc.ClientConn to the completer interface.
type grpcCompleter struct{ conn *grpc.ClientConn }

// complete sends one Complete request over the raw codec of wire.go.
func (c grpcCompleter) complete(ctx context.Context, req *CompleteRequest) (*CompleteResponse, error) {
	resp := &CompleteResponse{}
	if err := c.conn.Invoke(ctx, completeMethod, req, resp, grpc.ForceCodec(rillCodec{})); err != nil {
		return nil, err
	}
	return resp, nil
}

// estimateInputTokens estimates the token count of a translated request by dividing its total character count by four.
// It is a heuristic, not provider accounting: it errs conservatively in either direction, and the upstream may still reject a request under the limit.
func estimateInputTokens(messages []Message, tools []Tool) int {
	var chars int
	for _, m := range messages {
		chars += len(m.Data)
		for _, blk := range m.Blocks {
			switch {
			case blk.ToolCall != nil:
				chars += len(blk.ToolCall.ID) + len(blk.ToolCall.Name) + len(blk.ToolCall.Input)
			case blk.ToolResult != nil:
				chars += len(blk.ToolResult.ID) + len(blk.ToolResult.Content)
			default:
				chars += len(blk.Text)
			}
		}
	}
	for _, t := range tools {
		chars += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	return chars / 4
}

// authFailed returns true if a required bearer key was not provided. The key is
// compared in near-constant time so response latency does not leak partial key bytes.
func authFailed(w http.ResponseWriter, r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	provided := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(provided), []byte("Bearer "+key)) == 1 {
		return false
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	openAIError(w, http.StatusUnauthorized, "missing or invalid bearer key")
	return true
}

// resolveKey returns the proxy bearer key: the --key flag wins; the env fallback
// RILL_PROXY_KEY exists so secrets never need to land on a visible command line.
func resolveKey(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("RILL_PROXY_KEY")
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDefaultInt returns the named environment variable's value, or the given default if it is unset, empty, or not a valid number.
func envDefaultInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// fatalf prints an error to stderr and exits.
func fatalf(stderr io.Writer, format string, args ...any) {
	fmt.Fprintf(stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
