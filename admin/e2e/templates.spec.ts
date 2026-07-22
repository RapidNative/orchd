import { test, expect } from './fixtures'

// Registering a template on its dedicated Templates page lists it (the
// create-from-template source, and what Images are built from).
test('templates: register a template', async ({ page }) => {
  await page.goto('/templates')
  await expect(page.getByRole('heading', { level: 1, name: 'Templates' })).toBeVisible()

  await page.getByPlaceholder('name (e.g. rapidnative)').fill('demo-tmpl')
  await page.getByPlaceholder(/absolute\/path/).fill('/tmp/demo-template')
  await page.getByRole('button', { name: 'Add template' }).click()

  await expect(page.getByText('demo-tmpl')).toBeVisible()
  await expect(page.getByText('/tmp/demo-template')).toBeVisible()
})
