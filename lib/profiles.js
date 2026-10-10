// dsh profile routing shared by the dev launcher (bin/harness.ts) and the
// published launcher (bin/launcher.js).
//
// Snoopy's routing (the `proxy` provider, the default model, the acp server's
// model) lives in profile/<name>.patch.yml inside the installed package and is
// layered onto dsh with `--patch` on every boot: an upgraded package brings its
// routing along, and the profile's own cordis.patch.yml stays the user's (and
// the agent's) layer. The proxy port reaches the patch through SNOOPY_PORT,
// the bundled pnpm (for plugin installs) through SNOOPY_PNPM.
//
// Self-improvement is opt-out: profile/self-improve.patch.yml turns on the
// agent-facing plugin_manager tool unless SNOOPY_SELF_IMPROVE says no. Every
// plugin_manager action still needs approval (Web: the user; acp: the
// orchestrator's session/request_permission; headless: refused unless
// DSH_PERMISSION_MODE=danger-full-access).

import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'

/** The dsh profiles snoopy routes through the proxy, one profile/<name>.patch.yml each. */
export const PROXY_PROFILES = ['web', 'headless', 'acp']

/**
 * Whether self-improvement stays on: anything but SNOOPY_SELF_IMPROVE=0, false,
 * off or no (case-insensitive).
 * @param {NodeJS.ProcessEnv} env
 * @returns {boolean}
 */
export function selfImproveEnabled(env) {
  return !['0', 'false', 'off', 'no'].includes(String(env.SNOOPY_SELF_IMPROVE ?? '').trim().toLowerCase())
}

/**
 * The bundled pnpm's executable entry point (dsh's plugin manager runs it to
 * install bundles), or undefined when it cannot be resolved.
 * @param {NodeJS.Require} require - a require rooted where pnpm is a dependency.
 * @returns {string | undefined}
 */
export function resolvePnpm(require) {
  // pnpm's `exports` hide its package.json from require.resolve: walk the
  // module lookup directories instead.
  for (const dir of require.resolve.paths('pnpm') ?? []) {
    const manifestPath = path.join(dir, 'pnpm', 'package.json')
    if (!existsSync(manifestPath)) continue
    try {
      const { bin } = JSON.parse(readFileSync(manifestPath, 'utf8'))
      return path.join(path.dirname(manifestPath), typeof bin === 'string' ? bin : bin.pnpm)
    } catch {
      return undefined
    }
  }
  return undefined
}

/**
 * Insert `--patch <profileDir>/<name>.patch.yml` (then the self-improvement
 * patch, unless opted out) right after the profile name when argv boots a
 * proxy-routed profile (`<name> …`, `--profile <name> …` or
 * `--profile=<name> …`); any other invocation (plugin, --help, other profiles)
 * is returned unchanged. dsh applies --patch overlays after the profile layer.
 * @param {string[]} argv - the dsh arguments snoopy received.
 * @param {string} profileDir - the directory holding the routing patches.
 * @param {{ selfImprove: boolean }} options
 * @returns {string[]}
 */
export function proxyOverlayArgs(argv, profileDir, { selfImprove }) {
  const patch = name => ['--patch', path.join(profileDir, `${name}.patch.yml`)]
  const overlay = name => [...patch(name), ...(selfImprove ? patch('self-improve') : [])]
  const insertAfter = (index, name) =>
    PROXY_PROFILES.includes(name) ? [...argv.slice(0, index + 1), ...overlay(name), ...argv.slice(index + 1)] : argv
  const first = argv[0]
  if (first === undefined || first === 'plugin') return argv
  if (!first.startsWith('-')) return insertAfter(0, first)
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--profile' && i + 1 < argv.length) return insertAfter(i + 1, argv[i + 1])
    if (argv[i].startsWith('--profile=')) return insertAfter(i, argv[i].slice('--profile='.length))
  }
  return argv
}

/**
 * The environment the routing and self-improvement patches read: the proxy
 * port, the bundled pnpm, snoopy's packaged skills, the directory where agents
 * author their own plugins ($DSH_HOME/plugins: next to the profiles, never in
 * the workspace or a repository), and a PATH that starts with the Node chosen
 * for dsh (pnpm is a `#!/usr/bin/env node` script and needs Node >= 22.13; the
 * first `node` on PATH could be an older or Nix build).
 * @param {NodeJS.ProcessEnv} env - the environment dsh would otherwise get.
 * @param {{ port: number, nodePath: string, pnpm: string | undefined, dshHome: string, profileDir: string }} options
 * @returns {Record<string, string>}
 */
export function overlayEnv(env, { port, nodePath, pnpm, dshHome, profileDir }) {
  return {
    SNOOPY_PORT: String(port),
    PATH: [path.dirname(nodePath), env.PATH].filter(Boolean).join(path.delimiter),
    ...(pnpm === undefined ? {} : { SNOOPY_PNPM: pnpm }),
    SNOOPY_SKILLS_DIR: path.join(profileDir, 'skills'),
    SNOOPY_PLUGINS_DIR: path.join(dshHome, 'plugins'),
  }
}
