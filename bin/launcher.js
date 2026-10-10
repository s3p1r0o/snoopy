#!/usr/bin/env node
// @s3p1r0o/snoopy — standalone launcher.
//
// One process, one Ctrl-C: make sure the embedded proxy Go binary is
// present (postinstall normally downloaded it; else download now from the
// matching GitHub Release), start it loopback-only with an ephemeral bearer
// key, then boot DeepSeek Harness (dsh) through it with the bundled profile as
// the dsh home — the proxy serves the only provider ("proxy") and new
// sessions default to model "rill-assistant".
//
// Env overrides (all optional):
//   DSH_NODE            path to a Node binary (official nodejs.org builds preferred)
//   SNOOPY_PROXY_BIN    path to a pre-built proxy binary (skips download)
//   SNOOPY_RELEASE_URL_PREFIX   base URL for proxy tarball downloads
//                       (the asset host: GitHub Releases of s3p1r0o/snoopy — the repo also hosts them until its rename lands, then redirects)
//   SNOOPY_PROXY_VERSION release version to download (default: baked at publish time)
//   SNOOPY_PORT        fixed port for the proxy (default: first free ≥ 8307)
//   DSH_HOME            custom dsh home (default: XDG data dir/snoopy/dsh)
//   SNOOPY_PROXY_KEY  fixed bearer key instead of the ephemeral one

import { spawn, spawnSync } from 'node:child_process'
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import process from 'node:process'
import { randomBytes, createHash } from 'node:crypto'
import { createRequire } from 'node:module'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { collectNodeCandidates, pickNode } from '../lib/node-select.js'

const PKG_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(import.meta.url)

const manifest = JSON.parse(readFileSync(path.join(PKG_ROOT, 'package.json'), 'utf8'))
const RELEASE_VERSION = process.env.SNOOPY_PROXY_VERSION || manifest.release?.version || manifest.version
const RELEASE_URL_PREFIX =
  process.env.SNOOPY_RELEASE_URL_PREFIX ||
  `https://github.com/s3p1r0o/snoopy/releases/download/v${RELEASE_VERSION}`
const PROXY_BIN = path.join(PKG_ROOT, 'bin', 'proxy')

const platformTag =
  `${process.platform === 'darwin' ? 'darwin' : 'linux'}-${process.arch === 'x64' ? 'amd64' : 'arm64'}`

function warn(msg) {
  console.warn(`[snoopy] ${msg}`)
}
function log(msg) {
  console.log(`[snoopy] ${msg}`)
}

// --- proxy binary provisioning ---

async function downloadProxyBinary() {
  log(`downloading proxy binary (${platformTag}, ${RELEASE_VERSION})`)
  const name = `proxy-${RELEASE_VERSION}-${platformTag}.tar.gz`
  const url = `${RELEASE_URL_PREFIX}/${name}`
  const res = await fetch(url)
  if (!res.ok) throw new Error(`release download failed: ${res.status} ${res.statusText} for ${url}`)
  const dir = mkdtempSync(path.join(os.tmpdir(), 'harness-proxy-'))
  const tgz = path.join(dir, 'proxy.tar.gz')
  const body = Buffer.from(await res.arrayBuffer())
  // Supply-chain check: compare against the release's SHA256SUMS asset. A
  // mismatch aborts hard; sums published but not covering this asset warn.
  try {
    const sums = await (await fetch(`${RELEASE_URL_PREFIX}/SHA256SUMS`)).text()
    const line = sums.split('\n').find(l => l.endsWith(name))
    if (!line) {
      warn(`SHA256SUMS does not cover ${name}; integrity not verified`)
    } else {
      const expected = line.trim().split(/\s+/)[0]
      const got = createHash('sha256').update(body).digest('hex')
      if (got !== expected) throw new Error(`checksum mismatch for ${name}: got ${got}, want ${expected}`)
    }
  } catch (err) {
    if (err instanceof Error && err.message.startsWith('checksum mismatch')) throw err
    warn(`SHA256SUMS unavailable; integrity not verified (${err instanceof Error ? err.message : err})`)
  }
  writeFileSync(tgz, body)
  spawnSync('tar', ['-xzf', tgz, '-C', dir])
  const inner = path.join(dir, `proxy-${RELEASE_VERSION}-${platformTag}`, 'proxy')
  if (!existsSync(inner)) throw new Error('unexpected release tarball layout')
  mkdirSync(path.dirname(PROXY_BIN), { recursive: true })
  copyFileSync(inner, PROXY_BIN)
  chmodSync(PROXY_BIN, 0o755)
  rmSync(dir, { recursive: true, force: true })
  return PROXY_BIN
}

async function resolveProxyBinary() {
  if (process.env.SNOOPY_PROXY_BIN) return path.resolve(process.env.SNOOPY_PROXY_BIN)
  if (existsSync(PROXY_BIN)) return PROXY_BIN
  try {
    return await downloadProxyBinary()
  } catch (err) {
    throw new Error(
      `no embedded proxy binary and download failed (${err.message});\n` +
      'install one with `npx snoopy download` or point SNOOPY_PROXY_BIN at a built binary',
    )
  }
}

// --- dsh home (bundled profile) ---

function xdgDataHome() {
  return process.env.XDG_DATA_HOME || path.join(os.homedir(), '.local', 'share')
}

