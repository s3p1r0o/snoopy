// Unit tests for the launchers' shared helpers (lib/) and bin/launcher.js.
// node --test scripts/harness.test.ts

import test from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { accessSync, chmodSync, constants, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { firstFreePort } from '../bin/launcher.js'
import { portIsFree, proxyStdio, waitProxyReady } from '../lib/proxy-ready.js'
import { EMPTY_PROFILE_PATCH, LEGACY_PROFILE_PATCHES, MIGRATIONS, migrateDshHome } from '../lib/migrations.js'
import { agentPresetFromEnv, overlayEnv, PROXY_PROFILES, proxyOverlayArgs, resolvePnpm, selfImproveEnabled, webToolsSpec } from '../lib/profiles.js'
import { type NodeCandidate, parseNodeVersion, pickNode } from '../lib/node-select.js'

test('parseNodeVersion extracts a leading version from a `node -v` payload', () => {
  assert.equal(parseNodeVersion('v24.16.0\n'), '24.16.0')
  assert.equal(parseNodeVersion('garbage'), null)
})

const candidate = (path: string, version: string, official: boolean): NodeCandidate => ({
  path, version, official })

test('pickNode prefers the highest official build over any non-official one', () => {
  const candidates = [
    candidate('/nix/node', '24.21.0', false),
    candidate('/fnm/v24.16.0/node', '24.16.0', true),
    candidate('/fnm/v22.9.0/node', '22.9.0', true),
  ]
  const want = candidates.find(c => c.path === '/fnm/v24.16.0/node')!
  assert.deepEqual(pickNode(candidates), want)
})

test('pickNode falls back to the highest non-official build when no official exists', () => {
  const candidates = [
    { path: '/nix/node', version: '24.21.0', official: false },
    { path: '/usr/node', version: '20.3.1', official: false },
  ]
  assert.deepEqual(pickNode(candidates), candidates[0])
})

test('pickNode returns null for an empty candidate list', () => {
  assert.equal(pickNode([]), null)
})

test('pickNode orders versions numerically, not lexicographically', () => {
  const candidates = [
    { path: '/nix/node', version: '9.21.0', official: false },
    { path: '/usr/node', version: '20.3.1', official: false },
  ]
  assert.deepEqual(pickNode(candidates), candidates[1])
})

const PKG_PROFILE_DIR = path.join(import.meta.dirname, '..', 'profile')

const overlay = (name: string) => ['--patch', path.join(PKG_PROFILE_DIR, `${name}.patch.yml`)]
const SELF_IMPROVE = overlay('self-improve')

test('proxyOverlayArgs layers the packaged routing and self-improvement after the profile name', () => {
  const on = { selfImprove: true }
  assert.deepEqual(proxyOverlayArgs(['acp'], PKG_PROFILE_DIR, on), ['acp', ...overlay('acp'), ...SELF_IMPROVE])
  assert.deepEqual(proxyOverlayArgs(['web', '--no-open'], PKG_PROFILE_DIR, on), ['web', ...overlay('web'), ...SELF_IMPROVE, '--no-open'])
  assert.deepEqual(proxyOverlayArgs(['--profile', 'headless', 'task'], PKG_PROFILE_DIR, on), ['--profile', 'headless', ...overlay('headless'), ...SELF_IMPROVE, 'task'])
  assert.deepEqual(proxyOverlayArgs(['--profile=acp'], PKG_PROFILE_DIR, on), ['--profile=acp', ...overlay('acp'), ...SELF_IMPROVE])
})

test('proxyOverlayArgs keeps the routing but drops self-improvement when opted out', () => {
  assert.deepEqual(proxyOverlayArgs(['acp', 'x'], PKG_PROFILE_DIR, { selfImprove: false }), ['acp', ...overlay('acp'), 'x'])
})

test('proxyOverlayArgs leaves other invocations alone', () => {
  for (const argv of [[], ['--help'], ['sdk'], ['plugin', '--profile', 'web', 'add', 'x']]) {
    assert.deepEqual(proxyOverlayArgs(argv, PKG_PROFILE_DIR, { selfImprove: true }), argv)
  }
})

test('self-improvement is on unless SNOOPY_SELF_IMPROVE opts out', () => {
  assert.equal(selfImproveEnabled({}), true)
  for (const value of ['1', 'true', 'yes', '']) assert.equal(selfImproveEnabled({ SNOOPY_SELF_IMPROVE: value }), true, value)
  for (const value of ['0', 'false', 'FALSE', 'off', 'no']) assert.equal(selfImproveEnabled({ SNOOPY_SELF_IMPROVE: value }), false, value)
})

test('the self-improvement patch turns on the agent-facing plugin_manager tool', () => {
  const patch = readFileSync(path.join(PKG_PROFILE_DIR, 'self-improve.patch.yml'), 'utf8')
  assert.match(patch, /- id: tool-plugin-manager\n\s+disabled: false/)
})

test('overlayEnv hands dsh the port, pnpm, agent preset, and the chosen Node first on PATH', () => {
  const options = { port: 8307, nodePath: '/opt/node/bin/node', pnpm: '/pkg/pnpm.mjs', agentPreset: 'cordis' }
  assert.deepEqual(overlayEnv({ PATH: '/usr/bin' }, options), {
    SNOOPY_PORT: '8307',
    PATH: ['/opt/node/bin', '/usr/bin'].join(path.delimiter),
    SNOOPY_PNPM: '/pkg/pnpm.mjs',
    SNOOPY_AGENT_PRESET: 'cordis',
  })
  assert.deepEqual(overlayEnv({}, { ...options, pnpm: undefined, agentPreset: undefined }), { SNOOPY_PORT: '8307', PATH: '/opt/node/bin' })
})

test('SNOOPY_AGENT_PRESET names the default agent preset; creator is the UI name of cordis', () => {
  assert.equal(agentPresetFromEnv({}), undefined)
  assert.equal(agentPresetFromEnv({ SNOOPY_AGENT_PRESET: ' ' }), undefined)
  assert.equal(agentPresetFromEnv({ SNOOPY_AGENT_PRESET: 'Creator' }), 'cordis')
  assert.equal(agentPresetFromEnv({ SNOOPY_AGENT_PRESET: 'ptc' }), 'ptc')
})

test('the agent-preset patch only layers onto web, the profile with presets, and only when asked', () => {
  const preset = ['--patch', path.join(PKG_PROFILE_DIR, 'agent-preset.patch.yml')]
  const options = { selfImprove: false, agentPreset: 'cordis' }
  assert.deepEqual(proxyOverlayArgs(['web', '--no-open'], PKG_PROFILE_DIR, options), ['web', ...overlay('web'), ...preset, '--no-open'])
  assert.deepEqual(proxyOverlayArgs(['acp'], PKG_PROFILE_DIR, options), ['acp', ...overlay('acp')])
  assert.deepEqual(proxyOverlayArgs(['web'], PKG_PROFILE_DIR, { selfImprove: false, agentPreset: undefined }), ['web', ...overlay('web')])
  const patch = readFileSync(path.join(PKG_PROFILE_DIR, 'agent-preset.patch.yml'), 'utf8')
  assert.match(patch, /- id: agent-preset-registry\n\s+config:\n\s+default: !!js process\.env\.SNOOPY_AGENT_PRESET/)
})

test('resolvePnpm finds the bundled, executable pnpm', () => {
  const pnpm = resolvePnpm(createRequire(import.meta.url))
  assert.ok(pnpm && existsSync(pnpm), String(pnpm))
  accessSync(pnpm, constants.X_OK)
  assert.match(readFileSync(path.join(path.dirname(pnpm), '..', 'package.json'), 'utf8'), /"name": "pnpm"/)
})

test('every proxy-routed profile ships a routing patch that reads the port from the environment', () => {
  for (const name of PROXY_PROFILES) {
    const patch = readFileSync(path.join(PKG_PROFILE_DIR, `${name}.patch.yml`), 'utf8')
    assert.match(patch, /baseURL: !!js .*process\.env\.SNOOPY_PORT/)
    assert.match(patch, /- id: agent-default-model\n\s+config:\n\s+provider: proxy\n\s+model: rill-assistant/)
    // The Web Plugins page installs bundles too: pnpm is wired with the routing, not the opt-out.
    assert.match(patch, /- id: plugin-manager\n\s+config:\n\s+pnpmCommand: !!js .*process\.env\.SNOOPY_PNPM/)
  }
  // dsh-acp-app pins every ACP session to deepseek-official/deepseek-v4-flash
  // on its `acp` entry; the routing patch must override that entry itself.
  const acp = readFileSync(path.join(PKG_PROFILE_DIR, 'acp.patch.yml'), 'utf8')
  assert.match(acp, /- id: acp\n\s+config:\n\s+provider: proxy\n\s+model: rill-assistant/)
})

/** A dsh home as snoopy 0.1.x left it: its routing template copied in, port substituted. */
function legacyHome(edit: (name: string, text: string) => string = (_, text) => text): string {
  const home = mkdtempSync(path.join(os.tmpdir(), 'snoopy-migrate-'))
  for (const name of ['web', 'headless']) {
    const dir = path.join(home, 'profiles', name)
    mkdirSync(dir, { recursive: true })
    const text = LEGACY_PROFILE_PATCHES[name].replaceAll('{{PROXY_PORT}}', '8312')
    writeFileSync(path.join(dir, 'cordis.patch.yml'), edit(name, text))
  }
  return home
}
const readPatch = (home: string, name: string) => readFileSync(path.join(home, 'profiles', name, 'cordis.patch.yml'), 'utf8')

test('migration resets an untouched 0.1.x routing copy to an empty user layer', () => {
  const home = legacyHome()
  try {
    migrateDshHome(home, () => {})
    for (const name of ['web', 'headless']) {
      assert.equal(readPatch(home, name), EMPTY_PROFILE_PATCH)
      assert.equal(existsSync(path.join(home, 'profiles', name, 'cordis.patch.yml.pre-routing-overlay')), false)
    }
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('migration strips the 0.1.x routing copy but keeps what the user added, with a backup', () => {
  const home = legacyHome((name, text) => (name === 'web' ? text + '\n\n- id: hmr\n  disabled: false\n' : text))
  try {
    migrateDshHome(home, () => {})
    const web = readPatch(home, 'web')
    assert.doesNotMatch(web, /baseURL/)
    assert.match(web, /- id: hmr\n  disabled: false\n/)
    const backup = readFileSync(path.join(home, 'profiles', 'web', 'cordis.patch.yml.pre-routing-overlay'), 'utf8')
    assert.match(backup, /baseURL: http:\/\/127\.0\.0\.1:8312\/v1/)
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('migration keeps a comment-only remainder a valid patch list', () => {
  // As found on a real 0.1.1 home: a comment appended right after the copy.
  const home = legacyHome((name, text) => (name === 'web' ? text + '# user edit' : text))
  try {
    migrateDshHome(home, () => {})
    // dsh rejects a user layer that is not a top-level YAML array.
    assert.equal(readPatch(home, 'web'), '# user edit\n[]\n')
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('migration leaves a routing copy the user changed untouched', () => {
  const home = legacyHome((name, text) => (name === 'web' ? text.replace('maxTokens: 32768', 'maxTokens: 8192') : text))
  try {
    const before = readPatch(home, 'web')
    migrateDshHome(home, () => {})
    assert.equal(readPatch(home, 'web'), before)
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('migrations run once per dsh home and are recorded', () => {
  const home = legacyHome()
  try {
    assert.deepEqual(migrateDshHome(home, () => {}), MIGRATIONS.map(m => m.id))
    // A later edit that happens to look like a legacy copy must survive reboots.
    writeFileSync(path.join(home, 'profiles', 'web', 'cordis.patch.yml'), LEGACY_PROFILE_PATCHES.web)
    assert.deepEqual(migrateDshHome(home, () => {}), [])
    assert.equal(readPatch(home, 'web'), LEGACY_PROFILE_PATCHES.web)
    const state = JSON.parse(readFileSync(path.join(home, 'snoopy-migrations.json'), 'utf8'))
    assert.deepEqual(state.applied.map((a: { id: string }) => a.id), MIGRATIONS.map(m => m.id))
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('migrations on a fresh dsh home create nothing but the state file', () => {
  const home = path.join(mkdtempSync(path.join(os.tmpdir(), 'snoopy-migrate-')), 'dsh')
  try {
    migrateDshHome(home, () => {})
    assert.deepEqual(readdirSync(home), ['snoopy-migrations.json'])
  } finally {
    rmSync(path.dirname(home), { recursive: true, force: true })
  }
})

// A stand-in proxy that answers /healthz and logs each request to stdout, as
// the real one does: the launcher must keep that output off its own stdout.
const FAKE_PROXY = `#!/usr/bin/env node
const http = require('node:http')
const port = Number(process.argv[process.argv.indexOf('--port') + 1])
http.createServer((req, res) => {
  console.log(req.method + ' ' + req.url + ' 200')
  res.end('ok')
}).listen(port, '127.0.0.1')
`

test('in acp mode the launcher leaves stdout to the protocol', async () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'snoopy-stdout-'))
  const fakeProxy = path.join(dir, 'proxy.cjs')
  writeFileSync(fakeProxy, FAKE_PROXY)
  chmodSync(fakeProxy, 0o755)
  const launcher = path.join(import.meta.dirname, '..', 'bin', 'launcher.js')
  const child = spawn(process.execPath, [launcher, 'acp'], {
    env: {
      ...process.env,
      DSH_HOME: path.join(dir, 'dsh'),
      SNOOPY_PROXY_BIN: fakeProxy,
      SNOOPY_PORT: String(await firstFreePort(18400)),
    },
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  let stdout = ''
  let stderr = ''
  child.stdout.on('data', chunk => { stdout += chunk })
  child.stderr.on('data', chunk => { stderr += chunk })
  // Without dsh installed (CI runs no npm ci) the launcher exits after the
  // proxy boot; with dsh it serves ACP and answers this request.
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: 1 } }) + '\n')
  const deadline = Date.now() + 30_000
  while (child.exitCode === null && !stdout.includes('"id":1') && Date.now() < deadline) {
    await new Promise(r => setTimeout(r, 100))
  }
  child.kill('SIGTERM')
  await new Promise(r => (child.exitCode === null ? child.once('exit', r) : r(undefined)))
  rmSync(dir, { recursive: true, force: true })
  assert.match(stderr, /\[snoopy\] proxy ready/)
  assert.doesNotMatch(stderr, /GET \/v1\/models 200/) // proxy access logs are off by default
  for (const line of stdout.split('\n').filter(Boolean)) {
    assert.doesNotThrow(() => JSON.parse(line), `non-JSON line on stdout: ${line}`)
  }
})

/** An HTTP server on a free port answering every request with `status`. */
async function stubServer(status: number) {
  const { createServer } = await import('node:http')
  const port = await firstFreePort(18600)
  const server = createServer((_req, res) => { res.statusCode = status; res.end('{}') })
  await new Promise<void>(r => server.listen(port, '127.0.0.1', r))
  return { port, close: () => new Promise(r => server.close(r)) }
}
const longRunning = () => spawn(process.execPath, ['-e', 'setTimeout(() => {}, 30000)'])

test('portIsFree tells a taken port from a free one', async () => {
  const stub = await stubServer(200)
  try {
    assert.equal(await portIsFree(stub.port, '127.0.0.1'), false)
  } finally {
    await stub.close()
  }
  assert.equal(await portIsFree(stub.port, '127.0.0.1'), true)
})

test('waitProxyReady refuses a port served by another proxy (it rejects our key)', async () => {
  const stub = await stubServer(401)
  const child = longRunning()
  try {
    await assert.rejects(waitProxyReady(stub.port, 'k', child, { timeoutMs: 3000 }), /another proxy/)
  } finally {
    child.kill()
    await stub.close()
  }
})

test('waitProxyReady fails fast when our proxy exits before it is ready', async () => {
  const port = await firstFreePort(18650)
  const child = spawn(process.execPath, ['-e', 'process.exit(3)'])
  const started = Date.now()
  await assert.rejects(waitProxyReady(port, 'k', child, { timeoutMs: 8000 }), /exited/)
  assert.ok(Date.now() - started < 4000, 'did not wait for the timeout')
})

test('waitProxyReady resolves once the proxy accepts our key', async () => {
  const stub = await stubServer(200)
  const child = longRunning()
  try {
    await waitProxyReady(stub.port, 'k', child, { timeoutMs: 3000 })
  } finally {
    child.kill()
    await stub.close()
  }
})

test('proxy access logs are off by default; SNOOPY_PROXY_LOG=1 sends them to stderr', () => {
  assert.deepEqual(proxyStdio({}), ['ignore', 'ignore', 2]) // errors still reach stderr
  assert.deepEqual(proxyStdio({ SNOOPY_PROXY_LOG: '0' }), ['ignore', 'ignore', 2])
  for (const value of ['1', 'true', 'yes']) assert.deepEqual(proxyStdio({ SNOOPY_PROXY_LOG: value }), ['ignore', 2, 2], value)
})

test('webToolsSpec turns the proxy web tools on unless opted out; an explicit RILL_WEB_TOOLS wins', () => {
  assert.equal(webToolsSpec({}), 'all')
  for (const value of ['0', 'false', 'OFF', 'no']) assert.equal(webToolsSpec({ SNOOPY_WEB_TOOLS: value }), 'none', value)
  assert.equal(webToolsSpec({ SNOOPY_WEB_TOOLS: '1' }), 'all')
  assert.equal(webToolsSpec({ SNOOPY_WEB_TOOLS: '0', RILL_WEB_TOOLS: 'fetch' }), 'fetch')
  assert.equal(webToolsSpec({ RILL_WEB_TOOLS: ' ' }), 'all')
})
