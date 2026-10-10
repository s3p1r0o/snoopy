# @s3p1r0o/snoopy

[![npm](https://img.shields.io/npm/v/@s3p1r0o/snoopy)](https://www.npmjs.com/package/@s3p1r0o/snoopy)
[![ci](https://github.com/s3p1r0o/snoopy/actions/workflows/ci.yml/badge.svg)](https://github.com/s3p1r0o/snoopy/actions/workflows/ci.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue)](https://github.com/s3p1r0o/snoopy/blob/main/LICENSE)

Run [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (`dsh`) against Rill Cloud's AI assistant in one command. Snoopy starts an embedded OpenAI-compatible proxy written in Go — loopback-only, with a per-launch ephemeral key — points dsh at it, and stops both on Ctrl-C.

The proxy fronts the upstream LLM behind Rill Cloud's AI service (`admin.rilldata.com`, gRPC `rill.admin.v1.AIService/Complete`) and exposes it as a single model, `rill-assistant`; the client's `model` field is ignored. It also runs on its own, for any OpenAI chat client (see [The proxy alone](#the-proxy-alone-pure-go)).

## Install

Requirements: Linux or macOS on x64 or arm64, and Node ≥ 22.13 from an official nodejs.org build (nvm, fnm or the installer). dsh's native loader rejects distro and Nix builds of Node.

```sh
npm install -g @s3p1r0o/snoopy
```

The install downloads the proxy binary for your platform from this repository's GitHub Release matching the package version, checked against its `SHA256SUMS`. If that step could not run (offline, `--ignore-scripts`), the first launch retries; `snoopy download` fetches it explicitly.

## Usage

```sh
snoopy web                                         # dsh Web UI
snoopy headless "Reply with the single word: pong" # one-shot task: prints the answer and exits
snoopy acp                                         # Agent Client Protocol server on stdio, for orchestrators
```

The first argument names a dsh profile; `web`, `headless` and `acp` are the snoopy routes through the proxy. dsh's other shipped profile (`sdk`) boots without that routing, and this dsh release has no terminal UI. Stdout belongs to dsh (the headless answer, the ACP stream); snoopy's own messages and the proxy's errors go to stderr. The proxy's access logs are off unless `SNOOPY_PROXY_LOG=1`. Further arguments go to dsh unchanged (`snoopy web --help` lists the web app's flags). New sessions default to `rill-assistant` through the proxy; no model picker step. Profiles and sessions live under `$XDG_DATA_HOME/snoopy/dsh` (by default `~/.local/share/snoopy/dsh`).

Snoopy's routing ships inside the package (`profile/<name>.patch.yml`) and is layered onto dsh with `--patch` on every launch; it is never copied into your profiles. Each profile's `cordis.patch.yml` is yours, and the agent's when it manages its own plugins. Snoopy's routing wins over it for the fields it sets (provider, base URL, default model).

### Driving snoopy agents (ACP)

`snoopy acp` serves the standard [Agent Client Protocol](https://agentclientprotocol.com) v1 over JSON-RPC on stdin/stdout: an orchestrator spawns one process per agent and calls `initialize`, `session/new` (absolute `cwd`, MCP servers), `session/prompt`, then `session/close`; sessions persist under the dsh home and can be listed and resumed. Every session uses `rill-assistant` through the proxy (the acp bundle's own default, `deepseek-official`/`deepseek-v4-flash`, is overridden). The server can ask the client for permission (`session/request_permission`), so the orchestrator must answer those requests. Each process starts its own proxy on a free port.

### Self-improvement

Snoopy agents can improve themselves; `SNOOPY_SELF_IMPROVE=0` turns the plugin part off.

### Agent instructions

Snoopy gives its agents two default texts, in `lib/defaults/`. The system prompt states facts about the environment: `rill-assistant` is a route name, not a role; the proxy's server-side tools (`web_*`, MCP `server__tool`) do not keep their results in the conversation, and must not be mixed with the harness's own tools in one step. The launchers hand it to the proxy as `RILL_SYSTEM_PROMPT`; an explicit `RILL_SYSTEM_PROMPT` replaces it and `SNOOPY_SYSTEM_PROMPT=0` turns it off. The global `AGENTS.md` holds working rules, not tied to any technology (understand before changing, small changes, verify before claiming done, ask before anything destructive or external). It is written to `$DSH_HOME/AGENTS.md`, which dsh loads before each project's own `AGENTS.md`/`CLAUDE.md`, and snoopy rewrites it at every launch so upgrades bring the new rules: put personal rules in your projects' instruction files. A global `AGENTS.md` snoopy did not write is kept as `AGENTS.md.bak-<timestamp>` once; `SNOOPY_AGENTS_MD=0` leaves the file alone. In the `web` profile, the routing patch also turns off two prompt sections of dsh's Web app that only fit dsh's own monorepo (they called the snoopy install "the DeepSeek Harness implementation checkout" and described dsh's dev watcher).

- **Skills.** An agent can write `.agents/skills/<name>/SKILL.md` (YAML frontmatter `name` and `description`, then instructions) in its workspace; later sessions discover it without a restart. dsh also scans `<dsh home>/skills` and `.dsh/skills`.
- **Plugins and bundles.** The `plugin_manager` tool lets the agent list, enable and disable its profile's plugins, and install or remove bundles (with the pnpm bundled in snoopy). Changes land in the profile's own `cordis.patch.yml` and `package.json` and apply on the next launch (the Web app reloads them live).
- **Creating plugins: Creator mode.** To have an agent write a new plugin, use dsh's Creator mode (the `cordis` preset): it mounts the harness's own plugin-development skills, with templates and API references. Pick it in the Web app's mode selector (or make it the default for new tasks there), or start with `SNOOPY_AGENT_PRESET=creator`. The `standard` mode can install and toggle plugins but has no authoring guide.
- **Approval.** Every `plugin_manager` action needs approval: in `web` the user approves in the UI; in `acp` the orchestrator receives `session/request_permission`; `headless` refuses unless `DSH_PERMISSION_MODE=danger-full-access`. Installed bundles run as host code, outside the workspace sandbox.
- **Fleets.** Agents sharing a dsh home share its profiles: one agent's plugin change reaches every agent on that home. Give each agent its own `DSH_HOME` to keep them apart. Outside `web`, a change takes effect when the orchestrator restarts the agent.

### Skills

Snoopy bundles 23 skills for spec-driven development (listed with their sources and licences in [`skills/NOTICE.md`](skills/NOTICE.md)): the [superpowers](https://github.com/obra/superpowers) chain (brainstorming, writing-plans, executing-plans or subagent-driven-development, test-driven-development, systematic-debugging, verification-before-completion, code review, finishing a branch), five complements from [mattpocock/skills](https://github.com/mattpocock/skills) (grilling, domain-modeling, codebase-design, research, prototype), and the [OpenSpec](https://github.com/Fission-AI/OpenSpec) workflow (openspec-explore, openspec-propose, openspec-apply-change, …) with its `openspec` CLI. Agents load a skill when the task matches it; type `/<name>` (for example `/brainstorming` or `/openspec-propose`) to invoke one yourself. The global `AGENTS.md` tells agents to use OpenSpec in projects with an `openspec/` directory and the brainstorming chain elsewhere.

The launchers hand dsh the skills through `DSH_BUNDLED_SKILL_DIR`, the lowest-priority skill root: a skill with the same name in a project's `.agents/skills/` or `.dsh/skills/`, or in `~/.agents/skills/`, replaces the bundled one. The shim runs OpenSpec with its usage telemetry and npm update check off (`OPENSPEC_TELEMETRY=1` turns telemetry back on). `SNOOPY_SKILLS=0` turns the bundled skills and the `openspec` shim off; a `DSH_BUNDLED_SKILL_DIR` you set yourself wins. Skills appear in the `standard`, `ptc` and `cordis` presets, not in `minimal`.

### Web tools

The proxy itself gives agents three web tools, so no dsh plugin is needed: `web_fetch` reads one page and returns its main content as Markdown (Readability extraction, charset decoding), `web_crawl` follows a site's links on the same host (breadth first, up to 20 pages), and `web_search` queries a search engine you configure. The proxy runs them server-side, so the dsh UI shows the final answer, not the tool calls. They replace dsh's own `web_search` and `web_fetch` (dsh's search needs a DeepSeek key).

- **Search engine.** The Docker image bundles SearXNG, a metasearch engine started on loopback inside the container, which queries many engines at once (see [Docker](#docker)). Elsewhere, without configuration, snoopy searches DuckDuckGo's keyless HTML page: fine for low volume, spaced by 2 s, and DuckDuckGo may throttle bursts (the agent is then told to use `web_fetch`). For more, a self-hosted SearXNG (free, no key; its JSON format must be enabled) or the Brave Search API take over when configured.

  ```sh
  mkdir -p ~/.config/searxng
  printf 'use_default_settings: true\nserver:\n  secret_key: "change-me"\n  limiter: false\nsearch:\n  formats: [html, json]\n' > ~/.config/searxng/settings.yml
  docker run -d --name searxng -p 127.0.0.1:8888:8080 -v ~/.config/searxng:/etc/searxng searxng/searxng
  RILL_SEARXNG_URL=http://127.0.0.1:8888 just harness
  ```

  or `BRAVE_SEARCH_API_KEY=… just harness`.
- **Safety.** Pages are fetched from public addresses only (checked on the address actually dialed), with at most 5 redirects, 5 MiB and 20 s per page; results reach the model marked as untrusted content. Requests present themselves as desktop Chrome (User-Agent and client hints) and the crawl does not consult robots.txt, as a person's browser would not; it stays polite with a 250 ms delay and 20 pages at most. Paywalls and bot walls that check more than headers (such as TLS fingerprints) still refuse. JavaScript is not run: a page that renders its text with JavaScript comes back empty, and the tool says so.
- **Long tool loops.** A streamed request whose tool loop runs past 15 s gets its stream opened early with `: keep-alive` SSE comments; a failure after that arrives as an in-band `error` event before `[DONE]`. Without web tools, streaming is unchanged.
- `SNOOPY_WEB_TOOLS=0` turns them off (dsh's own web tools come back); `SNOOPY_PROXY_LOG=1` shows one `TOOL` line per run.

### MCP servers

The launchers declare one knowledge base to the proxy by default: Wikipedia, on a hosted instance of the open [wikipedia-mcp-server](https://github.com/cyanheads/wikipedia-mcp-server) over Streamable HTTP. On first boot the launcher writes `~/.config/snoopy/mcp.json` (never overwriting your own edits) and points the proxy at it; the agents get `wiki__wikipedia_search_articles`, `wiki__wikipedia_get_summary` and their siblings in the server-side tool loop. `SNOOPY_MCP=0` turns the default off, and `RILL_MCP_CONFIG` pointing at your own file replaces it.

`--mcp-config PATH` (env `RILL_MCP_CONFIG`) points to a JSON file declaring MCP servers (the proxy is the client), in the usual `mcpServers` shape:

```json
{
  "mcpServers": {
    "git": { "command": "uvx", "args": ["mcp-server-git"] },
    "remote": { "type": "http", "url": "https://mcp.example.com/mcp", "headers": { "Authorization": "Bearer ..." }, "enabled": false }
  }
}
```

stdio servers (`command`/`args`/`env`) are processes the proxy starts and keeps; http servers are Streamable HTTP endpoints authenticated with static headers. Every tool an enabled server announces is offered to the model as `server__tool`, next to the web tools in the same server-side tool loop; `enabled` (default true) turns a server off. A server that fails to start or to initialize within 10 s is logged and skipped, the proxy runs on; a server that dies mid-session makes its calls fail as tool errors the model can see. Results are treated as untrusted data (a stdio server's stderr is logged with its name); the config file's secrets are never logged, and the `TOOL` access log records the tool, its target and the duration only. Results are capped like fetched pages.

To see at runtime what the proxy is serving, `GET /debug/tools` (bearer key, like `/v1/models`) lists every offered tool with its source (`web` or `mcp`) and each declared MCP server's connection state:

```sh
curl -s -H "Authorization: Bearer $SNOOPY_PROXY_KEY" http://127.0.0.1:8081/debug/tools
```

```json
{
  "tools": [{ "name": "sh__echo", "description": "...", "source": "mcp", "input_schema": { "type": "object" } }],
  "mcpServers": [{ "name": "sh", "kind": "stdio", "connected": true, "tools": ["sh__echo"] }]
}
```

### Upgrading

`npm install -g @s3p1r0o/snoopy@latest` replaces the launcher and fetches the proxy binary of the new version (the old one goes with the old package). The routing comes from the installed package, so it updates along. Anything else in the dsh home is upgraded by versioned migrations, applied once on the first launch and recorded in `snoopy-migrations.json`. A migration changes a file only when it recognizes what snoopy wrote there, and leaves a `.pre-<migration>` backup when it keeps your own entries around it.

Upgrading from 0.1.x: the routing copies 0.1.x wrote into `profiles/{web,headless}/cordis.patch.yml` are removed; your own entries in those files stay (backup `cordis.patch.yml.pre-routing-overlay`). A copy you edited is left as is.

npm 11 warns that the package's `postinstall` (the proxy download) is not covered by `allowScripts`. If your npm blocks it, the first launch downloads the binary instead, or allow it: `npm install -g --allow-scripts=@s3p1r0o/snoopy @s3p1r0o/snoopy`.

### Environment

| Variable | Effect |
| -------- | ------ |
| `RILL_ADMIN_TOKEN` | Bearer token sent to Rill. Unset: calls to Rill are anonymous. |
| `RILL_SYSTEM_PROMPT`, `RILL_ADMIN_URL`, … | Passed through to the proxy (see [Proxy configuration](#proxy-configuration)). |
| `SNOOPY_AGENT_PRESET` | Default agent preset for new `web` sessions, e.g. `creator` (Creator mode, `cordis`), `standard`, `ptc`. Unset: dsh's default and the one remembered by the Web UI. |
| `SNOOPY_SELF_IMPROVE` | `0` (or `false`, `off`, `no`) hides the `plugin_manager` tool from agents; on by default. |
| `DSH_PERMISSION_MODE` | dsh sandbox mode; `danger-full-access` lets `headless` agents run `plugin_manager` without approval. |
| `DSH_HOME` | dsh home directory (default: `$XDG_DATA_HOME/snoopy/dsh`). |
| `SNOOPY_WEB_TOOLS` | `0` (or `false`, `off`, `no`) turns the proxy's web tools off; on by default. |
| `SNOOPY_MCP` | `0` (or `false`, `off`, `no`) drops the default MCP knowledge base (Wikipedia); `RILL_MCP_CONFIG` replaces it. |
| `SNOOPY_SYSTEM_PROMPT` | `0` (or `false`, `off`, `no`) sends no default system prompt; an explicit `RILL_SYSTEM_PROMPT` replaces it. |
| `SNOOPY_AGENTS_MD` | `0` (or `false`, `off`, `no`) leaves `$DSH_HOME/AGENTS.md` alone instead of rewriting it with snoopy's working rules. |
| `SNOOPY_SKILLS` | `0` (or `false`, `off`, `no`) drops the bundled skills and the `openspec` CLI shim; a `DSH_BUNDLED_SKILL_DIR` you set wins. |
| `RILL_SEARXNG_URL` | SearXNG base URL for `web_search`, e.g. `http://127.0.0.1:8888`. |
| `BRAVE_SEARCH_API_KEY` | Brave Search API key for `web_search` (used when no SearXNG URL is set). |
| `SNOOPY_PROXY_LOG` | `1` shows the proxy's startup line and access logs (one line per request) on stderr; off by default. Proxy errors always show. |
| `SNOOPY_PORT` | Fixed proxy port (default: first free port ≥ 8307). |
| `SNOOPY_PROXY_KEY` | Fixed proxy bearer key instead of the ephemeral one (`RILL_PROXY_KEY` wins if both are set). |
| `SNOOPY_PROXY_BIN` | Use this proxy binary; skips the download. |
| `SNOOPY_PROXY_VERSION` | Proxy release to download (default: the package version). |
| `DSH_NODE` | Node binary to run dsh with (default: the best official build found). |

## Quick start (development)

```
├── proxy/            Go module: the OpenAI-compatible HTTP proxy
├── bin/harness.ts    dev launcher: proxy + dsh with the repo's .dsh profile
├── bin/launcher.js   published standalone launcher (npm package)
├── lib/node-select.js shared official-vs-distro Node selection
├── lib/profiles.js   proxy-routed profiles and the --patch routing overlay
├── lib/migrations.js versioned dsh-home migrations (append-only)
├── profile/          routing patches (web, headless, acp) + agentic.patch.yml + self-improve.patch.yml, layered with --patch
├── AGENTS.md         workspace instructions dsh agents load (where bundles go)
└── .dsh/             dev dsh home (DSH_HOME): local state, git-ignored
```

```sh
npm install          # dsh + tooling
just harness         # proxy on 127.0.0.1:8081 + dsh Web UI (default model: rill-assistant)
just harness headless "Reply with the single word: pong"
just preset=creator harness   # new web tasks start in Creator mode (writing plugins)
```

One Ctrl-C stops both; the dsh↔proxy loop is loopback-only with a per-launch ephemeral bearer key.

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
| `--web-tools`        | `RILL_WEB_TOOLS`                 | *(none)*                    | Server-side web tools the proxy runs for the model: `all`, `none`, or a comma list of `search,fetch,crawl`. The proxy owns those tool names: client tools of the same name are replaced. |
| `--search-engine`    | `RILL_SEARCH_ENGINE`             | *(auto)*                    | `web_search` backend: `searxng`, `brave` or `duckduckgo` (keyless HTML page, never chosen implicitly by the proxy); by default whichever of SearXNG and Brave is configured. The snoopy launchers pass `duckduckgo` when neither is. |
| `--searxng-url`      | `RILL_SEARXNG_URL`               | *(unset)*                   | SearXNG base URL (JSON format enabled). The Brave key is read from `BRAVE_SEARCH_API_KEY` only. |
| `--mcp-config`       | `RILL_MCP_CONFIG`                | *(unset)*                   | JSON file declaring MCP servers (`mcpServers` shape, stdio `command` or `type: "http"` + `url`); their tools run server-side as `server__tool` (see [MCP servers](#mcp-servers)). |
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
just npm-release   # dist/s3p1r0o-snoopy-<version>.tgz + SHA256SUMS (refuses a dirty or untagged HEAD)
npm publish ./dist/*.tgz   # the ./ matters (bare dist/x is read as a GitHub repo); with your npm identity; publishConfig makes the scoped package public
```

The tag version becomes the npm package version, which tells the launcher which release assets to fetch (override with `SNOOPY_PROXY_VERSION`).

### Docker

One Dockerfile, two targets:

```sh
# Full standalone harness (default target): embedded proxy + dsh + supervisor.
docker build -t snoopy --build-arg VERSION=$(git describe --always) .
docker run --rm -d # --network host snoopy          # UI: http://127.0.0.1:3080/?token=… (from docker logs)
docker run --rm -p 8081:8081 snoopy headless "Reply with the single word: pong"

# Proxy alone (as before):
docker build --target proxy -t snoopy:proxy . 
docker run --rm -p 8081:8081 snoopy:proxy --key my-key

# Multi-arch (linux/amd64 + linux/arm64) through buildx; push to a registry:
just docker-buildx <registry>/snoopy linux/amd64,linux/arm64 --push   # tags <registry>/snoopy:<version>
```

Notes:
- **Bundled SearXNG.** The default image includes [SearXNG](https://github.com/searxng/searxng) (AGPL-3.0-or-later), pinned to one upstream commit (`SEARXNG_COMMIT` in the Dockerfile) and run unmodified as its own process on 127.0.0.1 inside the container; the launcher starts it before the proxy and uses it for `web_search` (falling back to DuckDuckGo if it cannot start). Its source and licence are in `/opt/searxng` in the image. It is skipped when `RILL_SEARXNG_URL` or `RILL_SEARCH_ENGINE` is set, or with `SNOOPY_WEB_TOOLS=0`; `SNOOPY_PROXY_LOG=1` also shows its logs. It adds about 350 MB to the image.
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
- Access logs (`METHOD PATH STATUS DURATION`) go to stdout; errors go to stderr. The snoopy launchers drop the access logs unless `SNOOPY_PROXY_LOG=1` (then on stderr); proxy errors always show.
- Timeouts: 10 s header, 10 min read/write (sized for long upstream completions and SSE), 2 min idle.

## DeepSeek Harness (dsh) — dev mode details

- One process, one Ctrl-C: `bin/harness.ts` builds the proxy to `build/proxy` (unless `DSH_PROXY_BIN` names one), waits for `/healthz`, then boots dsh with `DSH_HOME=$PWD/.dsh`; signals reach both.
- The routing patches `profile/{web,headless,acp}.patch.yml` declare the custom provider `proxy` (OpenAI Chat Completions, `http://127.0.0.1:$SNOOPY_PORT/v1`, model `rill-assistant`, 272k context) and point the `agent-default-model` plugin (and the acp server) at it — no picker click needed. Both launchers layer them with `--patch`; the dev home `.dsh/` is local state and not versioned.
- `profile/agentic.patch.yml` follows the routing patch in every proxy-routed profile: snoopy's defaults for agentic development. It mounts dsh's clock (`dsh-time-context`, shipped with dsh but in no profile), so the model knows the current date and time (at most one reading every 10 minutes, in the browser's zone or the machine's). Turn it off with `- id: time-context` / `disabled: true` in the profile's `cordis.patch.yml`.
- `skills/catalog.json` pins each skill source (git commit or npm version) and lists the selected skills; `just skills-sync` fetches them, copies them into `skills/bundled/` (rewriting `superpowers:<name>` references), and regenerates `skills/LICENSES/` and `skills/NOTICE.md`. To update a source, change its `ref`/`commit` (or `version`, together with the `@fission-ai/openspec` dependency), re-run the sync, and review the diff.
- Model routing edits go in `profile/*.patch.yml`; they apply on the next launch.
- **Node compatibility.** dsh's native loader only recognizes official nodejs.org builds; Nix/NixOS-compiled Node fails its probe at boot (see [deepseek-harness#690](https://github.com/deepseek-ai/deepseek-harness/discussions/690)). Both launchers prefer `DSH_NODE`, then fnm/nvm installs, then PATH — warning which one they use. On NixOS: `fnm install 24 && fnm use 24`.
- `node --check bin/launcher.js` + `npm test` run in CI as a Node-side syntax/test gate.

## Development

Common tasks are in the `justfile` (`just --list` for the menu): `just build`, `just lint`, `just test` (Go), `just node-test`, `just check`, `just run [flags]`, `just docker-build`, `just docker-buildx`, `just docker-run`, `just harness [mode]`, `just release`, `just npm-release`.

- `cd proxy && go test ./...` — handler tests in `proxy/serve_test.go`, wire-format tests pinned against generated-stub bytes in `proxy/wire_test.go`.
- `staticcheck ./...` should stay clean (CI enforces build, vet, staticcheck, gofmt, race tests; plus Node-side syntax checks and `npm test`).
- `npm test` — unit tests for the shared helpers (Node selection, routing overlay, migrations) and the launcher's stdout discipline.
- Changing what snoopy keeps in the dsh home: add a migration at the end of `MIGRATIONS` in `lib/migrations.js` (never edit a released one), with a test starting from the state the previous version leaves.
- `proxy/wire.go` pins the Rill contract: don't change encodings without re-verifying against `proxy/contract/*.proto`.