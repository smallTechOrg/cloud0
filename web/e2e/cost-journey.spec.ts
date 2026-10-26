import { expect, test, type Locator, type Page } from '@playwright/test';
import fs from 'node:fs';

// The Phase 1 journey (spec/roadmap.md → Phase 1 gate) against the live binary
// started by playwright.config.ts with a fresh temp DB. Refresh hits the real
// BigQuery billing export using the credentials in the repo-root .env.
const SMOKE_WEEK = process.env.CLOUD0_SMOKE_WEEK ?? '2026-W42';
const REFRESH_TIMEOUT_MS = 90_000;

test.describe.configure({ mode: 'serial' });
test.setTimeout(240_000);

function parseMoney(text: string | null): number {
  // "₹1,191.69" or "(₹12.34)" → number (currency symbol and grouping stripped)
  if (text === null) throw new Error('missing numeric text');
  const negative = text.includes('(');
  const n = Number(text.replace(/[^0-9.\-]/g, ''));
  if (!Number.isFinite(n)) throw new Error(`not a number: ${text}`);
  return negative ? -n : n;
}

async function selectSmokeWeek(page: Page): Promise<void> {
  const picker = page.getByTestId('week-picker');
  await expect(picker).toBeEnabled({ timeout: 15_000 });
  await picker.selectOption(SMOKE_WEEK);
  await expect(page.getByTestId(`cost-row-${SMOKE_WEEK}`)).toHaveAttribute('data-selected', 'true');
}

async function cellNet(cell: Locator): Promise<number> {
  const raw = await cell.getAttribute('data-net');
  const n = Number(raw);
  if (!Number.isFinite(n)) throw new Error(`data-net missing on cell: ${raw}`);
  return n;
}

