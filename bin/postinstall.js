#!/usr/bin/env node
// npm postinstall hook: pre-download the proxy binary for this platform so the
// first `snoopy` run is network-free. Best effort: any failure
// falls back to the launcher's runtime download. Runs on linux/darwin only
// (the Go proxy ships for those platforms).

import { existsSync } from 'node:fs'

import { PROXY_BIN, downloadProxyBinary } from './launcher.js'

if (process.platform !== 'linux' && process.platform !== 'darwin') {
  console.warn('[snoopy postinstall] platform not supported (linux/darwin only); skipping proxy download')
  process.exit(0)
}
if (existsSync(PROXY_BIN)) {
  process.exit(0)
}
try {
  await downloadProxyBinary()
  console.log('[snoopy postinstall] proxy binary ready')
} catch (err) {
  console.warn('[snoopy postinstall] ignored: ' + (err instanceof Error ? err.message : err))
  console.warn('[snoopy postinstall] the launcher will retry at first run (or: npx snoopy download)')
  process.exit(0) // never fail the install
}