import { expect, test } from '@playwright/test';

// Incidents tab against the live binary (real Monitoring alerts API). Read-only:
// no Apply / Snooze preview is ever submitted.
const SMOKE_WEEK = process.env.CLOUD0_SMOKE_WEEK ?? '2026-W42';

test.setTimeout(120_000);

test('Incidents tab shows incidents or "Nothing firing", the history table and the review button', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  const picker = page.getByTestId('week-picker');
  await expect(picker).toBeEnabled({ timeout: 15_000 });
  await picker.selectOption(SMOKE_WEEK);

  await page.getByTestId('nav-tab-incidents').click();
  const panel = page.getByTestId('incidents-panel');
  await expect(panel).toBeVisible({ timeout: 30_000 });

  const active = panel.getByTestId('incidents-active');
  await expect(active.getByTestId('incidents-checked')).toContainText('Last checked', { timeout: 30_000 });
  const rows = active.getByTestId('incident-row');
  if ((await rows.count()) > 0) {
    await expect(rows.first().getByTestId('severity-badge')).toBeVisible();
    await expect(rows.first().getByTestId('incident-link')).toHaveAttribute('href', /^https:\/\/console\.cloud\.google\.com\//);
    await expect(rows.first().getByTestId('incident-snooze')).toBeVisible();
  } else {
    await expect(active.getByTestId('incidents-none')).toContainText('Nothing firing');
  }
  await expect(active.getByTestId('error-banner')).toHaveCount(0);

  const history = panel.getByTestId('incidents-history');
  await expect(history).toContainText(SMOKE_WEEK);
  await expect(history.getByTestId('history-table').or(history.getByTestId('history-empty'))).toBeVisible({ timeout: 30_000 });
  await expect(history.getByTestId('error-banner')).toHaveCount(0);
  await expect(history.getByTestId('history-copy')).toBeVisible();
  await expect(history.getByTestId('history-export')).toBeVisible();

  await expect(panel.getByTestId('incidents-snoozes')).toBeVisible();
});
