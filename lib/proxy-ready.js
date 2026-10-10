// Readiness of the proxy a launcher just spawned, shared by the dev launcher
// (bin/harness.ts) and the published one (bin/launcher.js).
//
// /healthz is unauthenticated, so any proxy on the port answers it: with a
// fixed port already taken (another snoopy running), a launcher would declare
// someone else's proxy ready and dsh would then fail with a bare 401. Readiness
// therefore means "this launch's key is accepted on /v1/models", and our own
// proxy exiting first fails at once instead of after the timeout.

import net from 'node:net'

/**
 * Whether nothing listens on address:port yet.
 * @param {number} port
 * @param {string} address
 * @returns {Promise<boolean>}
 */
export function portIsFree(port, address) {
  return new Promise(resolve => {
    const server = net.createServer()
    server.once('error', () => resolve(false))
    server.once('listening', () => server.close(() => resolve(true)))
    server.listen(port, address)
  })
}

/**
 * Wait until the proxy on 127.0.0.1:port accepts `key`.
 * @param {number} port
 * @param {string} key - the bearer key this launch gave its proxy.
 * @param {import('node:child_process').ChildProcess} child - that proxy.
 * @param {{ timeoutMs?: number, intervalMs?: number }} [options]
 * @returns {Promise<void>}
 * @throws when another proxy holds the port, ours exits, or the timeout passes.
 */
export async function waitProxyReady(port, key, child, { timeoutMs = 10_000, intervalMs = 200 } = {}) {
  let exited = child.exitCode !== null ? `exit code ${child.exitCode}` : undefined
  const onExit = (code, signal) => { exited = signal ?? `exit code ${code}` }
  child.once('exit', onExit)
  try {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      if (exited !== undefined) {
        throw new Error(`proxy exited (${exited}) before it was ready; is port ${port} already in use? Set SNOOPY_PORT to another port`)
      }
      let status
      try {
        const res = await fetch(`http://127.0.0.1:${port}/v1/models`, { headers: { authorization: `Bearer ${key}` } })
        status = res.status
      } catch {}
      if (status === 200 && exited === undefined) return
      if (status === 401) {
        throw new Error(`port ${port} is served by another proxy (it rejects this launch's key): is another snoopy running? Stop it or set SNOOPY_PORT`)
      }
      await new Promise(resolve => setTimeout(resolve, intervalMs))
    }
    throw new Error(`proxy on :${port} did not become ready within ${timeoutMs / 1000}s`)
  } finally {
    child.off('exit', onExit)
  }
}
