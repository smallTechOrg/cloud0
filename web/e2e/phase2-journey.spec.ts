import { expect, test, type Page } from '@playwright/test';
import fs from 'node:fs';

// The Phase 2 journey (spec/roadmap.md → Phase 2 gate) against the live binary
// started by playwright.config.ts with a fresh temp DB and real GCP data.
const SMOKE_WEEK = process.env.CLOUD0_SMOKE_WEEK ?? '2026-W42';
const REFRESH_TIMEOUT_MS = 150_000;

test.describe.configure({ mode: 'serial' });
test.setTimeout(420_000);

async function selectSmokeWeek(page: Page): Promise<void> {
  const picker = page.getByTestId('week-picker');
  await expect(picker).toBeEnabled({ timeout: 15_000 });
  await picker.selectOption(SMOKE_WEEK);
}

test('Phase 2 journey: refresh → Metrics → Errors → Month → Limits → provenance → CSV', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await selectSmokeWeek(page);

  // Refresh week: billing + every Phase 2 source against real GCP.
  await page.getByTestId('refresh-week').click();
  await expect(page.getByTestId('refresh-status')).toContainText(/Run #\d+ (ok|partial)/, { timeout: REFRESH_TIMEOUT_MS });

  // ---- Metrics ----
  await page.getByTestId('nav-tab-metrics').click();
  const metrics = page.getByTestId('metrics-block');
  await expect(metrics).toBeVisible({ timeout: 30_000 });
  const headings = await metrics.locator('h3').allTextContents();
  expect(headings).toEqual(['VMs', 'Cloud SQL', 'Networking', 'Cloud Run', 'Cloud Storage', 'Cloud Logging', 'Errors']);

  // A numeric CPU % for at least one VM (first data column of the VM group).
  const vmRows = page.getByTestId('metrics-group-vm').getByTestId('metrics-row');
  expect(await vmRows.count()).toBeGreaterThanOrEqual(1);
  const cpuTexts = await vmRows.locator('td:nth-of-type(1) button span:first-child').allTextContents();
  expect(cpuTexts.some((t) => /^\d+(\.\d+)?$/.test(t.trim()))).toBe(true);

  // Gaps are marked by data-gap and never render as a bare 0; real cells are never gap text.
  const gapTexts = await metrics.locator('td[data-gap="true"] button span:first-child').allTextContents();
  for (const t of gapTexts) expect(t.trim()).not.toMatch(/^0(\.0+)?$/);
  expect(gapTexts.length + (await metrics.locator('td:not([data-gap]) button').count())).toBeGreaterThan(0);
  const valueTexts = await metrics.locator('td:not([data-gap]) button span:first-child').allTextContents();
  for (const t of valueTexts) expect(t.trim()).toMatch(/^[\d.,]+$/);
  // Memory column of the VMs: a real value or an explicit 'no agent' gap, never blank.
  const memTexts = await vmRows.locator('td:nth-of-type(2) button span:first-child').allTextContents();
  expect(memTexts.length).toBeGreaterThan(0);
  for (const t of memTexts) expect(t.trim()).toMatch(/^(\d+(\.\d+)?|no agent|no data|source failed|not collected)$/);

  // Provenance: clicking the first VM CPU cell shows the exact Monitoring filter.
  await vmRows.first().locator('td:nth-of-type(1) button').click();
  const prov = page.getByTestId('metric-provenance');
  await expect(prov).toBeVisible();
  await expect(prov.getByTestId('metric-provenance-filter')).toContainText('metric.type');
  await expect(prov.getByTestId('metric-provenance-window')).toContainText('→');
  await prov.getByTestId('metric-provenance-close').click();
  await expect(prov).toHaveCount(0);

  // Export CSV on Metrics.
  const [metricsCsv] = await Promise.all([page.waitForEvent('download'), page.getByTestId('metrics-export').click()]);
  expect(metricsCsv.suggestedFilename()).toBe(`cloud0-metrics-all-${SMOKE_WEEK}.csv`);
  const csv = fs.readFileSync(await metricsCsv.path(), 'utf8');
  expect(csv).toContain('VMs');
  expect(csv).toContain('Cloud Storage');

  // ---- Errors ----
  await page.getByTestId('nav-tab-errors').click();
  const errors = page.getByTestId('errors-block');
  await expect(errors).toBeVisible({ timeout: 30_000 });
  const crRows = errors.getByTestId('errors-cloudrun-row');
  expect(await crRows.count()).toBeGreaterThanOrEqual(1);
  await expect(errors.locator('[data-testid="errors-cloudrun-row"] td[data-emphasis="4xx"]').first()).toBeVisible();
  // The load-balancer metric has real data: each project's card shows counts when present,
  // and the gap reason + fix hint only when absent.
  const lbCards = errors.locator('[data-testid="errors-gap-card"][data-source="load_balancer"]');
  const lbN = await lbCards.count();
  expect(lbN).toBeGreaterThanOrEqual(1);
  for (let i = 0; i < lbN; i++) {
    const card = lbCards.nth(i);
    if ((await card.getByTestId('errors-gap-counts').count()) > 0) {
      await expect(card.getByTestId('errors-gap-counts')).toContainText(/4xx \d/);
    } else {
      await expect(card.getByTestId('errors-gap-reason')).toBeVisible();
      await expect(card.getByTestId('errors-gap-hint')).toContainText('Fix:');
    }
  }
  // nginx is never collected: gap card(s) with the fix hint.
  const nginxCards = errors.locator('[data-testid="errors-gap-card"][data-source="nginx"]');
  const nginxN = await nginxCards.count();
  expect(nginxN).toBeGreaterThanOrEqual(1);
  for (let i = 0; i < nginxN; i++) {
    await expect(nginxCards.nth(i).getByTestId('errors-gap-reason')).toContainText('not collected');
    await expect(nginxCards.nth(i).getByTestId('errors-gap-hint')).toContainText(/Fix:\s*\S+/);
  }

  // ---- Month ----
  await page.getByTestId('nav-tab-month').click();
  await expect(page.getByTestId('month-table')).toBeVisible({ timeout: 30_000 });
  expect(await page.locator('[data-testid^="month-row-"]').count()).toBeGreaterThanOrEqual(1);
  const summary = page.getByTestId('month-summary');
  await expect(summary).toContainText('target');
  await expect(summary).toContainText('projected');
  await page.getByTestId('month-refresh').click();
  await expect(page.getByTestId('month-notice')).toContainText(/Refreshed \d{4}-\d{2} · run #\d+ (ok|partial)/, { timeout: REFRESH_TIMEOUT_MS });
  await expect(page.getByTestId('month-table')).toBeVisible();

  // ---- Limits ----
  await page.getByTestId('nav-tab-limits').click();
  await expect(page.getByTestId('limits-table')).toBeVisible({ timeout: 30_000 });
  const levels = await page.locator('[data-testid^="limit-row-"]').evaluateAll((els) => els.map((e) => e.getAttribute('data-level')));
  expect(levels.length).toBe(14);
  for (const l of levels) expect(['ok', 'near', 'over', 'stub', 'no data']).toContain(l);
  expect(levels.filter((l) => l === 'ok' || l === 'near' || l === 'over').length).toBeGreaterThanOrEqual(1);

  // Edit an allowance, save, reload → persists.
  await page.getByTestId('limit-edit-logging_gb').click();
  await page.getByTestId('limit-input-logging_gb').fill('75');
  await page.getByTestId('limit-save-logging_gb').click();
  await expect(page.getByTestId('limit-allowance-logging_gb')).toContainText('75', { timeout: 15_000 });
  await page.reload();
  await expect(page.getByTestId('limit-allowance-logging_gb')).toContainText('75', { timeout: 30_000 });

  // Invalid value → inline error, nothing saved.
  await page.getByTestId('limit-edit-logging_gb').click();
  await page.getByTestId('limit-input-logging_gb').fill('-5');
  await page.getByTestId('limit-save-logging_gb').click();
  await expect(page.getByTestId('limit-error-logging_gb')).toBeVisible();
  await page.getByTestId('limit-cancel-logging_gb').click();
  await expect(page.getByTestId('limit-allowance-logging_gb')).toContainText('75');
});
