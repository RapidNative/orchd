import { test, expect } from './fixtures'

// The Docker substrate is mocked (ORCHD_DRIVER=mock), so this drives the real
// image CRUD flow through the browser: list seeded images, pull a new one,
// delete it.
test('images: list, pull, and delete round-trip', async ({ page }) => {
  await page.goto('/images')
  await expect(page.getByRole('heading', { level: 1, name: 'Images' })).toBeVisible()

  // Seeded images are listed.
  await expect(page.getByRole('cell', { name: 'tinbase', exact: true })).toBeVisible()
  await expect(page.getByRole('cell', { name: 'rn-vite', exact: true })).toBeVisible()

  // Pull a new image.
  await page.getByPlaceholder(/ghcr\.io/).fill('nginx:alpine')
  await page.getByRole('button', { name: 'Pull' }).click()
  await expect(page.getByText('Pulled nginx:alpine.')).toBeVisible()
  await expect(page.getByRole('cell', { name: 'nginx', exact: true })).toBeVisible()

  // Delete it (accept the confirm dialog).
  page.on('dialog', (d) => d.accept())
  await page
    .getByRole('row', { name: /nginx/ })
    .getByRole('button')
    .click()
  await expect(page.getByRole('cell', { name: 'nginx', exact: true })).toHaveCount(0)
})

// The "Built from templates" card is the ORCHD image concept (versioned freezes),
// distinct from the raw Docker daemon list. With no templates registered it shows
// the empty state and a disabled Build button.
test('images: built-from-templates card renders', async ({ page }) => {
  await page.goto('/images')
  await expect(page.getByText('Built from templates')).toBeVisible()
  await expect(page.getByText('No images built yet.', { exact: false })).toBeVisible()
  await expect(page.getByRole('button', { name: /Build image/ })).toBeDisabled()
})
