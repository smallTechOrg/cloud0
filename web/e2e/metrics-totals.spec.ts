import { expect, test } from '@playwright/test';

// Metrics tab: every group has a TOTAL row, and Cloud Run splits total memory
// into billing-derived active and idle (min-instance) columns. Real GCP data.
const SMOKE_WEEK = process.env.CLOUD0_SMOKE_WEEK ?? '2026-W42';
const REFRESH_TIMEOUT_MS = 150_000;

test.setTimeout(300_000);

const NUMERIC = /^\d[\d,]*(\.\d+)?$/;

test('Metrics: Cloud Run shows a Total row and numeric active/idle memory after Refresh', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  const picker = page.getByTestId('week-picker');
  await expect(picker).toBeEnabled({ timeout: 15_000 });
  await picker.selectOption(SMOKE_WEEK);

  await page.getByTestId('refresh-week').click();
  await expect(page.getByTestId('refresh-status')).toContainText(/Run #\d+ (ok|partial)/, { timeout: REFRESH_TIMEOUT_MS });

  await page.getByTestId('nav-tab-metrics').click();
  const metrics = page.getByTestId('metrics-block');
  await expect(metrics).toBeVisible({ timeout: 30_000 });

  const cloudrun = page.getByTestId('metrics-group-cloudrun');
  const headers = (await cloudrun.locator('thead th').allTextContents()).join('|');
  expect(headers).toContain('Memory total (GiB-s)');
  expect(headers).toContain('Active memory (GiB-s)');
  expect(headers).toContain('Idle (min-instance) memory (GiB-s)');

  // Column positions: heads[0] is the Resource th, so a head's index equals its 1-based td index.
  const heads = await cloudrun.locator('thead th').allTextContents();
  const col = (label: string) => heads.findIndex((h) => h.startsWith(label));
  const memTotal = col('Memory total');
  const memActive = col('Active memory');
  const memIdle = col('Idle (min-instance) memory');
  expect(memTotal).toBeGreaterThan(0);

  const valueAt = async (rowLocator: ReturnType<typeof cloudrun.locator>, idx: number) =>
    (await rowLocator.locator(`td:nth-of-type(${idx})`).locator('button span:first-child').first().textContent())?.trim() ?? '';

  // A visible, bold Total row.
  const total = cloudrun.getByTestId('metrics-total-row');
  await expect(total).toHaveCount(1);
  await expect(total).toBeVisible();
  await expect(total.locator('th')).toContainText('Total');
  const [t, a, i] = [await valueAt(total, memTotal), await valueAt(total, memActive), await valueAt(total, memIdle)];
  for (const v of [t, a, i]) expect(v).toMatch(NUMERIC);
  const num = (s: string) => Number(s.replace(/,/g, ''));
  // Monitoring total reconciles with billing active + idle (within 5%).
  expect(Math.abs(num(t) - (num(a) + num(i))) / num(t)).toBeLessThan(0.05);

  // Per-service rows are kept and at least one has a numeric active value.
  const rows = cloudrun.getByTestId('metrics-row');
  expect(await rows.count()).toBeGreaterThanOrEqual(1);
  const actives = await Promise.all((await rows.all()).map((r) => valueAt(r, memActive)));
  expect(actives.some((v) => NUMERIC.test(v))).toBe(true);

  // Every group with rows ends in a Total row; the VM total shows dashes for % columns.
  for (const g of ['vm', 'networking', 'cloudrun', 'logging', 'errors']) {
    const grp = page.getByTestId(`metrics-group-${g}`);
    if ((await grp.getByTestId('metrics-row').count()) > 0) await expect(grp.getByTestId('metrics-total-row')).toHaveCount(1);
  }
  const vmTotal = page.getByTestId('metrics-group-vm').getByTestId('metrics-total-row');
  if ((await vmTotal.count()) > 0) {
    expect((await vmTotal.locator('td button span:first-child').allTextContents()).map((s) => s.trim())).toEqual(['—', '—', '—']);
  }

  // The Limits row explains the memory allowance.
  await page.getByTestId('nav-tab-limits').click();
  await expect(page.getByTestId('limit-reason-cloudrun_memory_gib_s')).toContainText('active + idle min-instance memory', { timeout: 30_000 });
});
