import { expect, test } from '@playwright/test';

// Walks every tab by hash against the live binary. Read-only: nothing is applied or saved.
test.setTimeout(240_000);

const PANELS: Array<[string, string]> = [
  ['cost', 'cost-table'],
  ['metrics', 'metrics-panel'],
  ['errors', 'errors-panel'],
  ['attribution', 'attribution-panel'],
  ['month', 'month-panel'],
  ['alerts', 'alerts-panel'],
  ['incidents', 'incidents-panel'],
  ['budgets', 'budgets-panel'],
  ['limits', 'limits-panel'],
  ['rightsizing', 'rightsizing-panel'],
  ['targets', 'targets-panel'],
];

const HEADED = new Set(['attribution', 'alerts', 'incidents', 'budgets', 'rightsizing']);

test('every tab renders its panel with no error banner; Vendors is the labelled stub', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await expect(page.getByTestId('week-picker')).toBeEnabled({ timeout: 15_000 });

  for (const [id, testId] of PANELS) {
    await page.evaluate((h) => {
      window.location.hash = h;
    }, id);
    await expect(page.getByTestId('active-panel')).toHaveAttribute('data-tab', id);
    const panel = page.getByTestId(testId);
    await expect(panel, `${id} panel`).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId('stub-panel')).toHaveCount(0);
    // Main heading. Panels whose empty/loading state is a titled card rather than an <h*>
    // (metrics, errors, month, limits, targets) must at least render non-empty content.
    const active = page.getByTestId('active-panel');
    if (HEADED.has(id)) await expect(active.getByRole('heading').first(), `${id} heading`).toBeVisible({ timeout: 30_000 });
    else await expect(panel, `${id} content`).not.toBeEmpty();
    // Let in-flight loads settle, then no error banner may be showing.
    await expect(page.getByTestId('active-panel').getByText(/^loading/i)).toHaveCount(0, { timeout: 60_000 });
    await expect(page.getByTestId('error-banner'), `${id} error banner`).toHaveCount(0);
  }

  await page.evaluate(() => {
    window.location.hash = 'vendors';
  });
  await expect(page.getByTestId('stub-panel')).toContainText('Vendors — Coming in Phase 4');
});

test('Cost tab By product shows a reconciliation badge or an empty-state hint', async ({ page }) => {
  await page.goto('/#cost');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await expect(page.getByTestId('cost-dimension-toggle')).toBeVisible({ timeout: 15_000 });
  await page.getByTestId('cost-dimension-product').click();
  const panel = page.getByTestId('dimension-panel');
  await expect(panel).toBeVisible({ timeout: 60_000 });
  await expect(panel.getByTestId('dimension-recon').or(panel.getByTestId('dimension-empty'))).toBeVisible({ timeout: 60_000 });
  await page.getByTestId('cost-dimension-environment').click();
  await expect(page.getByTestId('dimension-panel')).toHaveAttribute('data-dimension', 'environment', { timeout: 60_000 });
  await page.getByTestId('cost-dimension-service').click();
  await expect(page.getByTestId('cost-table')).toBeVisible();
});
