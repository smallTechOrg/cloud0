import { expect, test } from '@playwright/test';

// Read-only checks against the live binary: never edits or saves targets.
test('Targets tab: account row, product rows or the add-rules empty state, estimates note', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await page.getByTestId('nav-tab-targets').click();
  const panel = page.getByTestId('targets-panel');
  await expect(panel).toBeVisible();
  await expect(panel.getByTestId('targets-table')).toBeVisible({ timeout: 30_000 });
  await expect(panel.getByTestId('target-row-account')).toBeVisible();
  await expect(panel.getByTestId('target-row-shared')).toBeVisible();
  await expect(panel.getByTestId('target-row-unattributed')).toBeVisible();
  await expect(panel.getByTestId('targets-note')).toContainText('estimates');
  // Save is disabled until something is edited (we never edit here).
  await expect(panel.getByTestId('targets-save')).toBeDisabled();
  // Either product rows exist or the empty state points at the Attribution tab.
  const productRows = panel.locator('[data-testid^="target-row-"]:not([data-testid="target-row-account"]):not([data-testid="target-row-shared"]):not([data-testid="target-row-unattributed"])');
  if ((await productRows.count()) === 0) {
    await expect(panel.getByTestId('targets-empty')).toContainText('Attribution tab');
  } else {
    await expect(productRows.first().getByTestId('target-status')).toHaveText(/On track|At risk|Over|No target|No data/);
  }
});

test('Cost tab product view (when the toggle is wired): groups, reconciliation, shared last', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  const toggle = page.getByTestId('cost-dimension-toggle');
  test.skip((await toggle.count()) === 0, 'cost-dimension-toggle is not mounted yet');
  await toggle.getByRole('button', { name: /product/i }).click();
  const panel = page.getByTestId('dimension-panel');
  await expect(panel).toBeVisible({ timeout: 30_000 });
  const empty = panel.getByTestId('dimension-empty');
  if ((await empty.count()) > 0) return; // no snapshot for the selected week
  await expect(panel.getByTestId('allocate-select')).toBeVisible();
  await expect(panel.getByTestId('dimension-recon')).toBeVisible();
  const kinds = await panel.locator('[data-testid^="dimension-row-"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-kind')));
  expect(kinds.at(-1)).toBe('unattributed');
  expect(kinds.at(-2)).toBe('shared');
});
