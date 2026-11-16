import { expect, test } from '@playwright/test';

// Attribution tab journey against the live binary (playwright.config.ts).
// It only READS and PREVIEWS: "Apply seed" and "Save rules" are verified present
// but NEVER clicked, so the browser test cannot change stored rules.

test('Attribution tab: coverage → rules → seed preview (no apply) → unattributed → labels → propose state', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByTestId('api-status')).toContainText('connected', { timeout: 15_000 });

  await page.getByTestId('nav-tab-attribution').click();
  const panel = page.getByTestId('attribution-panel');
  await expect(panel).toBeVisible();

  // (1) Coverage header: a percentage, or the explicit no-snapshot line.
  const header = page.getByTestId('coverage-header');
  await expect(header).toBeVisible();
  await expect(page.getByTestId('coverage-pct').or(page.getByTestId('coverage-nosnapshot'))).toBeVisible({ timeout: 30_000 });

  // (2) Rules editor renders: table with rows, or the empty state pointing at Auto-seed.
  const editor = page.getByTestId('rules-editor');
  await expect(editor).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('rules-table').or(page.getByTestId('rules-empty'))).toBeVisible();
  await expect(page.getByTestId('rule-add')).toBeVisible();
  await expect(page.getByTestId('allocate-select')).toBeVisible();
  await expect(page.getByTestId('aliases-editor')).toBeVisible();
  // Nothing edited yet: Save is disabled and no unsaved badge.
  await expect(page.getByTestId('rules-save')).toBeDisabled();
  await expect(page.getByTestId('rules-unsaved')).toHaveCount(0);

  // (3) Seed preview shows proposals without applying anything.
  await expect(page.getByTestId('seed-apply')).toHaveCount(0);
  await page.getByTestId('seed-preview-btn').click();
  await expect(page.getByTestId('seed-preview').or(page.getByTestId('seed-error'))).toBeVisible({ timeout: 60_000 });
  if (await page.getByTestId('seed-preview').isVisible()) {
    const hasSnapshot = (await page.getByTestId('seed-warning').count()) === 0 || (await page.getByTestId('seed-coverage-shift').count()) > 0;
    if (hasSnapshot && (await page.getByTestId('seed-coverage-shift').count()) > 0) {
      await expect(page.getByTestId('seed-coverage-shift')).toContainText('→');
      await expect(page.getByTestId('seed-apply')).toBeVisible(); // present, never clicked
    }
  }
  // Previewing wrote nothing: still no unsaved changes, rule list untouched.
  await expect(page.getByTestId('rules-unsaved')).toHaveCount(0);

  // (4) Unattributed list renders: rows, the all-attributed line, or no-snapshot line.
  await expect(
    page.getByTestId('unattributed-table').or(page.getByTestId('unattributed-empty')).or(page.getByTestId('unattributed-nosnapshot')),
  ).toBeVisible({ timeout: 30_000 });
  if ((await page.getByTestId('unattributed-create-0').count()) > 0) {
    // "Create rule from this" prefills the editor as an unsaved draft (not saved).
    await page.getByTestId('unattributed-create-0').click();
    await expect(page.getByTestId('rules-unsaved')).toBeVisible();
    await expect(page.getByTestId('rules-save').or(page.getByTestId('rules-invalid-count'))).toBeVisible();
    // Discard so the live rules are never touched.
    page.once('dialog', (d) => void d.accept());
    await page.getByTestId('rules-discard').click();
    await expect(page.getByTestId('rules-unsaved')).toHaveCount(0);
  }

  // Explain drawer is present with an input.
  await expect(page.getByTestId('explain-input')).toBeVisible();

  // (6) Label suggestions: a gcloud command when any suggestion exists; otherwise an explained state.
  await expect(
    page.getByTestId('labels-table').or(page.getByTestId('labels-empty')).or(page.getByTestId('labels-nosnapshot')),
  ).toBeVisible({ timeout: 30_000 });
  if ((await page.getByTestId('label-command-0').count()) > 0) {
    await expect(page.getByTestId('label-command-0')).toContainText('gcloud');
    await expect(page.getByTestId('label-copy-0')).toBeVisible();
    await expect(page.getByTestId('labels-progress-text')).toContainText('resources labelled');
  }

  // (7) Propose button is always shown, and its state is explained.
  const btn = page.getByTestId('propose-btn');
  await expect(btn).toBeVisible();
  await expect(page.getByTestId('propose-explain')).toContainText(/OpenRouter key|nothing is applied automatically/i);
  if (await btn.isDisabled()) {
    await expect(page.getByTestId('propose-explain')).toContainText('set an OpenRouter key');
  }
});
