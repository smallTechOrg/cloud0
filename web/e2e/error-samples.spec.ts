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

  // Triage card: "What to look at" with at least one verdict badge (text label, not colour only).
  const triage = errors.getByTestId('errors-look-at');
  await expect(triage).toBeVisible();
  await expect(triage).toContainText('What to look at');
  const badges = errors.getByTestId('errors-triage').getByTestId('verdict-badge');
  expect(await badges.count()).toBeGreaterThanOrEqual(1);
  await expect(badges.first()).toHaveText(/Needs attention|Watch|Probably noise|Healthy/);
  await expect(errors.getByTestId('errors-all-sources')).toBeVisible();

  // Logs Explorer links: new tab, safe rel, correct target (never clicked).
  const logLinks = errors.getByTestId('errors-logs-link');
  expect(await logLinks.count()).toBeGreaterThanOrEqual(1);
  for (const link of await logLinks.all()) {
    await expect(link).toHaveAttribute('href', /console\.cloud\.google\.com\/logs\/query/);
    await expect(link).toHaveAttribute('target', '_blank');
    await expect(link).toHaveAttribute('rel', /noopener/);
  }

  // On-demand sample from the first actionable item (or the noise section when nothing needs attention).
  let button = triage.getByTestId('triage-sample-button').first();
  if ((await triage.getByTestId('triage-sample-button').count()) === 0) {
    await errors.getByTestId('errors-noise').locator('summary').click();
    button = errors.getByTestId('errors-noise').getByTestId('triage-sample-button').first();
  }
  await expect(button).toBeVisible();
  await expect(button).toHaveText(/Show failing requests|Check paths/);
  await button.click();
  const item = button.locator('xpath=ancestor::li[@data-testid="triage-item"]');
  const outcome = item.locator(
    '[data-testid="errors-sample-result"], [data-testid="errors-sample-empty"], [data-testid="errors-sample-retention"], [data-testid="errors-sample-error"]',
  );
  await expect(outcome.first()).toBeVisible({ timeout: 90_000 });
  // Content or a clear empty state; never an error banner or a failed sample.
  await expect(item.getByTestId('errors-sample-error')).toHaveCount(0);
  await expect(page.getByTestId('error-banner')).toHaveCount(0);
  const result = item.getByTestId('errors-sample-result');
  if ((await result.count()) > 0) {
    expect(await result.getByTestId('errors-sample-entry-row').count()).toBeGreaterThanOrEqual(1);
  }

  // Details: three titled sections.
  await item.getByTestId('triage-details-button').click();
  const sections = item.getByTestId('errors-detail-section');
  await expect(sections).toHaveCount(3);
  await expect(item).toContainText('Server errors (5xx)');
  await expect(item).toContainText('Client errors that may be real');
  await expect(item).toContainText('404s: scanner-like vs possible broken links');
  await expect(page.getByTestId('error-banner')).toHaveCount(0);
});
