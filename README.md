# @s3p1r0o/snoopy

A unified, self-sufficient agent harness: [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (`dsh`) driven through an embedded, OpenAI-compatible proxy written in Go (`proxy/`) — which fronts the upstream LLM behind Rill Cloud's AI service (`admin.rilldata.com`, gRPC `rill.admin.v1.AIService/Complete`).

The repo's model is a single assistant, `rill-assistant`; the client's `model` field is ignored.

```
├── proxy/            Go module: the OpenAI-compatible HTTP proxy
├── bin/harness.ts    dev launcher: proxy + dsh with the repo's .dsh profile
├── bin/launcher.js   published standalone launcher (npm package)
├── lib/node-select.js shared official-vs-distro Node selection
├── profile/          bundled dsh profiles (web + headless) for the standalone launcher
└── .dsh/profiles/    repo-local dsh profiles (git-tracked config)
```

## Quick start (dev)

```sh
npm install          # dsh + tooling
just harness         # proxy on 127.0.0.1:8081 + dsh Web UI (default model: rill-assistant)
just harness headless "Reply with the single word: pong"
```

One Ctrl-C stops both; the dsh↔proxy loop is loopback-only with a per-launch ephemeral bearer key.

## Quick start (standalone — once published)

```sh
npm install @s3p1r0o/snoopy
npx snoopy web
npx snoopy headless "Reply with the single word: pong"
```

The postinstall hook downloads the proxy binary for your platform (linux/darwin, x64/arm64) from the proxy repository's GitHub Releases; `npx snoopy download` (re)fetches it. The dsh home lands under `$XDG_DATA_HOME/snoopy/dsh` (override `DSH_HOME`) with bundled profiles.

## The proxy alone (pure Go)

```sh
cd proxy && go build -o ../build/proxy .   # requires a Go toolchain (>= 1.26)
cd proxy && go test ./...                   # handler + pinned wire-format tests
```

Point any OpenAI chat client at it: `base_url = http://127.0.0.1:8081/v1`.

```sh
./build/proxy --port 8081
curl http://127.0.0.1:8081/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"rill-assistant","messages":[{"role":"user","content":"Reply with the single word: pong"}]}'
```

## Proxy configuration

Flags take precedence over environment variables where a fallback exists. Run `./build/proxy --help` for the authoritative list.

| Flag                 | Environment fallback             | Default                     | Description |
| -------------------- | -------------------------------- | --------------------------- | ----------- |
| `--port`             | —                                | `8081`                      | HTTP listen port. |
| `--address`          | —                                | *(all interfaces)*          | Interface to bind, e.g. `127.0.0.1`. The harness launcher always binds loopback. |
| `--key`              | `RILL_PROXY_KEY`                 | *(unset)*                   | Bearer key clients must pass to the proxy. Unset: the endpoint is open. |
| `--url`              | `RILL_ADMIN_URL`                 | `https://admin.rilldata.com` | Base URL for the Rill admin API. |
| `--token`            | `RILL_ADMIN_TOKEN`               | *(unset)*                   | Bearer token sent upstream to Rill. Unset: calls to Rill are anonymous. |
| `--max-input-tokens` | `RILL_AI_SERVE_MAX_INPUT_TOKENS` | `272000`                    | Reject chat requests whose estimated input exceeds this many tokens. `0` disables the local check. |
| `--system-prompt`    | `RILL_SYSTEM_PROMPT`             | *(empty — nothing sent)*    | System message sent to the upstream **before** each conversation's own system messages. Empty default: nothing injected. |
| `--version`          | —                                | —                           | Print the version and exit. |

## Endpoints

- `POST /v1/chat/completions` — OpenAI chat completions, non-streaming and streaming shape; translates `messages` (system/user/assistant/tool), `tools[].function`, and `tool_calls` to the Rill contract; relays upstream `usage`.
- `GET /v1/models` — a single fixed model, `rill-assistant`; protected by `--key` when set.
- `GET /healthz` — liveness probe, always open.

## Tool calling (web search example)

The assistant does not run web search on its own — it follows the standard OpenAI tool-calling loop: it asks for a tool, **you** execute it, and you send the result back. The proxy translates both round trips (`assistant` messages with `tool_calls` arrive as Rill tool-call blocks; `role: "tool"` results arrive as Rill tool-result blocks).

### Round 1 — offer the tool, receive a tool call

```sh
cat > q1.json <<'EOF'
{"model":"rill-assistant","messages":[{"role":"user","content":"What is the current EUR to USD exchange rate? Use the provided web search tool to get an up-to-date answer."}],
 "tools":[{"type":"function","function":{"name":"web_search","description":"Web search: returns the most relevant results for a query.","parameters":{"type":"object","properties":{"query":{"type":"string","required":["query"]}}}}}]}
EOF
curl -s http://127.0.0.1:8081/v1/chat/completions \
  -H 'Content-Type: application/json' -d @q1.json \
| jq '.choices[0]'
```

The reply comes back with `finish_reason: "tool_calls"` and the model's tool call:

```json
{
  "finish_reason": "tool_calls",
  "message": {
    "role": "assistant",
    "tool_calls": [{
      "id": "call_uHXmBaRjrNnPY4oNbdpy5O5E",
      "type": "function",
      "function": {
        "name": "web_search",
        "arguments": "{\"query\":\"current EUR to USD exchange rate\"}"
      }
    }]
  }
}
```

### Round 2 — run the tool, send the result back

Run the search yourself (your code, a script, a real search API), then append the tool result under the tool-call ID:

```sh
cat > q2.json <<'EOF'
{"model":"rill-assistant","messages":[
 {"role":"user","content":"What is the current EUR to USD exchange rate? Use the provided web search tool to get an up-to-date answer."},
 {"role":"assistant","tool_calls":[{"id":"call_uHXmBaRjrNnPY4oNbdpy5O5E","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"current EUR to USD exchange rate\"}"}}]},
 {"role":"tool","tool_call_id":"call_uHXmBaRjrNnPY4oNbdpy5O5E",
  "content":"EUR/USD as of October 9, 2026: ECB official rate 1 EUR = 1.1206 USD; XE.com mid-market 1 EUR = 1.1194 USD."}]}
EOF
curl -s http://127.0.0.1:8081/v1/chat/completions \
  -H 'Content-Type: application/json' -d @q2.json \
| jq -c '.choices[0].message.content, .choices[0].finish_reason, .usage'
```

The second call returns the final answer, as of the last test run:

> As of **October 9, 2026**, the **EUR → USD** exchange rate is: **ECB official rate 1 EUR = 1.1206 USD** (XE.com mid-market: ≈ 1.1194 USD).

with `finish_reason: "stop"` and the upstream usage relayed.

## Error handling

All errors use the OpenAI error envelope. Per the provider convention, 4xx responses carry `type: "invalid_request_error"` and 5xx carry `type: "api_error"`; the local input-size guard also sets `code: "context_length_exceeded"`.

## Limits

- **Input-size guard.** Requests whose estimated input exceeds the limit are rejected locally, without an upstream round-trip. The default limit is 272,000 tokens — the upstream provider's configured limit, disclosed by a relayed provider error on 2026-10-09. The estimate is characters/4: a heuristic, not exact tokenization.
- **Body cap.** Request bodies above 64 MB are rejected locally with a 413 envelope.
- **Contract.** The Rill contract (`rill.ai.v1.ContentBlock`) carries text and tool calls/results only — image content is rejected with a clean OpenAI error envelope.
- **Streaming.** The upstream contract is unary: `stream: true` receives the full response at once and relays it as the standard OpenAI SSE chunk sequence (role delta → content/tool-call deltas → finish chunk → `[DONE]`). Token-by-token streaming is not possible.

## Deployment

### Cross-compiled releases

Release tags are `v<version>` (e.g. `v0.1.0`); the version stamped everywhere drops the `v` (`0.1.0`).

```sh
git tag v0.1.0 && git push origin v0.1.0   # the release workflow publishes the GitHub Release
just release   # local equivalent: build/release/<version>/proxy-<version>-{linux,darwin}-{amd64,arm64}.tar.gz + SHA256SUMS
```

Each tarball carries a single static, version-stamped `proxy` binary (CGO off, `-trimpath`); verify with `sha256sum -c SHA256SUMS`. These assets are what the npm package downloads at install time, from `releases/download/v<version>/`. CI cross-compiles the same four targets on every push.

### npm package release

Once the tag's GitHub Release exists, from a clean checkout of that tag:

```sh
just npm-release   # dist/@s3p1r0o-snoopy-<version>.tgz + SHA256SUMS (refuses a dirty or untagged HEAD)
npm publish dist/*.tgz   # with your npm identity; publishConfig makes the scoped package public
```

The tag version becomes the npm package version, which tells the launcher which release assets to fetch (override with `SNOOPY_PROXY_VERSION`).

### Docker

One Dockerfile, two targets:

```sh
# Full standalone harness (default target): embedded proxy + dsh + supervisor.
docker build -t snoopy --build-arg VERSION=$(git describe --always) .
docker run --rm -d # --network host snoopy          # UI: http://127.0.0.1:3080/?token=… (from docker logs)
docker run --rm -p 8081:8081 snoopy headless "Reply with the single word: pong"
docker run --rm -p 8081:8081 snoopy tui           # terminal UI over an attached tty

# Proxy alone (as before):
docker build --target proxy -t snoopy:proxy . 
docker run --rm -p 8081:8081 snoopy:proxy --key my-key

# Multi-arch (linux/amd64 + linux/arm64) through buildx; push to a registry:
just docker-buildx <registry>/snoopy linux/amd64,linux/arm64 --push   # tags <registry>/snoopy:<version>
```

Notes:
- dsh **refuses** non-loopback `--host` by upstream design (an agent UI is a remote-code surface): reach the Web UI with `--network host` (loopback on the host) or your own SSH/k8s tunnel — not by exposing the container port.
- The runtime user is `node`; dsh state lives on the `/data` volume (`DSH_HOME=/data/dsh`). Mount it to keep sessions across restarts.
- `RILL_ADMIN_TOKEN` (upstream auth), `RILL_SYSTEM_PROMPT`, etc. pass as `-e` flags; the dsh↔proxy loop is loopback-only with an ephemeral bearer key unless you export `RILL_PROXY_KEY`.
- Both runtimes get the CA bundle copied in at build time (gRPC-TLS to Rill otherwise fails with `x509: unknown authority`).

### systemd

```ini
[Unit]
Description=OpenAI-compatible proxy to Rill Cloud's AI service
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/proxy --port 8081
EnvironmentFile=/etc/snoopy-proxy/env
Restart=on-failure
RestartSec=5
DynamicUser=yes
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

With `/etc/snoopy-proxy/env` holding environment overrides (e.g. `RILL_ADMIN_URL=...`, `RILL_ADMIN_TOKEN=...`, `RILL_AI_SERVE_MAX_INPUT_TOKENS=...`); flags stay in `ExecStart`.

## Hardening notes

- The proxy key (`--key`) protects the proxy, not Rill: calls to Rill remain anonymous unless `--token` is set.
- The harness boot adds an ephemeral key + loopback bind (nothing secret on the command line, nothing beyond localhost).
- Access logs (`METHOD PATH STATUS DURATION`) go to stdout; errors go to stderr.
- Timeouts: 10 s header, 10 min read/write (sized for long upstream completions and SSE), 2 min idle.

## DeepSeek Harness (dsh) — dev mode details

- One process, one Ctrl-C: `bin/harness.ts` builds the proxy to `build/proxy` (unless `DSH_PROXY_BIN` names one), waits for `/healthz`, then boots dsh with `DSH_HOME=$PWD/.dsh`; signals reach both.
- The repo profiles `.dsh/profiles/{web,headless}/cordis.patch.yml` declare the custom provider `proxy` (OpenAI Chat Completions, `http://127.0.0.1:8081/v1`, model `rill-assistant`, 272k context) and the `agent-default-model` plugin points new sessions at it — no picker click needed.
- Model routing edits go there; changes apply on the next request, no restart.
- **Node compatibility.** dsh's native loader only recognizes official nodejs.org builds; Nix/NixOS-compiled Node fails its probe at boot (see [deepseek-harness#690](https://github.com/deepseek-ai/deepseek-harness/discussions/690)). Both launchers prefer `DSH_NODE`, then fnm/nvm installs, then PATH — warning which one they use. On NixOS: `fnm install 24 && fnm use 24`.
- `node --check bin/launcher.js` + `npm test` run in CI as a Node-side syntax/test gate.

## Development

Common tasks are in the `justfile` (`just --list` for the menu): `just build`, `just lint`, `just test` (Go), `just node-test`, `just check`, `just run [flags]`, `just docker-build`, `just docker-buildx`, `just docker-run`, `just harness [mode]`, `just release`, `just npm-release`.

- `cd proxy && go test ./...` — handler tests in `proxy/serve_test.go`, wire-format tests pinned against generated-stub bytes in `proxy/wire_test.go`.
- `staticcheck ./...` should stay clean (CI enforces build, vet, staticcheck, gofmt, race tests; plus Node-side syntax checks and `npm test`).
- `npm test` — unit tests for the shared Node-selection helpers.
- `proxy/wire.go` pins the Rill contract: don't change encodings without re-verifying against `proxy/contract/*.proto`.