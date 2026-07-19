import { test, expect } from './fixtures'

// Every sidebar destination renders its page.
test('sidebar navigates across all sections', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Dashboard' })).toBeVisible()

  const dests: [string, string][] = [
    ['Projects', 'Projects'],
    ['Images', 'Images'],
    ['Backups', 'Backups'],
    ['Activity', 'Activity'],
    ['System', 'System'],
    ['Settings', 'Settings'],
    ['Docs', 'Documentation'],
  ]
  for (const [link, heading] of dests) {
    await page.getByRole('link', { name: link, exact: true }).click()
    await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible()
  }
})
