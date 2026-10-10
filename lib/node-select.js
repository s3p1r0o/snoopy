// Node-runtime selection shared by the dev launcher (bin/harness.ts) and the
// published single-file launcher (packages/harness/bin/launcher.js).
//
// DeepSeek Harness's native loader only recognizes official nodejs.org builds —
// Nix/NixOS-compiled Node fails its probe at boot (see
// github.com/deepseek-ai/deepseek-harness/discussions/690). Candidates are
// collected from DSH_NODE, fnm, nvm, then PATH, preferring official builds and
// the highest version within each group.

import { spawnSync } from 'node:child_process'
import { existsSync, readdirSync, realpathSync } from 'node:fs'
import { homedir } from 'node:os'
import path from 'node:path'

/**
 * @typedef {{ path: string, version: string, official: boolean }} NodeCandidate
 */

/** Extract a leading semver from the payload of `node -v`. */
export function parseNodeVersion(payload) {
  const m = /^v?(\d+\.\d+\.\d+)\b/.exec(String(payload ?? '').trim())
  return m ? m[1] : null
}

/**
 * Pick one binary among collected candidates: the highest version, with
 * official builds strongly preferred over distro/Nix builds.
 * @param {NodeCandidate[]} candidates
 * @returns {NodeCandidate | null}
 */
export function pickNode(candidates) {
  const ok = candidates.filter(c => c.path && c.version)
  if (ok.length === 0) return null
  const versionKey = c => c.version.split('.').map(Number)
  return ok.sort((a, b) => {
    if (a.official !== b.official) return a.official ? -1 : 1
    const [amaj, amin, apat] = versionKey(a)
    const [bmaj, bmin, bpat] = versionKey(b)
    return bmaj - amaj || bmin - amin || bpat - apat
  })[0]
}

/**
 * @param {NodeJS.ProcessEnv} [env]
 * @param {string} [home]
 * @returns {NodeCandidate[]}
 */
export function collectNodeCandidates(env = process.env, home = homedir()) {
  const out = []
  const seen = new Set()
  const push = p => {
    if (!p || seen.has(p)) return
    seen.add(p)
    if (!existsSync(p)) return
    const c = probeNode(p)
    if (c) out.push(c)
  }
  if (env.DSH_NODE) push(env.DSH_NODE)
  // fnm ("installation/bin/node") and nvm ("bin/node") layouts; .tooling/ caches.
  const roots = [
    [env.FNM_DIR ?? `${home}/.local/share/fnm`, 'node-versions'],
    [home, '.nvm/versions/node'],
    [home, '.tooling/node'],
  ]
  for (const [dir, leaf] of roots) {
    const versionsDir = path.join(dir, leaf)
    if (!existsSync(versionsDir)) continue
    for (const v of readdirSync(versionsDir).sort()) {
      push(`${versionsDir}/${v}/installation/bin/node`)
      push(`${versionsDir}/${v}/bin/node`)
    }
  }
  if (env.PATH) {
    for (const dir of env.PATH.split(':').filter(Boolean)) {
      push(path.join(dir, 'node'))
    }
  }
  return out
}

/** Run `node -v` on one candidate; realpath decides official-vs-Nix. */
function probeNode(p) {
  const info = spawnSync(p, ['-v'], { encoding: 'utf8' })
  const version = parseNodeVersion(info.stdout)
  if (!version) return null
  let official
  try {
    official = !realpathSync(p).includes('/nix/')
  } catch {
    official = false
  }
  return { path: p, version, official }
}