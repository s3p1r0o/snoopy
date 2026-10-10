// The SearXNG instance bundled in the snoopy Docker image (SNOOPY_SEARXNG_DIR),
// started by bin/launcher.js as the proxy's web_search engine. SearXNG runs
// unmodified as its own process (AGPL-3.0, source in the image and upstream),
// bound to 127.0.0.1 inside the container; the proxy talks to it over HTTP.

import { spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdtempSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { webToolsSpec } from './profiles.js'

/**
 * The bundled SearXNG directory when it should start: in the image (which sets
 * SNOOPY_SEARXNG_DIR), with web tools on, and with no engine chosen already
 * (RILL_SEARXNG_URL or RILL_SEARCH_ENGINE win). Undefined otherwise.
 * @param {NodeJS.ProcessEnv} env
 * @returns {string | undefined}
 */
export function embeddedSearxngDir(env) {
  const dir = String(env.SNOOPY_SEARXNG_DIR ?? '').trim()
  if (dir === '' || webToolsSpec(env) === 'none') return undefined
  if (String(env.RILL_SEARXNG_URL ?? '').trim() !== '' || String(env.RILL_SEARCH_ENGINE ?? '').trim() !== '') return undefined
  return dir
}

/**
 * settings.yml for the bundled instance: SearXNG's defaults minus the
 * Tor-only engines (they need a Tor proxy and fail to load), the JSON API on
 * (the proxy's searxng engine uses it), and the bot limiter off (its only
 * client is the local proxy; the limiter would also need Valkey).
 * @param {string} secret - the instance secret, random per launch.
 * @returns {string}
 */
export function searxngSettings(secret) {
  return `use_default_settings:
  engines:
    remove:
      - ahmia
      - torch
general:
  instance_name: snoopy
server:
  secret_key: "${secret}"
  limiter: false
  image_proxy: false
  public_instance: false
search:
  formats: [html, json]
`
}

/**
 * Start the bundled SearXNG with granian on 127.0.0.1:port and wait until its
 * /healthz answers. Rejects when granian exits first or the wait times out.
 * @param {{ dir: string, port: number, stdio: import('node:child_process').StdioOptions, timeoutMs?: number }} options
 * @returns {Promise<{ url: string, child: import('node:child_process').ChildProcess }>}
 */
export async function startSearxng({ dir, port, stdio, timeoutMs = 60_000 }) {
  const settingsDir = mkdtempSync(path.join(os.tmpdir(), 'snoopy-searxng-'))
  const settingsPath = path.join(settingsDir, 'settings.yml')
  writeFileSync(settingsPath, searxngSettings(randomBytes(32).toString('hex')), { mode: 0o600 })
  // Bot detection reads limiter.toml next to settings.yml even with the limiter off.
  writeFileSync(path.join(settingsDir, 'limiter.toml'), '')
  const child = spawn(path.join(dir, '.venv', 'bin', 'granian'), ['searx.webapp:app'], {
    cwd: dir,
    stdio,
    env: {
      ...process.env,
      SEARXNG_SETTINGS_PATH: settingsPath,
      GRANIAN_INTERFACE: 'wsgi',
      GRANIAN_HOST: '127.0.0.1',
      GRANIAN_PORT: String(port),
      GRANIAN_BLOCKING_THREADS: '4',
    },
  })
  let exited
  child.once('exit', (code, signal) => { exited = signal ?? `exit code ${code}` })
  child.once('error', err => { exited = err.message })
  const url = `http://127.0.0.1:${port}`
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (exited !== undefined) throw new Error(`SearXNG exited (${exited}) before it was ready`)
    try {
      // X-Real-IP: the probe is local, which SearXNG's bot detection wants to be told.
      if ((await fetch(url + '/healthz', { headers: { 'X-Real-IP': '127.0.0.1' } })).ok) return { url, child }
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 250))
  }
  child.kill('SIGTERM')
  throw new Error(`SearXNG did not answer on ${url}/healthz within ${timeoutMs / 1000}s`)
}
