// dsh profile routing shared by the dev launcher (bin/harness.ts) and the
// published launcher (bin/launcher.js).
//
// Snoopy's routing (the `proxy` provider, the default model, the acp server's
// model) lives in profile/<name>.patch.yml inside the installed package and is
// layered onto dsh with `--patch` on every boot: an upgraded package brings its
// routing along, and the profile's own cordis.patch.yml stays the user's (and
// the agent's) layer. The proxy port reaches the patch through SNOOPY_PORT.

import path from 'node:path'

/** The dsh profiles snoopy routes through the proxy, one profile/<name>.patch.yml each. */
export const PROXY_PROFILES = ['web', 'headless', 'acp']

/**
 * Insert `--patch <profileDir>/<name>.patch.yml` right after the profile name
 * when argv boots a proxy-routed profile (`<name> …`, `--profile <name> …` or
 * `--profile=<name> …`); any other invocation (plugin, --help, other profiles)
 * is returned unchanged. dsh applies --patch overlays after the profile layer.
 * @param {string[]} argv - the dsh arguments snoopy received.
 * @param {string} profileDir - the directory holding the routing patches.
 * @returns {string[]}
 */
export function proxyOverlayArgs(argv, profileDir) {
  const overlay = name => ['--patch', path.join(profileDir, `${name}.patch.yml`)]
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
