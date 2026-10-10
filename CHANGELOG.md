# Changelog

All notable changes to this project are documented here.
Format based on [Keep a Changelog](https://keepachangelog.com/); the project does not yet follow SemVer (single-binary tool, version stamped at build time).

## [Unreleased]

- **just recipes.** `harness`, `run` and `docker-run` pass their arguments to the command as given (`[positional-arguments]`): an apostrophe in a question used to break `just harness headless "…l'été…"` with a shell syntax error.
- **Web tools in the proxy.** `--web-tools` (`RILL_WEB_TOOLS`) makes the proxy run `web_fetch` (main content as Markdown: Readability, charset decoding), `web_crawl` (same-host, robots.txt, up to 20 pages) and `web_search` (SearXNG via `RILL_SEARXNG_URL`, or Brave via `BRAVE_SEARCH_API_KEY`) in a server-side tool loop; it owns those tool names, replacing a client's own. Pages come from public addresses only (checked at connect time), with redirect, size and time limits. Long streamed completions get SSE keep-alives. The snoopy launchers turn the tools on (`SNOOPY_WEB_TOOLS=0` opts out), replacing dsh's DeepSeek-only `web_search`.
- **ACP.** `snoopy acp` serves the Agent Client Protocol on stdio for orchestrators, routed through the proxy (overrides the acp bundle's own `deepseek-official` model). Also in dev mode (`just harness acp`).
- **Routing from the package.** Both launchers layer `profile/<name>.patch.yml` with `--patch` on every boot (port through `SNOOPY_PORT`) instead of copying it into the dsh home, so an upgrade brings the new routing and `cordis.patch.yml` stays the user's (and the agent's) layer.
- **Migrations.** `lib/migrations.js`: ordered, append-only migrations applied once per dsh home and recorded in `snoopy-migrations.json`. The first one removes the 0.1.x routing copies, keeping user entries (with a backup) and leaving edited copies alone.
- **Self-improvement (opt-out).** `profile/self-improve.patch.yml` turns on dsh's agent-facing `plugin_manager` tool (list, enable, disable plugins; install, remove bundles) unless `SNOOPY_SELF_IMPROVE=0`. Every action still needs approval: the Web user, the acp orchestrator (`session/request_permission`), or `DSH_PERMISSION_MODE=danger-full-access` in headless.
- **Agent preset at launch.** `SNOOPY_AGENT_PRESET` sets the default agent preset for new web sessions (`creator` for dsh's Creator mode, `cordis`, which mounts dsh's own plugin-development skills); unset, nothing changes. Plugin authoring is left to Creator mode.
- **Dev dsh home is local.** `.dsh/` (sessions, credentials, profiles) is git-ignored as a whole: dsh regenerates the profiles at boot, and its Web UI and plugin installs rewrite them (`package.json`, lockfile, user-layer choices such as the default model). The repo's own dsh settings live in `profile/*.patch.yml`. This also drops the committed, dangling `dsh-walking-pet` workspace dependency, which made every plugin install in the web profile fail with `ERR_PNPM_WORKSPACE_PKG_NOT_FOUND`.
- **Workspace bundles.** `AGENTS.md` has dsh agents (Creator mode) write this repo's plugin bundles under `bundles/<name>/`, git-ignored.
- **Bundled pnpm.** `pnpm@11.28.0` ships as a dependency and dsh's plugin manager uses it (`SNOOPY_PNPM`) for bundle installs, from the agent tool and the Web Plugins page. dsh now runs with the chosen Node first on `PATH`.
- **Node ≥ 22.13** (pnpm 11's floor; Node 20 is end-of-life).
- **Proxy readiness.** Both launchers refuse a fixed port that is already taken, and only call the proxy ready once it accepts this launch's key on `/v1/models` (`/healthz` is open to any proxy): with another snoopy on the port, `just harness` used to declare that proxy ready and dsh then failed with a bare 401.
- **Stdout belongs to dsh.** Both launchers send their own messages and the proxy's errors to stderr, so the ACP stream and `snoopy headless` output stay clean.
- **Quiet proxy.** The launchers drop the proxy's startup line and access logs (one line per chat completion) unless `SNOOPY_PROXY_LOG=1`; proxy warnings and errors still show.
- **Docs.** Drop the `tui` mode: this dsh release ships no terminal UI (`snoopy tui` failed with `profile "tui" does not exist`). `web` and `headless` are the profiles routed through the proxy.

## [0.1.1] - 2026-10-10

- **npm page.** README opens with install, usage and environment for package users (development notes follow); `package.json` gains repository, homepage, bugs, keywords and author; MIT `LICENSE` file added (shipped in the package).
- **Docs.** `npm publish ./dist/*.tgz`: without the `./`, npm reads `dist/<file>` as a GitHub repository.

## [0.1.0] - 2026-10-10

Withdrawn from npm (superseded by 0.1.1); the version number cannot be reused there.

- **Releases.** Tags are `v<version>`; pushing one runs the `release` workflow, which publishes the four proxy tarballs (linux/darwin × amd64/arm64) and `SHA256SUMS` as the GitHub Release the npm launcher downloads from. Asset names and the stamped version drop the `v`.
- **npm package.** Publishable (no longer `private`, public scoped access). `just npm-release` takes the package version from the release tag, refuses a dirty or untagged HEAD, and leaves `package.json` unchanged.
- **Launcher.** An existing dsh profile is retargeted at each launch's proxy port; previously the first boot's port stayed in place and dsh lost the proxy when the port changed.
- **Dev harness.** `npm run harness` builds the proxy from `proxy/` (it ran `go build` at the repo root and ignored the failure).
- **Docker.** The Go stage cross-compiles on the build host; `just docker-buildx` builds linux/amd64 + linux/arm64 images.
- **CI.** Cross-compiles the release targets on every push.
