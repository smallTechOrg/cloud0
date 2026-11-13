import { expect, test } from '@playwright/test';

// Rightsizing tab against the live binary: loads without an error banner and
// shows either candidates or the honest empty state; filters narrow the list.
test('Rightsizing tab: candidates or empty state, filters, CSV button', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });
  await page.getByTestId('nav-tab-rightsizing').click();
  const panel = page.getByTestId('rightsizing-panel');
  await expect(panel).toBeVisible();
  await expect(panel.getByTestId('rightsizing-headline')).toBeVisible({ timeout: 30_000 });
  await expect(panel.getByTestId('error-banner')).toHaveCount(0);
  await expect(panel.getByTestId('rightsizing-csv')).toBeVisible();
  await expect(panel.getByTestId('rightsizing-footnote')).toContainText('estimates');
  await expect(panel.getByTestId('rightsizing-thresholds')).toContainText('mean CPU');

  const cards = panel.getByTestId('rightsizing-card');
  const n = await cards.count();
  if (n === 0) {
    await expect(panel.getByTestId('rightsizing-empty')).toContainText('Nothing to right-size with the data we have');
    return;
  }
  await expect(panel.getByTestId('rightsizing-total')).toContainText('₹');
  await expect(cards.first().getByTestId('candidate-saving')).toBeVisible();
  await expect(cards.first().getByTestId('candidate-action')).not.toBeEmpty();

  // Kind filter narrows the list to that kind only.
  const kind = panel.getByTestId('rightsizing-kind');
  const options = await kind.locator('option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value).filter((v) => v !== ''));
  if (options.length > 1) {
    await kind.selectOption(options[0]!);
    const kinds = await panel.getByTestId('rightsizing-card').evaluateAll((els) => els.map((e) => e.getAttribute('data-kind')));
    expect(kinds.length).toBeGreaterThan(0);
    expect(kinds.length).toBeLessThanOrEqual(n);
    expect(new Set(kinds)).toEqual(new Set([options[0]]));
  }
});
