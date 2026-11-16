import { expect, test } from '@playwright/test';

// Assign products journey against the live binary (isolated e2e DB, real GCP reads).
// It assigns a product through a cloud0 rule (stored only in the throwaway e2e DB),
// and opens the GCP label PREVIEW (a read). "Write … to GCP" is verified present
// but NEVER clicked, so this test cannot change any label in GCP.

test('Assign products: select → assign (rule) → coverage moves → label preview (no write)', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await page.getByTestId('refresh-week').click();
  await expect(page.getByTestId('refresh-week')).toBeEnabled({ timeout: 120_000 });

  await page.getByTestId('nav-tab-attribution').click();
  const section = page.getByTestId('assign-section');
  await expect(section).toBeVisible();
  await expect(page.getByTestId('assign-table')).toBeVisible({ timeout: 60_000 });
  const rows = page.locator('[data-testid^="assign-row-"]');
  if ((await rows.count()) === 0) return; // everything already assigned: nothing to do

  // Export links exist.
  await expect(page.getByTestId('assign-export-csv')).toHaveAttribute('href', /attribution\/export.*format=csv/);

  // Toolbar is inert until something is selected.
  await expect(page.getByTestId('assign-apply')).toBeDisabled();
  await rows.first().getByRole('checkbox').check();
  await page.getByTestId('assign-product').fill('e2e-product');
  await page.getByTestId('assign-env').fill('prod');
  await expect(page.getByTestId('assign-apply')).toBeEnabled();
  await page.getByTestId('assign-apply').click();
  await expect(page.getByTestId('assign-msg')).toContainText('Assigned 1 resource group', { timeout: 30_000 });

  // The assigned group leaves the "unassigned only" list and shows up with its product when unfiltered.
  await page.getByTestId('assign-only-unassigned').uncheck();
  await expect(section.getByText('e2e-product').first()).toBeVisible();

  // Label preview: assign + preview for a labelable group; the write button exists but is never clicked.
  const labelable = section.locator('tr', { hasText: /compute (instance|disk)|cloud run|cloud sql|gcs bucket/i }).first();
  if ((await labelable.count()) > 0 && (await page.getByTestId('assign-and-label').isEnabled().catch(() => false)) === false) {
    await labelable.getByRole('checkbox').check();
    await page.getByTestId('assign-product').fill('e2e-product');
    if (await page.getByTestId('assign-and-label').isEnabled()) {
      await page.getByTestId('assign-and-label').click();
      await expect(page.getByTestId('label-preview').or(page.getByTestId('label-error'))).toBeVisible({ timeout: 60_000 });
      if (await page.getByTestId('label-preview').isVisible()) {
        await expect(page.getByTestId('label-apply')).toBeVisible(); // present, never clicked
      }
    }
  }
});
