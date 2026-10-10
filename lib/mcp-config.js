// The MCP servers the launchers declare for the proxy by default (the proxy
// is the MCP client; --mcp-config / RILL_MCP_CONFIG, see proxy/mcp.go).
//
// Out of the box snoopy offers the agents one knowledge base: Wikipedia, over
// Streamable HTTP on a hosted instance of the open wikipedia-mcp-server
// (Apache-2.0, https://github.com/cyanheads/wikipedia-mcp-server), wrapping
// Wikipedia's public API. Its results reach the model marked as untrusted
// data. Operators who want other or self-hosted servers set RILL_MCP_CONFIG
// to their own file; SNOOPY_MCP=0 turns the default off.

import { existsSync, mkdirSync, renameSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'

/** The server name the default config's tools appear under: wiki__*. */
const DEFAULT_SERVER_NAME = 'wiki'

/** The Streamable HTTP endpoint the default config points at. */
const DEFAULT_SERVER_URL = 'https://wikipedia.caseyjhand.com/mcp'

export const DEFAULT_MCP_CONFIG = {
  mcpServers: {
    [DEFAULT_SERVER_NAME]: { type: 'http', url: DEFAULT_SERVER_URL },
  },
}

/**
 * What the launcher should do about MCP: 'explicit' (an RILL_MCP_CONFIG is
 * set: the proxy reads it itself, the launcher adds nothing), 'off'
 * (SNOOPY_MCP turned the default off), or 'default' (write the default config
 * and point the proxy at it).
 * @param {NodeJS.ProcessEnv} env
 * @returns {'explicit' | 'off' | 'default'}
 */
export function mcpConfigMode(env) {
  const explicit = String(env.RILL_MCP_CONFIG ?? '').trim()
  if (explicit !== '') return 'explicit'
  return ['0', 'false', 'off', 'no'].includes(String(env.SNOOPY_MCP ?? '').trim().toLowerCase()) ? 'off' : 'default'
}

/** The config file the launchers hand the proxy by default ($XDG_CONFIG_HOME/snoopy/mcp.json). */
function mcpConfigPath(env) {
  return path.join(env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config'), 'snoopy', 'mcp.json')
}

/**
 * The environment the launchers add for the proxy: { RILL_MCP_CONFIG: path }
 * with the default config written (once; an existing file is never
 * overwritten, user edits stand), or {} when MCP is off or configured
 * explicitly.
 * @param {NodeJS.ProcessEnv} env
 * @returns {Record<string, string>}
 */
export function mcpConfigEnv(env) {
  switch (mcpConfigMode(env)) {
    case 'explicit':
      return {}
    case 'off':
      return {}
    case 'default': {
      const configPath = mcpConfigPath(env)
      if (!existsSync(configPath)) {
        // Idempotent, several snoopy processes may boot at once: write to a
        // temporary file and rename, so a reader never sees a half file.
        const dir = path.dirname(configPath)
        mkdirSync(dir, { recursive: true })
        const tmp = path.join(dir, `.mcp.json.tmp-${process.pid}`)
        writeFileSync(tmp, JSON.stringify(DEFAULT_MCP_CONFIG, null, 2) + '\n')
        renameSync(tmp, configPath)
      }
      return { RILL_MCP_CONFIG: configPath }
    }
  }
}