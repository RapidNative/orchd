import { test, expect } from './fixtures'

// Naming the instance in Settings updates the sidebar (fetched from /v1/info).
test('settings: naming the instance updates the sidebar', async ({ page }) => {
  await page.goto('/settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Settings' })).toBeVisible()

  // Sidebar always shows the generic engine name.
  await expect(page.getByText('ORCHD', { exact: true }).first()).toBeVisible()

  const input = page.getByPlaceholder('tinbase cloud')
  await input.fill('RapidNative Cloud')
  await page.getByRole('button', { name: 'Save' }).first().click()

  // The chosen name appears in the sidebar under ORCHD (exact, not the hint text).
  await expect(page.getByText('RapidNative Cloud', { exact: true })).toBeVisible()
})
