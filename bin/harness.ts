#!/usr/bin/env node
// Repo-dev launcher (snoopy): builds (when needed) and starts the proxy Go binary
// (build/proxy), then boots DeepSeek Harness (dsh) with DSH_HOME rooted at
// <repo>/.dsh and the routing patch profile/<name>.patch.yml layered with
// --patch, so the provider and default model resolve to the proxy (see README).
// The npm-published standalone launcher lives in bin/launcher.js — same
// bootstrap, but it manages its own dsh home and downloads the proxy binary if
// missing.
//
// Usage: npm run harness [-- dsh args...]     (e.g. `npm run harness -- headless "task"`)

import { spawn, spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import process from 'node:process'
import { collectNodeCandidates, pickNode } from '../lib/node-select.js'
import { agentPresetFromEnv, overlayEnv, proxyOverlayArgs, resolvePnpm, searchEngineSpec, selfImproveEnabled, webToolsSpec } from '../lib/profiles.js'
import { portIsFree, proxyStdio, waitProxyReady } from '../lib/proxy-ready.js'

const ROOT = path.resolve(new URL('./..', import.meta.url).pathname)

function ensureProxyBinary(): string {
  const bin = path.join(ROOT, 'build', 'proxy')
  if (existsSync(bin)) return bin
  const go = spawnSync('go', ['version'], { encoding: 'utf8' })
  if (go.error) throw new Error('no proxy binary and no Go toolchain to build one (run: just build)')
  // The Go module lives in proxy/, not at the repo root.
  const build = spawnSync('go', ['build', '-o', bin, '-ldflags', '-X main.version=dev', '.'], {
    cwd: path.join(ROOT, 'proxy'),
    stdio: 'inherit',
  })
  if (build.status !== 0) throw new Error('proxy build failed (see go output above; or run: just build)')
  return bin
}


const require = createRequire(import.meta.url)

async function main(argv: string[]): Promise<void> {
  const chosen = pickNode(collectNodeCandidates())
  if (!chosen) {
    console.error('[snoopy] no Node runtime found; install one (e.g. `fnm install 24`) or set DSH_NODE.')
    process.exitCode = 1
    return
  }
  if (!chosen.official) {
    console.warn(
      `[snoopy] Node ${chosen.version} at ${chosen.path} is not an official nodejs.org build;\n` +
      "[snoopy] dsh's native loader may fail on distro/Nix builds. Prefer an official one:\n" +
      '[snoopy]   fnm install 24 && fnm use 24\n' +
      '[snoopy] or force a specific binary: DSH_NODE=/path/to/node npm run harness',
    )
  }
  // Stdout belongs to dsh (headless answer, acp JSON-RPC): own messages and
  // proxy logs go to stderr.
  console.error(`[snoopy] booting dsh under Node ${chosen.version} (${chosen.official ? 'official' : 'unofficial'})`)

  const port = Number(process.env.SNOOPY_PORT ?? 8081)
  const proxyBin = process.env.DSH_PROXY_BIN ? path.resolve(process.env.DSH_PROXY_BIN) : ensureProxyBinary()
  // Loopback bind + per-launch ephemeral key: the dsh↔proxy loop is
  // mutually-credentialed and never exposed beyond localhost. The key goes by
  // env (RILL_PROXY_KEY), not the command line, so `ps` shows nothing. A
  // caller-provided RILL_PROXY_KEY (deploy intent) wins; no silent override.
  const proxyKey =
    process.env.RILL_PROXY_KEY ||
    process.env.SNOOPY_PROXY_KEY ||
    randomBytes(16).toString('hex')
  if (!(await portIsFree(port, '127.0.0.1'))) {
    throw new Error(`port ${port} is already in use (another snoopy running?); set SNOOPY_PORT to another port`)
  }
  const proxy = spawn(proxyBin, ['--port', String(port), '--address', '127.0.0.1'], {
    stdio: proxyStdio(process.env), // access logs only with SNOOPY_PROXY_LOG=1
    env: { ...process.env, RILL_PROXY_KEY: proxyKey, RILL_WEB_TOOLS: webToolsSpec(process.env), RILL_SEARCH_ENGINE: searchEngineSpec(process.env) },
  })
  try {
    await waitProxyReady(port, proxyKey, proxy)
  } catch (err) {
    proxy.kill('SIGTERM')
    throw err
  }
  console.error(`[snoopy] proxy ready: http://127.0.0.1:${port}/v1 (loopback, ephemeral bearer key)`)

  // Same routing overlay as the published launcher; .dsh itself is local state.
  const agentPreset = agentPresetFromEnv(process.env)
  const dshArgs = proxyOverlayArgs(argv, path.join(ROOT, 'profile'), { selfImprove: selfImproveEnabled(process.env), agentPreset })
  const child = spawn(chosen.path, [require.resolve('@deepseek-ai/dsh/lib/bin.js'), ...dshArgs], {
    stdio: 'inherit',
    // dsh requires a credential for the provider route even when the proxy
    // validates none: the ephemeral key (or the caller's SNOOPY_PROXY_KEY)
    // satisfies it and authenticates against the proxy's bearer check.
    env: {
      ...process.env,
      DSH_HOME: path.join(ROOT, '.dsh'),
      ...overlayEnv(process.env, {
        port,
        nodePath: chosen.path,
        pnpm: resolvePnpm(require),
        agentPreset,
      }), // read by profile/*.patch.yml
      SNOOPY_PROXY_KEY: proxyKey,
    },
  })
  const stop = (sig: NodeJS.Signals) => {
    child.kill(sig)
    proxy.kill(sig)
  }
  for (const sig of ['SIGINT', 'SIGTERM'] as const) process.on(sig, () => stop(sig))
  child.on('exit', code => {
    proxy.kill('SIGTERM')
    process.exitCode = code
  })
  await new Promise(() => {}) // hold open while children run
}

const isMain = process.argv[1] && import.meta.url === `file://${process.argv[1]}`
if (isMain) {
  main(process.argv.slice(2)).catch(err => {
    console.error('[snoopy] ' + (err instanceof Error ? err.message : err))
    process.exit(process.exitCode || 1)
  })
}

export {} // treat the module as an ES module even without top-level exports