/** Write the bundled web+headless profiles under dshHome unless already present. */
function ensureDshProfile(dshHome, port) {
  for (const profileName of ['web', 'headless']) {
    const profiles = path.join(dshHome, 'profiles')
    mkdirSync(profiles, { recursive: true })
    const patch = path.join(profiles, profileName, 'cordis.patch.yml')
    if (!existsSync(patch)) {
      const template = readFileSync(path.join(PKG_ROOT, 'profile', `${profileName}.patch.yml`), 'utf8')
      mkdirSync(path.dirname(patch), { recursive: true })
      writeFileSync(patch, template.replaceAll('{{PROXY_PORT}}', String(port)))
      log(`profile written to ${patch}`)
    }
  }
}

// --- network helpers ---

function firstFreePort(from = 8307) {
  return new Promise((resolve, reject) => {
    const tryPort = port => {
      if (port > 65000) return reject(new Error('no free port found ≥ 8307'))
      const srv = net.createServer()
      srv.once('error', () => tryPort(port + 1))
      srv.once('listening', () => srv.close(() => resolve(port)))
      srv.listen(port, '127.0.0.1')
    }
    tryPort(from)
  })
}

async function waitHealthy(port, timeoutMs = 10_000) {
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

// --- main ---

async function main(argv) {
  const chosen = pickNode(collectNodeCandidates())
  if (!chosen) {
    console.error('[snoopy] no Node runtime found; install Node >= 20 from nodejs.org, or set DSH_NODE.')
    process.exitCode = 1
    return
  }
  if (!chosen.official) {
    warn(`Node ${chosen.version} at ${chosen.path} is not an official nodejs.org build`)
    warn("dsh's native loader may reject it; prefer an official install (nvm/fnm/nodejs.org)")
  }
  log(`booting dsh under Node ${chosen.version} (${chosen.official ? 'official' : 'unofficial'})`)

  const proxyBin = await resolveProxyBinary()
  const port = process.env.SNOOPY_PORT ? Number(process.env.SNOOPY_PORT) : await firstFreePort()
  // Loopback bind by default + per-launch ephemeral key: dsh↔proxy traffic
  // stays local and mutually credentialed; the key travels by env, never
  // argv. Containers can widen the bind with SNOOPY_PROXY_ADDRESS.
  const proxyAddress = process.env.SNOOPY_PROXY_ADDRESS ?? '127.0.0.1'
  // A caller-provided RILL_PROXY_KEY (deploy intent) must win over the
  // ephemeral default — silently replacing an explicit key would drop the
  // proxy's auth posture; hence the precedence below, not a plain override.
  const proxyKey =
    process.env.RILL_PROXY_KEY ||
    process.env.SNOOPY_PROXY_KEY ||
    randomBytes(16).toString('hex')
  const proxy = spawn(proxyBin, ['--port', String(port), '--address', proxyAddress], {
    stdio: ['ignore', 'inherit', 'inherit'],
    env: { ...process.env, RILL_PROXY_KEY: proxyKey },
  })
  process.on('exit', () => proxy.kill('SIGTERM'))
  if (!(await waitHealthy(port))) {
    proxy.kill('SIGTERM')
    throw new Error(`proxy on :${port} did not become healthy within 10s`)
  }
  log(`proxy ready: http://${proxyAddress}:${port}/v1 (bearer-key protected)`)

  const dshHome = process.env.DSH_HOME || path.join(xdgDataHome(), 'snoopy', 'dsh')
  ensureDshProfile(dshHome, port)

  const child = spawn(chosen.path, [require.resolve('@deepseek-ai/dsh/lib/bin.js'), ...argv], {
    stdio: 'inherit',
    env: {
      ...process.env,
      DSH_HOME: dshHome,
      SNOOPY_PROXY_KEY: proxyKey,
    },
  })
  const stop = sig => {
    child.kill(sig)
    proxy.kill(sig)
  }
  for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => stop(sig))
  child.on('exit', code => {
    proxy.kill('SIGTERM')
    process.exitCode = code
  })
  await new Promise(() => {}) // hold open while children run
}

// --- download subcommand: `snoopy download` ---

async function downloadCommand() {
  if (existsSync(PROXY_BIN)) {
    log(`proxy binary already present: ${PROXY_BIN}`)
    return
  }
  await downloadProxyBinary()
  log(`proxy binary installed: ${PROXY_BIN} (the runtime download is skipped on next boot)`)
}

// argv[1] comes through the npm bin symlink, so the comparison must run on
// realpaths: an unresolved check would silently skip main (no output, exit 0).
const isMain = (() => {
  if (!process.argv[1]) return false
  try {
    return pathToFileURL(realpathSync(process.argv[1])).href === pathToFileURL(realpathSync(fileURLToPath(import.meta.url))).href
  } catch {
    return false
  }
})()
if (isMain) {
  const cmd = process.argv[2]
  const rest = cmd === 'download' && existsSync(PROXY_BIN) ? [] : process.argv.slice(2)
  const runner = cmd === 'download' ? downloadCommand() : main(rest)
  runner.catch(err => {
    console.error('[snoopy] ' + (err instanceof Error ? err.message : err))
    process.exit(1)
  })
}

export { firstFreePort, ensureDshProfile, resolveProxyBinary, downloadProxyBinary, PROXY_BIN, platformTag, RELEASE_VERSION, RELEASE_URL_PREFIX }