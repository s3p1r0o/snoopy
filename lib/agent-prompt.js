// The instructions snoopy gives its agents by default, as two texts shipped in
// lib/defaults/ (English, not tied to any technology):
//
// - the system prompt, facts about the environment (what "rill-assistant" is,
//   how the proxy's server-side tools behave), handed to the proxy as
//   RILL_SYSTEM_PROMPT so it reaches every profile (web, headless, acp);
// - the global AGENTS.md, working rules, written to $DSH_HOME/AGENTS.md, which
//   dsh's agent-instructions plugin loads before the project's own chain.
//
// snoopy owns that AGENTS.md: it is rewritten at every launch, so upgrades
// bring the new rules. A file without the managed marker is the user's and is
// backed up once before being replaced; SNOOPY_AGENTS_MD=0 leaves the file
// alone. Personal rules belong in each project's AGENTS.md.

import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const DEFAULTS_DIR = path.join(path.dirname(fileURLToPath(import.meta.url)), 'defaults')

/** The system prompt the launchers hand the proxy by default. */
export const DEFAULT_SYSTEM_PROMPT = readFileSync(path.join(DEFAULTS_DIR, 'system-prompt.md'), 'utf8').trim()

/** First line of a snoopy-managed AGENTS.md: a file without it is the user's. */
export const AGENTS_MD_MARKER = '<!-- Managed by snoopy: rewritten at every launch; SNOOPY_AGENTS_MD=0 keeps your own file -->'

/** The global AGENTS.md as written to $DSH_HOME. */
export const DEFAULT_AGENTS_MD = AGENTS_MD_MARKER + '\n' + readFileSync(path.join(DEFAULTS_DIR, 'AGENTS.md'), 'utf8')

function optedOut(value) {
  return ['0', 'false', 'off', 'no'].includes(String(value ?? '').trim().toLowerCase())
}

/**
 * The environment the launchers add for the proxy: { RILL_SYSTEM_PROMPT }
 * with the default prompt, or {} when an explicit RILL_SYSTEM_PROMPT is set
 * (the proxy reads it itself) or SNOOPY_SYSTEM_PROMPT turns the default off.
 * @param {NodeJS.ProcessEnv} env
 * @returns {Record<string, string>}
 */
export function systemPromptEnv(env) {
  if (String(env.RILL_SYSTEM_PROMPT ?? '').trim() !== '') return {}
  if (optedOut(env.SNOOPY_SYSTEM_PROMPT)) return {}
  return { RILL_SYSTEM_PROMPT: DEFAULT_SYSTEM_PROMPT }
}

/**
 * Writes the default global AGENTS.md into the dsh home, unless
 * SNOOPY_AGENTS_MD turns it off. Written atomically and only when the content
 * differs; an existing file without the managed marker is first renamed to
 * AGENTS.md.bak-<epoch ms>, with one log line naming the backup.
 * @param {string} dshHome
 * @param {NodeJS.ProcessEnv} env
 * @param {(msg: string) => void} log
 * @returns {{ action: 'off' | 'unchanged' | 'written', backup?: string }}
 */
export function installAgentsMd(dshHome, env, log) {
  if (optedOut(env.SNOOPY_AGENTS_MD)) return { action: 'off' }
  const target = path.join(dshHome, 'AGENTS.md')
  let backup
  if (existsSync(target)) {
    const current = readFileSync(target, 'utf8')
    if (current === DEFAULT_AGENTS_MD) return { action: 'unchanged' }
    if (!current.startsWith(AGENTS_MD_MARKER)) {
      backup = `${target}.bak-${Date.now()}`
      renameSync(target, backup)
      log(`${target} was not managed by snoopy: kept as ${backup} (SNOOPY_AGENTS_MD=0 leaves your own file in place)`)
    }
  }
  mkdirSync(dshHome, { recursive: true })
  const tmp = path.join(dshHome, `.AGENTS.md.tmp-${process.pid}`)
  writeFileSync(tmp, DEFAULT_AGENTS_MD)
  renameSync(tmp, target)
  return backup ? { action: 'written', backup } : { action: 'written' }
}
