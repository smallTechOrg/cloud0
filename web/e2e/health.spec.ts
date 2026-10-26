import { expect, test } from '@playwright/test';

test('API health reports the real embedded UI', async ({ request }) => {
  const res = await request.get('/api/v1/health');
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body.ok).toBe(true);
  expect(body.ui).toBe('real');
  expect(body.db).toBe('ok');
  expect(body.migrations).toBe('current');
  expect(body).toHaveProperty('bigquery');
});

test('dashboard shell loads, connects to the API and shows the Phase 1 placeholder', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('body')).toHaveAttribute('data-cloud0-ui', 'real');
  await expect(page.getByRole('heading', { level: 1, name: 'cloud0' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Cost' })).toHaveAttribute('href', '#cost');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await expect(page.getByTestId('cost-placeholder')).toContainText('Cost table arrives in Phase 1');
  await expect(page.getByTestId('config-summary')).toContainText('2026-W18');
});
