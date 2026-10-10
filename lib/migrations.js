// Versioned migrations of the snoopy-managed dsh home (schema-evolution
// style): an ordered list of steps, each applied once per dsh home and
// recorded in $DSH_HOME/snoopy-migrations.json.
//
// Rules for a migration:
// - Append only: never edit or reorder a released migration; add a new one.
// - Respect user edits: change a file only when its content is recognizably
//   what snoopy wrote, back it up when user content is kept around, and leave
//   anything else untouched.
// - Idempotent: several snoopy processes (an agent fleet) can boot on the same
//   home at once, so a step may run twice; writes go through a rename.

import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const STATE_FILE = 'snoopy-migrations.json'

/** dsh's own initial user layer (dsh-app-boot PROFILE_PATCH_TEMPLATE). */
export const EMPTY_PROFILE_PATCH = `# Your patch layer for this dsh profile, applied after every bundle layer:
# a top-level YAML array of loader patch entries (id-targeted config
# overrides, disables, and insert lists; \`!!js\` expressions allowed).
[]
`

/**
 * The routing templates snoopy 0.1.x copied into profiles/<name>/cordis.patch.yml
 * (frozen: what is on users' disks, {{PROXY_PORT}} then replaced by a port).
 */
export const LEGACY_PROFILE_PATCHES = {
  web: `# Bundled web profile for @s3p1r0o/snoopy: the embedded proxy is
# the only provider route and the sessions' default model. {{PROXY_PORT}} is
# substituted by the launcher at first boot.
- id: llm-pi-ai
  config:
    providers:
      proxy:
        apiKeyEnv: SNOOPY_PROXY_KEY
        api: openai-completions
        baseURL: http://127.0.0.1:{{PROXY_PORT}}/v1
        models:
          - id: rill-assistant
            name: rill-assistant (Rill via the embedded proxy)
            contextWindow: 272000
            maxTokens: 32768
            input: [text]

- id: agent-default-model
  config:
    provider: proxy
    model: rill-assistant`,
  headless: `# Bundled headless profile for @s3p1r0o/snoopy: same routing as
# the web profile; {{PROXY_PORT}} is substituted by the launcher at first boot.
- id: llm-pi-ai
  config:
    providers:
      proxy:
        apiKeyEnv: SNOOPY_PROXY_KEY
        api: openai-completions
        baseURL: http://127.0.0.1:{{PROXY_PORT}}/v1
        models:
          - id: rill-assistant
            name: rill-assistant (Rill via the embedded proxy)
            contextWindow: 272000
            maxTokens: 32768
            input: [text]

- id: agent-default-model
  config:
    provider: proxy
    model: rill-assistant`,
}

/**
 * Match a legacy template with whatever port the launcher substituted (0.1.x
 * used replaceAll, so the placeholder in the header comment got it too).
 */
function legacyPattern(template) {
  const parts = template.split('{{PROXY_PORT}}').map(part => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
  return new RegExp(parts.join('\\d+'))
}

function writeAtomic(file, text) {
  const tmp = `${file}.${process.pid}.tmp`
  writeFileSync(tmp, text)
  renameSync(tmp, file)
}

/**
 * The migrations, in order. `run(dshHome, log)` must follow the rules above.
 * @type {{ id: string, description: string, run: (dshHome: string, log: (msg: string) => void) => void }[]}
 */
export const MIGRATIONS = [
  {
    id: '2026-10-10-routing-overlay',
    description: 'routing moved to a --patch overlay from the package: drop the 0.1.x copies from the user layer',
    run(dshHome, log) {
      for (const [name, template] of Object.entries(LEGACY_PROFILE_PATCHES)) {
        const file = path.join(dshHome, 'profiles', name, 'cordis.patch.yml')
        if (!existsSync(file)) continue
        const text = readFileSync(file, 'utf8')
        const match = legacyPattern(template).exec(text)
        if (!match) continue // the user changed snoopy's rows: theirs now, the overlay still wins
        const rest = (text.slice(0, match.index) + text.slice(match.index + match[0].length)).trim()
        if (rest === '') {
          writeAtomic(file, EMPTY_PROFILE_PATCH)
          log(`profile ${name}: routing now comes from the package; emptied the old copy`)
        } else {
          // dsh rejects a user layer that is not a top-level array: comments
          // alone parse as null, so give them an empty list to annotate.
          const commentsOnly = rest.split('\n').every(line => line.trim() === '' || line.trim().startsWith('#'))
          writeAtomic(`${file}.pre-routing-overlay`, text)
          writeAtomic(file, rest + (commentsOnly ? '\n[]\n' : '\n'))
          log(`profile ${name}: removed the old routing copy, kept your entries (backup: ${file}.pre-routing-overlay)`)
        }
      }
    },
  },
]

function readState(dshHome) {
  try {
    return JSON.parse(readFileSync(path.join(dshHome, STATE_FILE), 'utf8'))
  } catch {
    return { applied: [] }
  }
}

/**
 * Apply the pending migrations to dshHome, in order, recording each one; stop
 * at the first failure (reported through log) so later steps never run on a
 * half-migrated home.
 * @param {string} dshHome
 * @param {(msg: string) => void} log
 * @returns {string[]} the ids applied by this call.
 */
export function migrateDshHome(dshHome, log) {
  mkdirSync(dshHome, { recursive: true })
  const state = readState(dshHome)
  const done = new Set(state.applied.map(entry => entry.id))
  const applied = []
  for (const migration of MIGRATIONS) {
    if (done.has(migration.id)) continue
    try {
      migration.run(dshHome, log)
    } catch (err) {
      log(`migration ${migration.id} failed (${err instanceof Error ? err.message : err}); will retry next boot`)
      break
    }
    state.applied.push({ id: migration.id, at: new Date().toISOString() })
    writeAtomic(path.join(dshHome, STATE_FILE), JSON.stringify(state, null, 2) + '\n')
    applied.push(migration.id)
  }
  return applied
}
