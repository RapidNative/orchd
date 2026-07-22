import { test, expect } from './fixtures'

// Registering a template in Settings lists it (the create-from-template source).
test('settings: register a template', async ({ page }) => {
  await page.goto('/settings')
  await expect(page.getByText('Templates', { exact: true })).toBeVisible()

  await page.getByPlaceholder('name (e.g. rapidnative)').fill('demo-tmpl')
  await page.getByPlaceholder(/absolute\/path/).fill('/tmp/demo-template')
  await page.getByRole('button', { name: 'Add template' }).click()

  await expect(page.getByText('demo-tmpl')).toBeVisible()
  await expect(page.getByText('/tmp/demo-template')).toBeVisible()
})
