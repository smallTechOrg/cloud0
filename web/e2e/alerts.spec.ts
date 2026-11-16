import { expect, test } from '@playwright/test';

// Alerts tab journey against the live binary (playwright.config.ts). It only
// READS and PREVIEWS: Apply is verified present but NEVER clicked, so the
// browser test cannot mutate real GCP.

test('Alerts tab: templates → install preview (diff/desired/gcloud) → routing → adopt banner', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });

  await page.getByTestId('nav-tab-alerts').click();
  const panel = page.getByTestId('alerts-panel');
  await expect(panel).toBeVisible();

  // Status strip states the mode (writable or read-only).
  await expect(page.getByTestId('alerts-mode')).toHaveText(/writable|read-only/, { timeout: 30_000 });

  // Templates grid, grouped.
  await expect(page.getByTestId('templates-section')).toBeVisible();
  const cpuCard = page.getByTestId('template-card-vm_cpu_high');
  await expect(cpuCard).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('template-group-VM')).toBeVisible();
  await expect(cpuCard).toContainText('VM CPU high');
  await expect(page.getByTestId('template-default-vm_cpu_high')).toContainText('80');

  // Policies table lists the existing policies (managed or not).
  await expect(page.getByTestId('policies-section')).toBeVisible();

  // Adopt banner renders (list of known shapes or the explicit "none" line).
  const adopt = page.getByTestId('adopt-banner');
  await expect(adopt).toBeVisible();
  await expect(adopt).toContainText('Adopt & fix');
  await expect(page.getByTestId('adopt-budget-note')).toContainText('Budgets tab');

  // Routing editor renders with all three severities.
  const routing = page.getByTestId('routing-editor');
  await expect(routing).toBeVisible();
  for (const sev of ['CRITICAL', 'ERROR', 'WARNING']) {
    await expect(page.getByTestId(`routing-${sev}`)).toBeVisible();
  }

  // Install form on the CPU template -> drawer with diff / desired / gcloud.
  const install = page.getByTestId('template-install-vm_cpu_high');
  if ((await install.count()) === 0) {
    // Already installed in this account: open the Edit form instead.
    await cpuCard.locator('[data-testid^="template-edit-"]').first().click();
  } else {
    await install.click();
  }
  await expect(page.getByTestId('template-form-vm_cpu_high')).toBeVisible();
  await expect(page.getByTestId('param-threshold')).toBeVisible();
  await page.getByTestId('template-preview').click();

  const drawer = page.getByTestId('preview-drawer');
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute('data-phase', /preview|error/, { timeout: 30_000 });
  await expect(drawer).toHaveAttribute('data-phase', 'preview');
  await expect(page.getByTestId('preview-diff')).toBeVisible();
  await expect(page.getByTestId('preview-desired')).toContainText('cloud0');
  await expect(page.getByTestId('preview-gcloud')).toContainText('gcloud');
  await expect(page.getByTestId('preview-gcloud-copy')).toBeVisible();
  // Apply exists; deliberately NOT clicked.
  await expect(page.getByTestId('preview-apply')).toBeVisible();

  // Esc closes the drawer (keyboard accessible).
  await page.keyboard.press('Escape');
  await expect(drawer).toHaveCount(0);
});
