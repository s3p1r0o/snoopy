// Unit tests for the bundled skills: lib/skills.js and scripts/skills-sync.js.
// node --test scripts/skills.test.ts

import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { openspecShim, resolveOpenspec, skillsEnv, skillsMode, writeOpenspecShim } from '../lib/skills.js'
import { assembleSkills, bundledName, npxEnv, parseFrontmatter, renderNotice, rewriteSkillReferences } from './skills-sync.js'

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

const skillMd = (name: string, body = '') => `---\nname: ${name}\ndescription: "Use when testing ${name}"\n---\n${body}\n`

/** A fake upstream checkout: { 'a/SKILL.md': '...' } under a fresh dir. */
function fixture(files: Record<string, string>): string {
  const root = tmp()
  for (const [rel, content] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(root, rel)), { recursive: true })
    writeFileSync(path.join(root, rel), content)
  }
  return root
}

test('bundledName keeps the last path segment', () => {
  assert.equal(bundledName('engineering/domain-modeling'), 'domain-modeling')
  assert.equal(bundledName('brainstorming'), 'brainstorming')
})

test('rewriteSkillReferences strips the superpowers prefix for bundled skills only', () => {
  const known = new Set(['writing-plans', 'test-driven-development'])
  const cited = new Set(['writing-skills'])
  assert.deepEqual(rewriteSkillReferences('Use superpowers:writing-plans, then superpowers:test-driven-development.', known, cited),
    { text: 'Use writing-plans, then test-driven-development.', unknown: [] })
  assert.deepEqual(rewriteSkillReferences('see (superpowers:writing-skills)', known, cited),
    { text: 'see (superpowers:writing-skills)', unknown: [] })
  assert.deepEqual(rewriteSkillReferences('superpowers:nope and superpowers:nope', known, cited).unknown, ['nope'])
})

test('parseFrontmatter reads name and description, quoted or not', () => {
  assert.deepEqual(parseFrontmatter(skillMd('x')), { name: 'x', description: 'Use when testing x' })
  assert.deepEqual(parseFrontmatter("---\nname: 'y'\ndescription: plain text\n---\n"), { name: 'y', description: 'plain text' })
  assert.equal(parseFrontmatter('no frontmatter'), null)
})

test('assembleSkills copies the selected skills, rewrites references and drops agents/', () => {
  const up = fixture({
    'skills/plan/SKILL.md': skillMd('plan', 'Then use superpowers:tdd.'),
    'skills/plan/scripts/run.sh': 'echo superpowers:tdd\n',
    'skills/tdd/SKILL.md': skillMd('tdd'),
    'skills/tdd/agents/openai.yaml': 'x: 1\n',
    'skills/unselected/SKILL.md': skillMd('unselected'),
  })
  const out = path.join(tmp(), 'bundled')
  try {
    assembleSkills([{ id: 'up', root: path.join(up, 'skills'), skills: ['plan', 'tdd'] }], out)
    assert.deepEqual(readdirSync(out).sort(), ['plan', 'tdd'])
    assert.match(readFileSync(path.join(out, 'plan', 'SKILL.md'), 'utf8'), /Then use tdd\./)
    assert.equal(readFileSync(path.join(out, 'plan', 'scripts', 'run.sh'), 'utf8'), 'echo tdd\n')
    assert.equal(existsSync(path.join(out, 'tdd', 'agents')), false)
  } finally {
    rmSync(up, { recursive: true, force: true })
    rmSync(path.dirname(out), { recursive: true, force: true })
  }
})

test('assembleSkills fails, leaving the output untouched, on any problem', () => {
  const cases: [string, Record<string, string>, string[], RegExp][] = [
    ['missing skill', { 'a/SKILL.md': skillMd('a') }, ['a', 'b'], /b\/SKILL\.md not found/],
    ['unknown reference', { 'a/SKILL.md': skillMd('a', 'superpowers:ghost') }, ['a'], /outside the catalog: ghost/],
    ['name mismatch', { 'a/SKILL.md': skillMd('other') }, ['a'], /name: a/],
    ['duplicate name', { 'x/a/SKILL.md': skillMd('a'), 'y/a/SKILL.md': skillMd('a') }, ['x/a', 'y/a'], /duplicate skill name: a/],
  ]
  for (const [label, files, skills, error] of cases) {
    const up = fixture(files)
    const out = path.join(tmp(), 'bundled')
    mkdirSync(path.join(out, 'previous'), { recursive: true })
    try {
      assert.throws(() => assembleSkills([{ id: 'up', root: up, skills }], out), error, label)
      assert.deepEqual(readdirSync(out), ['previous'], label)
    } finally {
      rmSync(up, { recursive: true, force: true })
      rmSync(path.dirname(out), { recursive: true, force: true })
    }
  }
})

