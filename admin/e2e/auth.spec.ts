import { test, expect } from '@playwright/test'
import { API_KEY } from '../playwright.config'

// Exercises the key-gate itself (unauthenticated), so it uses the plain test.
test('key gate rejects a bad key and accepts a good one', async ({ page }) => {
  await page.goto('/')

  // The gate is shown, not the app.
  await expect(page.getByText('ORCHD · Admin')).toBeVisible()
  const input = page.getByPlaceholder('API key')
  await expect(input).toBeVisible()

  // Wrong key -> inline error, still gated.
  await input.fill('wrong-key')
  await page.getByRole('button', { name: 'Connect' }).click()
  await expect(page.getByText('Invalid key')).toBeVisible()

  // Correct key -> app loads (sidebar brand + Dashboard heading).
  await input.fill(API_KEY)
  await page.getByRole('button', { name: 'Connect' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Dashboard' })).toBeVisible()
})
