import { expect, test } from '@playwright/test';

// Boot-level checks that hold before and after the Phase 1 API routes exist:
// the embedded UI is the real build, the shell renders, the week list comes
// from /api/v1/config and every week reads "not fetched — Refresh" on a fresh DB.

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

test('dashboard shell loads, connects to the API and renders the Cost tab with not-fetched rows and stub tabs', async ({ page, request }) => {
  const config = await (await request.get('/api/v1/config')).json();

  await page.goto('/');
  await expect(page.locator('body')).toHaveAttribute('data-cloud0-ui', 'real');
  await expect(page.getByRole('heading', { level: 1, name: 'cloud0' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Cost' })).toHaveAttribute('href', '#cost');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });

  // Week picker: default week preselected, newest first, oldest = first_week.
  const picker = page.getByTestId('week-picker');
  await expect(picker).toBeEnabled({ timeout: 15_000 });
  await expect(picker).toHaveValue(config.default_week);
  const options = await picker.locator('option').allTextContents();
  expect(options[0]).toContain(config.current_week);
  expect(options[options.length - 1]).toContain(config.first_week);
  await expect(page.getByTestId('scope-select')).toContainText('All billed projects');

  // Fresh DB → every week is "not fetched — Refresh" (never zeros), selected row highlighted.
  await expect(page.getByTestId(`not-fetched-${config.first_week}`)).toContainText('not fetched');
  await expect(page.getByTestId(`not-fetched-refresh-${config.first_week}`)).toHaveText('Refresh');
  await expect(page.getByTestId(`cost-row-${config.default_week}`)).toHaveAttribute('data-selected', 'true');

  // Stub columns are labelled; vendor cells are "—".
  const headers = await page.getByTestId('cost-header-label').allTextContents();
  expect(headers[0]).toBe('Week');
  expect(headers.slice(-2)).toEqual(['AI Cost (Groq)', 'claude (usage)']);
  await expect(page.getByTestId('cost-table').locator('thead')).toContainText('(stub)');

  // Phase 2 tabs are real (never a stub panel); every other non-Cost tab is a labelled stub.
  for (const id of ['metrics', 'errors', 'month', 'limits']) {
    await page.getByTestId(`nav-tab-${id}`).click();
    await expect(page.getByTestId('active-panel')).toHaveAttribute('data-tab', id);
    await expect(page.getByTestId('stub-panel')).toHaveCount(0);
  }
  const stubs: Array<[string, string]> = [
    ['attribution', 'Attribution — Coming in Phase 3'],
    ['budgets', 'Budgets & Alerts — Coming in Phase 4'],
    ['vendors', 'Vendors — Coming in Phase 4'],
    ['rightsizing', 'Rightsizing — Coming in Phase 3'],
    ['targets', 'Targets — Coming in Phase 3'],
  ];
  for (const [id, heading] of stubs) {
    await page.getByTestId(`nav-tab-${id}`).click();
    await expect(page.getByTestId('stub-panel')).toContainText(heading);
    await expect(page.getByTestId('stub-panel')).toContainText('Nothing on this tab is interactive in the current build.');
  }
  await page.getByTestId('nav-tab-cost').click();
  await expect(page.getByTestId('cost-table')).toBeVisible();

  // Run log drawer opens and never renders blank.
  await page.getByTestId('run-log-toggle').click();
  await expect(page.getByTestId('run-log')).toContainText(/No runs yet|runs unavailable|#\d+/);
});
