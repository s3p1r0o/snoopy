// Unit tests for the launchers' shared helpers (lib/) and bin/launcher.js.
// node --test scripts/harness.test.ts

import test from 'node:test'
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { firstFreePort } from '../bin/launcher.js'
import { EMPTY_PROFILE_PATCH, LEGACY_PROFILE_PATCHES, MIGRATIONS, migrateDshHome } from '../lib/migrations.js'
import { PROXY_PROFILES, proxyOverlayArgs } from '../lib/profiles.js'
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

test('proxyOverlayArgs layers the packaged routing after the profile name', () => {
  const overlay = (name: string) => ['--patch', path.join(PKG_PROFILE_DIR, `${name}.patch.yml`)]
  assert.deepEqual(proxyOverlayArgs(['acp'], PKG_PROFILE_DIR), ['acp', ...overlay('acp')])
  assert.deepEqual(proxyOverlayArgs(['web', '--no-open'], PKG_PROFILE_DIR), ['web', ...overlay('web'), '--no-open'])
  assert.deepEqual(proxyOverlayArgs(['--profile', 'headless', 'task'], PKG_PROFILE_DIR), ['--profile', 'headless', ...overlay('headless'), 'task'])
  assert.deepEqual(proxyOverlayArgs(['--profile=acp'], PKG_PROFILE_DIR), ['--profile=acp', ...overlay('acp')])
})

test('proxyOverlayArgs leaves other invocations alone', () => {
  for (const argv of [[], ['--help'], ['sdk'], ['plugin', '--profile', 'web', 'add', 'x']]) {
    assert.deepEqual(proxyOverlayArgs(argv, PKG_PROFILE_DIR), argv)
  }
})

test('every proxy-routed profile ships a routing patch that reads the port from the environment', () => {
  for (const name of PROXY_PROFILES) {
    const patch = readFileSync(path.join(PKG_PROFILE_DIR, `${name}.patch.yml`), 'utf8')
    assert.match(patch, /baseURL: !!js .*process\.env\.SNOOPY_PORT/)
    assert.match(patch, /- id: agent-default-model\n\s+config:\n\s+provider: proxy\n\s+model: rill-assistant/)
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
  assert.match(stderr, /GET \/healthz 200/)
  for (const line of stdout.split('\n').filter(Boolean)) {
    assert.doesNotThrow(() => JSON.parse(line), `non-JSON line on stdout: ${line}`)
  }
})