test('renderNotice lists every source with its pin, licence file and skills', () => {
  const notice = renderNotice({ sources: [
    { id: 'superpowers', git: 'https://github.com/obra/superpowers', ref: 'v6.4.1', commit: '5bf4e78011075bcfc0dc295f0724994cd123ee71', license: 'MIT', skills: ['brainstorming'] },
    { id: 'openspec', npm: '@fission-ai/openspec', version: '1.14.1', license: 'MIT', skills: ['openspec-explore'] },
  ] })
  assert.match(notice, /\| \[superpowers\]\(https:\/\/github\.com\/obra\/superpowers\) \| v6\.4\.1 \(`5bf4e78`\) \| MIT, `LICENSES\/superpowers\.txt` \| brainstorming \|/)
  assert.match(notice, /\| \[@fission-ai\/openspec\]\(https:\/\/www\.npmjs\.com\/package\/@fission-ai\/openspec\) \| 1\.14\.1 \| MIT, `LICENSES\/openspec\.txt` \| openspec-explore \|/)
})

test('the committed skills tree matches the catalog and ships with the package and the image', () => {
  const catalog = JSON.parse(readFileSync(path.join(PKG_ROOT, 'skills', 'catalog.json'), 'utf8'))
  const names = catalog.sources.flatMap((s: { skills: string[] }) => s.skills.map(bundledName)).sort()
  assert.equal(names.length, 23)
  assert.deepEqual(readdirSync(BUNDLED).sort(), names)
  const cited = new Set(catalog.sources.flatMap((s: { citedNotBundled?: string[] }) => s.citedNotBundled ?? []))
  for (const name of names) {
    const front = parseFrontmatter(readFileSync(path.join(BUNDLED, name, 'SKILL.md'), 'utf8'))
    assert.equal(front?.name, name)
    assert.ok(front?.description, `${name} has a description`)
    for (const f of readdirSync(path.join(BUNDLED, name), { recursive: true })) {
      const file = path.join(BUNDLED, name, String(f))
      if (!statSync(file).isFile()) continue
      for (const [, ref] of readFileSync(file, 'utf8').matchAll(/superpowers:([a-z0-9-]+)/g)) {
        assert.ok(cited.has(ref), `${file} still references superpowers:${ref}`)
      }
    }
  }
  assert.equal(readFileSync(path.join(PKG_ROOT, 'skills', 'NOTICE.md'), 'utf8'), renderNotice(catalog))
  for (const s of catalog.sources) assert.ok(existsSync(path.join(PKG_ROOT, 'skills', 'LICENSES', `${s.id}.txt`)), s.id)
  const manifest = JSON.parse(readFileSync(path.join(PKG_ROOT, 'package.json'), 'utf8'))
  assert.ok(manifest.files.includes('skills/'), 'package.json files ships skills/')
  const openspec = catalog.sources.find((s: { npm?: string }) => s.npm === '@fission-ai/openspec')
  assert.equal(manifest.dependencies['@fission-ai/openspec'], openspec.version)
  assert.match(readFileSync(path.join(PKG_ROOT, 'Dockerfile'), 'utf8'), /^COPY skills\/ skills\/$/m)
})

test('the shim keeps the OpenSpec CLI quiet: telemetry and update checks off unless the user set them', () => {
  const dir = tmp()
  try {
    const probe = path.join(dir, 'probe.js')
    writeFileSync(probe, 'console.log(`${process.env.OPENSPEC_TELEMETRY} ${process.env.OPENSPEC_NO_UPDATE_CHECK}`)\n')
    writeOpenspecShim(path.join(dir, 'bin'), process.execPath, probe)
    const shim = path.join(dir, 'bin', 'openspec')
    const { OPENSPEC_TELEMETRY: _t, OPENSPEC_NO_UPDATE_CHECK: _u, ...base } = process.env
    assert.equal(spawnSync(shim, [], { encoding: 'utf8', env: base }).stdout.trim(), '0 1')
    assert.equal(spawnSync(shim, [], { encoding: 'utf8', env: { ...base, OPENSPEC_TELEMETRY: '1' } }).stdout.trim(), '1 1')
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('the sync runs OpenSpec with telemetry and update checks off', () => {
  const env = npxEnv({ PATH: '/usr/bin' })
  assert.equal(env.OPENSPEC_TELEMETRY, '0')
  assert.equal(env.OPENSPEC_NO_UPDATE_CHECK, '1')
  assert.equal(env.PATH, '/usr/bin')
})
