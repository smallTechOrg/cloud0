import { expect, test } from '@playwright/test';

// Errors explainability: exact status codes per Cloud Run service, an on-demand
// Cloud Logging sample, and a Logs Explorer deep link — against the live binary
// started by playwright.config.ts with a fresh temp DB and real GCP data.
const SMOKE_WEEK = process.env.CLOUD0_SMOKE_WEEK ?? '2026-W42';
const REFRESH_TIMEOUT_MS = 150_000;

test.setTimeout(300_000);

test('Errors tab explains itself: how counted, exact codes, sampled paths, Logs Explorer link', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  const picker = page.getByTestId('week-picker');
  await expect(picker).toBeEnabled({ timeout: 15_000 });
  await picker.selectOption(SMOKE_WEEK);

  await page.getByTestId('refresh-week').click();
  await expect(page.getByTestId('refresh-status')).toContainText(/Run #\d+ (ok|partial)/, { timeout: REFRESH_TIMEOUT_MS });

  await page.getByTestId('nav-tab-errors').click();
  const errors = page.getByTestId('errors-block');
  await expect(errors).toBeVisible({ timeout: 30_000 });
  await expect(errors.getByTestId('errors-how-counted')).toContainText('internet scanners');

  // Expand the first Cloud Run service that has 4xx responses.
  const row = errors.locator('[data-testid="errors-cloudrun-row"]:has(td[data-emphasis="4xx"])').first();
  await expect(row).toBeVisible();
  await row.getByTestId('errors-expand').click();
  const detail = errors.getByTestId('errors-detail-row').first();
  await expect(detail).toBeVisible();
  const chips = detail.getByTestId('errors-code-chip');
  expect(await chips.count()).toBeGreaterThanOrEqual(1);
  await expect(chips.first()).toHaveText(/^[45]\d\d ×[\d,]+$/);

  // Explain & sample: top paths from real Cloud Logging.
  await detail.getByTestId('errors-sample-button').click();
  await expect(detail.getByTestId('errors-sample-result')).toBeVisible({ timeout: 90_000 });
  expect(await detail.getByTestId('errors-sample-path-row').count()).toBeGreaterThanOrEqual(1);
  expect(await detail.getByTestId('errors-sample-entry-row').count()).toBeGreaterThanOrEqual(1);
  await expect(detail.getByTestId('errors-sample-result')).toContainText('most recent entries');

  // Logs Explorer link: new tab, safe rel, correct target (not clicked).
  const link = detail.getByTestId('errors-logs-link');
  await expect(link).toHaveAttribute('href', /console\.cloud\.google\.com\/logs\/query/);
  await expect(link).toHaveAttribute('target', '_blank');
  await expect(link).toHaveAttribute('rel', /noopener/);
});
