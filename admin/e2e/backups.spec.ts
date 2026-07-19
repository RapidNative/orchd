import { test, expect } from './fixtures'

// The mock orchd defaults to a local (on-box) backup target, so the page should
// show the on-box durability banner and point at Settings — the target-aware
// banner, not the old hard-coded "S3/R2 is the next step" copy.
test('backups: shows target-aware on-box banner', async ({ page }) => {
  await page.goto('/backups')
  await expect(page.getByRole('heading', { level: 1, name: 'Backups' })).toBeVisible()

  await expect(page.getByText(/stored/)).toBeVisible()
  await expect(page.getByText(/Restoring replaces/)).toBeVisible()
  // The stale "next step" wording must be gone.
  await expect(page.getByText(/is the next step/)).toHaveCount(0)
})