test('Phase 1 journey: refresh → table → inspector → drill-down → CSV → note → thresholds → stubs → run log', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('body')).toHaveAttribute('data-cloud0-ui', 'real');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });

  // Week picker lists first → current with the default (last complete) week preselected.
  const picker = page.getByTestId('week-picker');
  await expect(picker).toBeEnabled({ timeout: 15_000 });
  const defaultWeek = await picker.inputValue();
  expect(defaultWeek).toMatch(/^\d{4}-W\d{2}$/);
  await expect(page.getByTestId('scope-select')).toHaveValue('all');

  // Fresh DB: every row reads "not fetched — Refresh", never zeros.
  await expect(page.getByTestId(`not-fetched-${SMOKE_WEEK}`)).toContainText('not fetched');

  // Refresh the smoke week (real BigQuery, 5–30 s).
  await selectSmokeWeek(page);
  await page.getByTestId('refresh-week').click();
  await expect(page.getByTestId('refresh-status')).toContainText(/Refreshing|Run #/);

  const compute = page.getByTestId(`cell-${SMOKE_WEEK}-compute_engine`);
  const total = page.getByTestId(`cell-${SMOKE_WEEK}-total`);
  await expect(compute).toBeVisible({ timeout: REFRESH_TIMEOUT_MS });
  await expect(page.getByTestId('refresh-status')).toContainText(/Run #\d+ ok/);
  expect(await cellNet(compute)).toBeGreaterThan(0);
  expect(await cellNet(total)).toBeGreaterThan(0);
  expect(parseMoney(await compute.textContent())).toBeGreaterThan(0);

  // Header order: Week first, Total GCP / % change / the two stub columns last.
  const headers = await page.getByTestId('cost-header-label').allTextContents();
  expect(headers[0]).toBe('Week');
  expect(headers.slice(1, 12)).toEqual([
    'Compute Engine',
    'Networking',
    'CloudSQL',
    'Gemini API',
    'Cloud Run',
    'GCS',
    'VM manager',
    'Cloud Build',
    'Cloud Logging',
    'Secret Manager',
    'Artifact Registry',
  ]);
  expect(headers.slice(-4)).toEqual(['Total GCP', '% change', 'AI Cost (Groq)', 'claude (usage)']);

  // Cell Inspector: provenance (SQL with the partition filter, snapshot id, status) + SKU drill-down.
  await compute.click();
  const inspector = page.getByTestId('inspector');
  await expect(inspector).toBeVisible();
  await expect(inspector.getByTestId('inspector-sql')).toContainText('_PARTITIONTIME', { timeout: 15_000 });
  await expect(inspector.getByTestId('inspector-snapshot-id')).toContainText(/#\d+/);
  await expect(inspector.getByTestId('inspector-status')).toContainText(/partial|settling|final/);
  await expect(inspector.getByTestId('inspector-params')).toContainText('usage_start');
  await expect(inspector.getByTestId('inspector-column-line')).toHaveText('Column = services: Compute Engine');

  const drilldown = inspector.getByTestId('drilldown');
  await expect(drilldown.getByTestId('drilldown-level')).toHaveText('sku');
  const skuRows = drilldown.getByTestId('drilldown-row');
  await expect(skuRows.first()).toBeVisible({ timeout: 15_000 });
  expect(await skuRows.count()).toBeGreaterThanOrEqual(1);
  await expect(drilldown.getByTestId('drilldown-mismatch')).toHaveCount(0);

  // SKU → resource rows with gross / credits / net.
  await drilldown.getByTestId('drilldown-row-link').first().click();
  await expect(drilldown.getByTestId('drilldown-level')).toHaveText('resource', { timeout: 15_000 });
  const resourceRows = drilldown.getByTestId('drilldown-row');
  await expect(resourceRows.first()).toBeVisible({ timeout: 15_000 });
  expect(await resourceRows.count()).toBeGreaterThanOrEqual(1);
  await expect(drilldown.getByTestId('drilldown-table').locator('thead')).toContainText(/gross/);
  await expect(drilldown.getByTestId('drilldown-table').locator('thead')).toContainText(/credits/);
  await expect(drilldown.getByTestId('drilldown-table').locator('thead')).toContainText(/net/);
  await drilldown.getByTestId('drilldown-back').click();
  await expect(drilldown.getByTestId('drilldown-level')).toHaveText('sku');
  await inspector.getByTestId('inspector-close').click();
  await expect(inspector).toHaveCount(0);

  // Export CSV: the first line equals the visible header labels joined by commas.
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByTestId('export-csv').click()]);
  expect(download.suggestedFilename()).toMatch(/^cloud0-cost-all-\d{4}-W\d{2}-\d{4}-W\d{2}-[A-Z]{3}\.csv$/);
  const csvPath = await download.path();
  const csv = fs.readFileSync(csvPath, 'utf8');
  const [firstLine, ...dataLines] = csv.split('\r\n');
  expect(firstLine).toBe(headers.join(','));
  const smokeLine = dataLines.find((l) => l.startsWith(`${SMOKE_WEEK},`));
  expect(smokeLine).toBeDefined();
  expect(smokeLine!.split(',')[1]).toMatch(/^\d+\.\d{2}$/);

  // Annotation: type, Save, reload → still present.
  const note = `e2e note ${Date.now()}`;
  await page.getByTestId('annotation-input').fill(note);
  await page.getByTestId('annotation-save').click();
  await expect(page.getByTestId('annotation-saved')).toContainText(/Saved \d{2}:\d{2}Z/);
  await page.reload();
  await selectSmokeWeek(page);
  await expect(page.getByTestId('annotation-input')).toHaveValue(note, { timeout: 15_000 });
  await expect(page.getByTestId(`note-icon-${SMOKE_WEEK}`)).toBeVisible();

  // Thresholds: 10 % / 5, Save, reload → still 10 / 5.
  await page.getByTestId('thresholds-edit').click();
  await page.getByTestId('thresholds-pct').fill('10');
  await page.getByTestId('thresholds-abs').fill('5');
  await page.getByTestId('thresholds-save').click();
  await expect(page.getByTestId('thresholds-label')).toContainText('≥ 10 % and ≥ ₹5.00');
  await page.reload();
  await expect(page.getByTestId('thresholds-label')).toContainText('≥ 10 % and ≥ ₹5.00', { timeout: 15_000 });
  await page.getByTestId('thresholds-edit').click();
  await expect(page.getByTestId('thresholds-pct')).toHaveValue('10');
  await expect(page.getByTestId('thresholds-abs')).toHaveValue('5');
  await page.getByTestId('thresholds-cancel').click();

  // Vendors is the only remaining labelled stub (every other tab is real).
  await page.getByTestId('nav-tab-vendors').click();
  await expect(page.getByTestId('stub-panel')).toContainText('Vendors — Coming in Phase 4');
  await expect(page.getByTestId('stub-panel')).toContainText('Not functional yet');
  await page.getByTestId('nav-tab-cost').click();
  await expect(page.getByTestId('cost-table')).toBeVisible();

  // Run log drawer lists the run with bytes billed and the job id.
  await page.getByTestId('run-log-toggle').click();
  const runTable = page.getByTestId('run-log-table');
  await expect(runTable).toBeVisible({ timeout: 15_000 });
  const firstRun = runTable.locator('[data-testid^="run-row-"]').first();
  await expect(firstRun).toContainText('refresh_week');
  await expect(firstRun).toContainText(SMOKE_WEEK);
  await expect(firstRun.locator('[data-testid^="run-bytes-billed-"]')).toContainText(/\d+(\.\d+)? (B|KB|MB|GB)/);
  await firstRun.getByRole('button', { name: /toggle sources/ }).click();
  await expect(runTable.getByTestId('run-source-job-id').first()).not.toHaveText('—');
});

test('unknown API paths return the not_found envelope and a deep-linked stub tab still renders the shell', async ({ page, request }) => {
  const res = await request.get('/api/v1/does-not-exist');
  expect(res.status()).toBe(404);
  const body = await res.json();
  expect(body.error.code).toBe('not_found');

  // The page itself always renders the shell even when a data call fails.
  await page.goto('/#vendors');
  await expect(page.getByTestId('stub-panel')).toContainText('Vendors — Coming in Phase 4');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
});
