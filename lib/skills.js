// The skills snoopy bundles for its agents (skills/catalog.json, materialized
// into skills/bundled/ by `just skills-sync`, see scripts/skills-sync.js).
//
// The launchers point dsh at them through DSH_BUNDLED_SKILL_DIR, which every
// dsh-skill-filesystem provider scans at the lowest rank: a project or user
// skill with the same name wins. The OpenSpec skills call the `openspec` CLI,
// so a shim running the bundled one goes first on dsh's PATH, from
// $DSH_HOME/bin (a dedicated directory rather than node_modules/.bin, which
// would hand the agent every package binary). SNOOPY_SKILLS=0 turns both off;
// an already set DSH_BUNDLED_SKILL_DIR wins and snoopy adds nothing.

import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'

const require = createRequire(import.meta.url)

/**
 * What the launcher should do about skills: 'explicit' (DSH_BUNDLED_SKILL_DIR
 * is already set), 'off' (SNOOPY_SKILLS turned the bundled skills off), or
 * 'default'.
 * @param {NodeJS.ProcessEnv} env
 * @returns {'explicit' | 'off' | 'default'}
 */
export function skillsMode(env) {
  if (String(env.DSH_BUNDLED_SKILL_DIR ?? '').trim() !== '') return 'explicit'
  return ['0', 'false', 'off', 'no'].includes(String(env.SNOOPY_SKILLS ?? '').trim().toLowerCase()) ? 'off' : 'default'
}

/**
 * The bundled OpenSpec CLI's entry script, or undefined when the package is
 * missing.
 * @returns {string | undefined}
 */
export function resolveOpenspec() {
  // OpenSpec's `exports` hide its package.json from require.resolve: walk the
  // module lookup directories instead (as resolvePnpm does).
  for (const dir of require.resolve.paths('@fission-ai/openspec') ?? []) {
    const manifestPath = path.join(dir, '@fission-ai', 'openspec', 'package.json')
    if (!existsSync(manifestPath)) continue
    try {
      const { bin } = JSON.parse(readFileSync(manifestPath, 'utf8'))
      return path.join(path.dirname(manifestPath), typeof bin === 'string' ? bin : bin.openspec)
    } catch {
      return undefined
    }
  }
  return undefined
}

/** Single-quotes a string for sh. */
function shQuote(value) {
  return `'${String(value).replaceAll("'", `'\\''`)}'`
}

/**
 * The shim that runs the OpenSpec CLI with the Node chosen for dsh.
 * @param {string} nodePath
 * @param {string} entry
 * @returns {string}
 */
export function openspecShim(nodePath, entry) {
  return [
    '#!/bin/sh',
    '# Written by snoopy at every launch: the OpenSpec CLI its bundled skills call.',
    '# OpenSpec sends usage telemetry and checks npm for updates by default: both',
    '# off unless set otherwise (OPENSPEC_TELEMETRY=1 turns telemetry back on).',
    'export OPENSPEC_TELEMETRY="${OPENSPEC_TELEMETRY:-0}"',
    'export OPENSPEC_NO_UPDATE_CHECK="${OPENSPEC_NO_UPDATE_CHECK:-1}"',
    `exec ${shQuote(nodePath)} ${shQuote(entry)} "$@"`,
    '',
  ].join('\n')
}

/**
 * Writes <binDir>/openspec (executable, atomically), unless it already holds
 * the same content.
 * @param {string} binDir
 * @param {string} nodePath
 * @param {string} entry
 * @returns {'written' | 'unchanged'}
 */
export function writeOpenspecShim(binDir, nodePath, entry) {
  const target = path.join(binDir, 'openspec')
  const content = openspecShim(nodePath, entry)
  if (existsSync(target) && readFileSync(target, 'utf8') === content) return 'unchanged'
  mkdirSync(binDir, { recursive: true })
  const tmp = path.join(binDir, `.openspec.tmp-${process.pid}`)
  writeFileSync(tmp, content)
  chmodSync(tmp, 0o755)
  renameSync(tmp, target)
  return 'written'
}

/**
 * The environment the launchers add for dsh, spread after overlayEnv():
 * DSH_BUNDLED_SKILL_DIR, and PATH (the one overlayEnv produced) led by
 * $DSH_HOME/bin once the OpenSpec shim is written; {} when opted out or
 * overridden.
 * @param {NodeJS.ProcessEnv} env
 * @param {string} pkgRoot - the snoopy package (or repo) root.
 * @param {{ dshHome: string, nodePath: string, path: string | undefined, openspecEntry?: string | null }} options
 * @returns {Record<string, string>}
 */
export function skillsEnv(env, pkgRoot, { dshHome, nodePath, path: searchPath, openspecEntry = resolveOpenspec() }) {
  if (skillsMode(env) !== 'default') return {}
  const out = { DSH_BUNDLED_SKILL_DIR: path.join(pkgRoot, 'skills', 'bundled') }
  if (openspecEntry != null) {
    // Absolute: a relative DSH_HOME would put a cwd-relative directory first on
    // PATH, so any repo shipping .dsh/bin/<command> would shadow that command.
    const binDir = path.resolve(dshHome, 'bin')
    writeOpenspecShim(binDir, nodePath, openspecEntry)
    out.PATH = [binDir, searchPath].filter(Boolean).join(path.delimiter)
  }
  return out
}
