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
  await page.getByPlaceholder(/my-runtime/).fill('nginx:alpine')
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

// Importing a pushed image's spec (from another instance) registers it here as a
// docker-only image that projects can boot from.
test('images: import an image from a spec', async ({ page }) => {
  await page.goto('/images')
  const spec = JSON.stringify({
    template: 'imp-demo',
    version: 'v1',
    dockers: { api: 'ghcr.io/acme/orchd-imp-demo-api:v1' },
    workloads: [{ name: 'api', kind: 'node', workspace: 'api', image: 'rn-api:dev' }],
  })
  await page.getByPlaceholder(/"template"/).fill(spec)
  await page.getByRole('button', { name: 'Import image', exact: true }).click()

  await expect(page.getByText('Imported.')).toBeVisible()
  await expect(page.getByRole('cell', { name: 'imp-demo', exact: true })).toBeVisible()
  await expect(page.getByText('imported', { exact: true })).toBeVisible()

  // An image with registry refs (published) exposes a Copy spec button, and
  // clicking it reports back (copied, or a clipboard-blocked fallback).
  const copy = page.getByRole('button', { name: /Copy spec/ })
  await expect(copy).toBeVisible()
  await copy.click()
  await expect(page.getByText(/Copied import spec|Clipboard blocked/)).toBeVisible()
})
