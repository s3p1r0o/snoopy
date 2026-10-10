#!/usr/bin/env node
// Repo-dev launcher (snoopy): builds (when needed) and starts the proxy Go binary
// (build/proxy), then boots DeepSeek Harness (dsh) with DSH_HOME rooted at
// <repo>/.dsh so the provider and default model resolve from
// .dsh/profiles/web/cordis.patch.yml (see README). The npm-published standalone
// launcher lives in packages/harness/bin/launcher.js — same bootstrap, but it
// manages its own dsh home and downloads the proxy binary if missing.
//
// Usage: npm run harness [-- dsh args...]     (e.g. `npm run harness -- headless "task"`)

import { spawn, spawnSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import process from 'node:process'
import { collectNodeCandidates, pickNode } from '../lib/node-select.js'

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

async function waitHealthy(port: number, timeoutMs = 10_000): Promise<boolean> {
  const url = `http://127.0.0.1:${port}/healthz`
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url)
      if (res.ok) return true
    } catch {}
    await new Promise(r => setTimeout(r, 200))
  }
  return false
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
  console.log(`[snoopy] booting dsh under Node ${chosen.version} (${chosen.official ? 'official' : 'unofficial'})`)

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
  const proxy = spawn(proxyBin, ['--port', String(port), '--address', '127.0.0.1'], {
    stdio: ['ignore', 'inherit', 'inherit'],
    env: { ...process.env, RILL_PROXY_KEY: proxyKey },
  })
  if (!(await waitHealthy(port))) {
    proxy.kill('SIGTERM')
    throw new Error(`proxy on :${port} did not become healthy within 10s`)
  }
  console.log(`[snoopy] proxy ready: http://127.0.0.1:${port}/v1 (loopback, ephemeral bearer key)`)

  const child = spawn(chosen.path, [require.resolve('@deepseek-ai/dsh/lib/bin.js'), ...argv], {
    stdio: 'inherit',
    // dsh requires a credential for the provider route even when the proxy
    // validates none: the ephemeral key (or the caller's SNOOPY_PROXY_KEY)
    // satisfies it and authenticates against the proxy's bearer check.
    env: {
      ...process.env,
      DSH_HOME: path.join(ROOT, '.dsh'),
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