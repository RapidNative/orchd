import { test, expect } from './fixtures'

// Create a project through the UI and land on its detail page.
test('projects: create and open detail', async ({ page }) => {
  await page.goto('/projects')
  await expect(page.getByRole('heading', { level: 1, name: 'Projects' })).toBeVisible()

  await page.getByRole('button', { name: 'tinbase project' }).click()

  // Create navigates to /projects/<id>; the detail page shows its actions.
  await expect(page).toHaveURL(/\/projects\/[a-z0-9]+$/i)
  await expect(page.getByRole('button', { name: 'Backup project' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Delete project' })).toBeVisible()

  // The new project shows up back on the list.
  await page.getByRole('link', { name: 'Projects', exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Projects' })).toBeVisible()
  await expect(page.getByText('1 total')).toBeVisible()
})
