// Unit tests for the unified launcher's pure helpers (bin/harness.mjs).
// node --test scripts/harness.test.mjs

import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { ensureDshProfile } from '../bin/launcher.js'
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
test('ensureDshProfile writes the bundled profiles with the proxy port on first boot', () => {
  const home = mkdtempSync(path.join(os.tmpdir(), 'snoopy-profile-'))
  try {
    ensureDshProfile(home, 8307)
    for (const name of ['web', 'headless']) {
      const patch = readFileSync(path.join(home, 'profiles', name, 'cordis.patch.yml'), 'utf8')
      assert.match(patch, /baseURL: http:\/\/127\.0\.0\.1:8307\/v1/)
      assert.doesNotMatch(patch, /\{\{PROXY_PORT\}\}/)
    }
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('ensureDshProfile retargets an existing profile at the new port and keeps user edits', () => {
  const home = mkdtempSync(path.join(os.tmpdir(), 'snoopy-profile-'))
  try {
    ensureDshProfile(home, 8307)
    const patch = path.join(home, 'profiles', 'web', 'cordis.patch.yml')
    writeFileSync(patch, readFileSync(patch, 'utf8') + '# user edit\n')
    ensureDshProfile(home, 8312)
    const updated = readFileSync(patch, 'utf8')
    assert.match(updated, /baseURL: http:\/\/127\.0\.0\.1:8312\/v1/)
    assert.doesNotMatch(updated, /:8307\//)
    assert.match(updated, /# user edit\n$/)
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})
