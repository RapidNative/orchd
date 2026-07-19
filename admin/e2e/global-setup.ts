import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { API_KEY, API_PORT } from '../playwright.config'

const ORCHESTRATOR = join(import.meta.dirname, '..', '..', 'orchestrator')

// Boot a real orchd with the in-memory mock driver (no Docker), then hand a
// teardown back to Playwright to kill it. This is "mock Docker": the control
// plane is real; only the substrate is faked.
export default async function globalSetup() {
  const tmp = mkdtempSync(join(tmpdir(), 'tbc-e2e-'))
  const bin = join(tmp, 'orchd')
  const keyFile = join(tmp, 'api.key')
  writeFileSync(keyFile, API_KEY)

  // Build once so the child is a plain binary we can signal cleanly.
  execFileSync('go', ['build', '-o', bin, './cmd/orchd'], { cwd: ORCHESTRATOR, stdio: 'inherit' })

  const child: ChildProcess = spawn(bin, [], {
    env: {
      ...process.env,
      ORCHD_DRIVER: 'mock',
      ORCHD_API_ADDR: `127.0.0.1:${API_PORT}`,
      ORCHD_GATEWAY_ADDR: `127.0.0.1:${API_PORT + 1}`,
      ORCHD_API_KEY_FILE: keyFile,
      ORCHD_DATA_ROOT: join(tmp, 'data'),
      ORCHD_BASE_DOMAIN: 'test.local',
      ORCHD_BACKUP_INTERVAL: '0',
      ORCHD_METRICS_INTERVAL: '0',
      ORCHD_RATE_LIMIT: '0',
    },
    stdio: 'inherit',
  })

  await waitForHealth(`http://127.0.0.1:${API_PORT}/healthz`)

  return async () => {
    child.kill('SIGTERM')
  }
}

async function waitForHealth(url: string) {
  const deadline = Date.now() + 15_000
  for (;;) {
    try {
      const res = await fetch(url)
      if (res.ok) return
    } catch {
      /* not up yet */
    }
    if (Date.now() > deadline) throw new Error(`mock orchd did not become healthy at ${url}`)
    await new Promise((r) => setTimeout(r, 200))
  }
}
