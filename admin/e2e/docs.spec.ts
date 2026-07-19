import { test, expect } from './fixtures'

// Docs are a routed layout: the TOC switches sections via child routes.
test('docs: routed sections navigate', async ({ page }) => {
  await page.goto('/docs')
  await expect(page.getByRole('heading', { level: 1, name: 'Documentation' })).toBeVisible()
  // Index shows the About section.
  await expect(page.getByText(/hosted, multi-tenant orchestration/)).toBeVisible()

  // TOC link to the API reference changes the route and content.
  await page.getByRole('link', { name: 'API reference' }).click()
  await expect(page).toHaveURL(/\/docs\/api$/)
  await expect(page.getByText(/endpoint requires an API key/)).toBeVisible()

  // The Images & presets section is reachable too.
  await page.getByRole('link', { name: 'Images & presets' }).click()
  await expect(page).toHaveURL(/\/docs\/images$/)
  await expect(page.getByText(/a Docker image tag/)).toBeVisible()
})
