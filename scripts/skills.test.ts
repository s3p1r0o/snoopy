// Unit tests for the bundled skills: lib/skills.js and scripts/skills-sync.js.
// node --test scripts/skills.test.ts

import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { openspecShim, resolveOpenspec, skillsEnv, skillsMode, writeOpenspecShim } from '../lib/skills.js'

const PKG_ROOT = path.join(import.meta.dirname, '..')
const BUNDLED = path.join(PKG_ROOT, 'skills', 'bundled')
const tmp = () => mkdtempSync(path.join(os.tmpdir(), 'snoopy-skills-'))

test('skillsMode: bundled skills on unless opted out; an explicit DSH_BUNDLED_SKILL_DIR wins', () => {
  assert.equal(skillsMode({}), 'default')
  assert.equal(skillsMode({ DSH_BUNDLED_SKILL_DIR: '/mine' }), 'explicit')
  assert.equal(skillsMode({ DSH_BUNDLED_SKILL_DIR: '/mine', SNOOPY_SKILLS: '0' }), 'explicit')
  assert.equal(skillsMode({ DSH_BUNDLED_SKILL_DIR: '  ' }), 'default')
  for (const off of ['0', 'false', 'off', 'no', ' OFF ']) assert.equal(skillsMode({ SNOOPY_SKILLS: off }), 'off', off)
  for (const on of ['1', 'true', 'yes', '']) assert.equal(skillsMode({ SNOOPY_SKILLS: on }), 'default', on)
})

test('skillsEnv points dsh at the bundled skills and puts the OpenSpec shim first on PATH', () => {
  const home = tmp()
  try {
    const options = { dshHome: home, nodePath: '/opt/node/bin/node', path: ['/opt/node/bin', '/usr/bin'].join(path.delimiter), openspecEntry: '/pkg/openspec/bin/openspec.js' }
    assert.deepEqual(skillsEnv({}, PKG_ROOT, options), {
      DSH_BUNDLED_SKILL_DIR: BUNDLED,
      PATH: [path.join(home, 'bin'), '/opt/node/bin', '/usr/bin'].join(path.delimiter),
    })
    assert.equal(readFileSync(path.join(home, 'bin', 'openspec'), 'utf8'), openspecShim('/opt/node/bin/node', '/pkg/openspec/bin/openspec.js'))
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('skillsEnv adds nothing and writes no shim when opted out or overridden', () => {
  const home = tmp()
  try {
    const options = { dshHome: home, nodePath: '/n', path: '/usr/bin', openspecEntry: '/e.js' }
    assert.deepEqual(skillsEnv({ SNOOPY_SKILLS: '0' }, PKG_ROOT, options), {})
    assert.deepEqual(skillsEnv({ DSH_BUNDLED_SKILL_DIR: '/mine' }, PKG_ROOT, options), {})
    assert.equal(existsSync(path.join(home, 'bin')), false)
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('skillsEnv keeps the skills but leaves PATH alone when the OpenSpec CLI is missing', () => {
  const home = tmp()
  try {
    assert.deepEqual(skillsEnv({}, PKG_ROOT, { dshHome: home, nodePath: '/n', path: '/usr/bin', openspecEntry: null }), { DSH_BUNDLED_SKILL_DIR: BUNDLED })
    assert.equal(existsSync(path.join(home, 'bin')), false)
  } finally {
    rmSync(home, { recursive: true, force: true })
  }
})

test('writeOpenspecShim writes an executable shim, then leaves it when unchanged', () => {
  const dir = tmp()
  try {
    const bin = path.join(dir, 'bin')
    assert.equal(writeOpenspecShim(bin, '/n', '/e.js'), 'written')
    assert.ok(statSync(path.join(bin, 'openspec')).mode & 0o111, 'executable')
    assert.equal(writeOpenspecShim(bin, '/n', '/e.js'), 'unchanged')
    assert.equal(writeOpenspecShim(bin, '/other/node', '/e.js'), 'written')
    assert.equal(readFileSync(path.join(bin, 'openspec'), 'utf8'), openspecShim('/other/node', '/e.js'))
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('openspecShim quotes paths with spaces and quotes for sh', () => {
  const shim = openspecShim('/a b/node', "/c'd/openspec.js")
  assert.ok(shim.startsWith('#!/bin/sh\n'))
  assert.ok(shim.includes(`exec '/a b/node' '/c'\\''d/openspec.js' "$@"\n`), shim)
  const check = spawnSync('sh', ['-n'], { input: shim })
  assert.equal(check.status, 0, String(check.stderr))
})

test('the shim runs the bundled OpenSpec CLI', () => {
  const entry = resolveOpenspec()
  assert.ok(entry && existsSync(entry), String(entry))
  const dir = tmp()
  try {
    writeOpenspecShim(dir, process.execPath, entry)
    const run = spawnSync(path.join(dir, 'openspec'), ['--version'], { encoding: 'utf8' })
    assert.equal(run.status, 0, run.stderr)
    assert.match(run.stdout, /^1\.14\.1\b/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})
