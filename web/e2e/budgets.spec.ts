import { expect, test } from '@playwright/test';

// Budgets tab against the live binary (needs CLOUD0_ALERTS_CREDENTIALS and a
// billing account). Never clicks Apply: GCP is read-only for this spec.
test('Budgets tab: unmanaged budget, suggestions with a basis, create form opens the preview drawer', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await page.getByTestId('nav-tab-budgets').click();
  const panel = page.getByTestId('budgets-panel');
  await expect(panel).toBeVisible();

  // The pre-existing budget is listed as unmanaged and offers adopt.
  const unmanaged = panel.locator('[data-testid="budget-card"][data-managed="false"]').first();
  await expect(unmanaged).toBeVisible({ timeout: 30_000 });
  await expect(unmanaged.getByTestId('budget-badge')).toHaveText('Unmanaged');
  await expect(unmanaged.getByTestId('budget-amount')).toContainText('₹');
  await expect(unmanaged.getByTestId('budget-adopt')).toBeVisible();

  // Suggestions render, each with a basis line.
  const rows = panel.getByTestId('suggestion-row');
  await expect(rows.first()).toBeVisible();
  const basis = (await rows.first().getByTestId('suggestion-basis').textContent()) ?? '';
  expect(basis.trim().length).toBeGreaterThan(0);

  // Create form: scope account-wide, explicit INR amount, preview opens the drawer.
  await panel.getByTestId('budget-create').click();
  const form = page.getByTestId('budget-form');
  await expect(form).toBeVisible();
  await expect(form.getByTestId('budget-basis')).toBeVisible();
  await expect(form.getByTestId('budget-name-input')).toHaveValue(/^cloud0-/);
  await form.getByTestId('budget-amount-input').fill('7777');
  await form.getByTestId('budget-submit').click();

  const drawer = page.getByTestId('preview-drawer');
  await expect(drawer).toBeVisible({ timeout: 30_000 });
  await expect(drawer.getByTestId('preview-desired')).toContainText('7777', { timeout: 30_000 });
  await expect(drawer.getByTestId('preview-desired')).toContainText('INR');
  // Deliberately NOT clicking Apply.
  await drawer.getByTestId('preview-close').click();
  await expect(drawer).toBeHidden();
});
