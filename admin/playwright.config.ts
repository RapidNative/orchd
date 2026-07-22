import { defineConfig, devices } from '@playwright/test'

// Browser E2E: a real Chromium drives the built admin, which talks to a local
// mock orchd (ORCHD_DRIVER=mock — no Docker). global-setup boots orchd; the
// webServer runs Vite with its /api proxy pointed at that mock via
// E2E_API_TARGET. Fixed ports keep it hermetic.
// 819x, clear of the local dev range (dev/local.sh uses 8090-8092 + workloads
// from 8100), so a running local stack never clashes with the E2E harness.
export const API_PORT = 8190
export const WEB_PORT = 8192
export const API_KEY = 'e2e-admin-key'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  reporter: [['list']],
  globalSetup: './e2e/global-setup.ts',
  use: {
    baseURL: `http://127.0.0.1:${WEB_PORT}`,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `vite --host 127.0.0.1 --port ${WEB_PORT} --strictPort`,
    url: `http://127.0.0.1:${WEB_PORT}`,
    reuseExistingServer: false,
    timeout: 60_000,
    env: { E2E_API_TARGET: `http://127.0.0.1:${API_PORT}` },
  },
})
