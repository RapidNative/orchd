import { test as base } from '@playwright/test'
import { API_KEY } from '../playwright.config'

// `test` from here starts already authenticated: it seeds the API key into
// localStorage before any app script runs, so the key-gate is bypassed. Use the
// plain @playwright/test `test` when you want to exercise the gate itself.
export const test = base.extend({
  page: async ({ page }, use) => {
    await page.addInitScript((key) => {
      window.localStorage.setItem('rnc_admin_key', key as string)
    }, API_KEY)
    await use(page)
  },
})

export { expect } from '@playwright/test'
