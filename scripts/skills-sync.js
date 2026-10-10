#!/usr/bin/env node
// Materializes skills/catalog.json into skills/bundled/ (`just skills-sync`):
// fetches each source at its pinned commit or version, copies the selected
// skills, rewrites superpowers:<name> references to the bare names dsh uses,
// validates the result, and writes skills/LICENSES/ and skills/NOTICE.md.
// Any failure leaves the committed tree untouched. See
// docs/superpowers/specs/2026-10-11-bundled-skills-design.md (git-ignored).

import { spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * The directory name a catalog entry gets in skills/bundled/.
 * @param {string} entry - a path under the source's `path`, e.g. engineering/research.
 * @returns {string}
 */
export function bundledName(entry) {
  return entry.split('/').filter(Boolean).at(-1)
}

/**
 * Rewrites superpowers:<name> to <name> when <name> is bundled; citations of
 * skills left out on purpose stay as they are; anything else is reported.
 * @param {string} text
 * @param {Set<string>} known - bundled skill names.
 * @param {Set<string>} cited - names cited but not bundled.
 * @returns {{ text: string, unknown: string[] }}
 */
export function rewriteSkillReferences(text, known, cited) {
  const unknown = new Set()
  const out = text.replace(/superpowers:([a-z0-9-]+)/g, (match, name) => {
    if (known.has(name)) return name
    if (!cited.has(name)) unknown.add(name)
    return match
  })
  return { text: out, unknown: [...unknown] }
}

/**
 * The name and description of a SKILL.md's YAML frontmatter (single-line
 * values, optionally quoted), or null without frontmatter.
 * @param {string} text
 * @returns {{ name?: string, description?: string } | null}
 */
export function parseFrontmatter(text) {
  const match = /^---\r?\n([\s\S]*?)\r?\n---/.exec(text)
  if (!match) return null
  const field = key => {
    const line = match[1].split(/\r?\n/).find(l => l.startsWith(`${key}:`))
    return line === undefined ? undefined : line.slice(key.length + 1).trim().replace(/^(['"])(.*)\1$/, '$2')
  }
  return { name: field('name'), description: field('description') }
}

/** Every file under dir, recursively. */
function listFiles(dir) {
  return readdirSync(dir, { recursive: true }).map(f => path.join(dir, String(f))).filter(f => statSync(f).isFile())
}

/**
 * Copies the selected skills of already fetched sources into outDir, which is
 * replaced only when every skill passed: a failure throws and leaves it as it
 * was.
 * @param {{ id: string, root: string, skills: string[], citedNotBundled?: string[] }[]} sources
 * @param {string} outDir
 */
export function assembleSkills(sources, outDir) {
  const names = sources.flatMap(s => s.skills.map(bundledName))
  const duplicate = names.find((n, i) => names.indexOf(n) !== i)
  if (duplicate !== undefined) throw new Error(`duplicate skill name: ${duplicate}`)
  const known = new Set(names)
  const staging = `${outDir}.staging-${process.pid}`
  rmSync(staging, { recursive: true, force: true })
  try {
    for (const source of sources) {
      const cited = new Set(source.citedNotBundled ?? [])
      for (const entry of source.skills) {
        const name = bundledName(entry)
        const from = path.join(source.root, entry)
        if (!existsSync(path.join(from, 'SKILL.md'))) throw new Error(`${source.id}: ${entry}/SKILL.md not found`)
        const to = path.join(staging, name)
        // agents/ holds other harnesses' metadata (Codex's openai.yaml).
        cpSync(from, to, { recursive: true, filter: src => src !== path.join(from, 'agents') })
        for (const file of listFiles(to)) {
          const original = readFileSync(file, 'utf8')
          const { text, unknown } = rewriteSkillReferences(original, known, cited)
          if (unknown.length > 0) throw new Error(`${source.id}/${entry}: references skills outside the catalog: ${unknown.join(', ')}`)
          if (text !== original) writeFileSync(file, text)
        }
        const front = parseFrontmatter(readFileSync(path.join(to, 'SKILL.md'), 'utf8'))
        if (front?.name !== name || !front?.description) {
          throw new Error(`${source.id}/${entry}: SKILL.md frontmatter needs name: ${name} and a description`)
        }
      }
    }
    rmSync(outDir, { recursive: true, force: true })
    mkdirSync(path.dirname(outDir), { recursive: true })
    renameSync(staging, outDir)
  } finally {
    rmSync(staging, { recursive: true, force: true })
  }
}

/**
 * skills/NOTICE.md for a catalog.
 * @param {{ sources: object[] }} catalog
 * @returns {string}
 */
export function renderNotice(catalog) {
  const rows = catalog.sources.map(s => {
    const link = s.git ? `[${s.id}](${s.git})` : `[${s.npm}](https://www.npmjs.com/package/${s.npm})`
    const pin = s.git ? `${s.ref} (\`${s.commit.slice(0, 7)}\`)` : s.version
    return `| ${link} | ${pin} | ${s.license}, \`LICENSES/${s.id}.txt\` | ${s.skills.map(bundledName).join(', ')} |`
  })
  return [
    '# Bundled skills',
    '',
    'Generated by `just skills-sync` from `catalog.json`; do not edit. Each skill is',
    'copied unchanged from its source, except that `superpowers:<name>` references',
    'to bundled skills are rewritten to `<name>`.',
    '',
    '| Source | Version | Licence | Skills |',
    '| --- | --- | --- | --- |',
    ...rows,
    '',
  ].join('\n')
}

/** Runs a command, throwing with its stderr on failure. */
function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, encoding: 'utf8' })
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} failed: ${result.stderr || result.error}`)
  return result.stdout
}

/** Fetches one source into work/<id>; returns its checkout root and licence text. */
function fetchSource(source, work) {
  const dir = path.join(work, source.id)
  if (source.git) {
    run('git', ['clone', '--quiet', '--depth', '1', '--branch', source.ref, source.git, dir])
    const head = run('git', ['-C', dir, 'rev-parse', 'HEAD']).trim()
    if (head !== source.commit) throw new Error(`${source.id}: ${source.ref} is ${head}, the catalog pins ${source.commit}`)
    return { root: dir, license: readFileSync(path.join(dir, 'LICENSE'), 'utf8') }
  }
  const spec = `${source.npm}@${source.version}`
  mkdirSync(dir, { recursive: true })
  run('git', ['init', '--quiet'], dir)
  run('npx', ['--yes', spec, ...source.generate], dir)
  const tarball = run('npm', ['pack', '--quiet', spec, '--pack-destination', work], work).trim().split('\n').at(-1)
  const unpacked = path.join(work, `${source.id}-package`)
  mkdirSync(unpacked)
  run('tar', ['-xzf', path.join(work, tarball), '-C', unpacked])
  return { root: dir, license: readFileSync(path.join(unpacked, 'package', 'LICENSE'), 'utf8') }
}

function main() {
  const skillsDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'skills')
  const catalog = JSON.parse(readFileSync(path.join(skillsDir, 'catalog.json'), 'utf8'))
  const work = mkdtempSync(path.join(os.tmpdir(), 'snoopy-skills-sync-'))
  try {
    const fetched = catalog.sources.map(source => ({ source, ...fetchSource(source, work) }))
    assembleSkills(fetched.map(({ source, root }) => ({
      id: source.id,
      root: path.join(root, source.path),
      skills: source.skills,
      citedNotBundled: source.citedNotBundled,
    })), path.join(skillsDir, 'bundled'))
    rmSync(path.join(skillsDir, 'LICENSES'), { recursive: true, force: true })
    mkdirSync(path.join(skillsDir, 'LICENSES'))
    for (const { source, license } of fetched) writeFileSync(path.join(skillsDir, 'LICENSES', `${source.id}.txt`), license)
    writeFileSync(path.join(skillsDir, 'NOTICE.md'), renderNotice(catalog))
    console.error(`skills-sync: ${fetched.reduce((n, f) => n + f.source.skills.length, 0)} skills in skills/bundled/`)
  } finally {
    rmSync(work, { recursive: true, force: true })
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main()
  } catch (err) {
    console.error(`skills-sync: ${err instanceof Error ? err.message : err}`)
    process.exit(1)
  }
}
