import fs from 'node:fs';

// Remove the per-run temp SQLite directory created in playwright.config.ts.
export default function globalTeardown(): void {
  const dir = process.env.CLOUD0_E2E_DB_DIR;
  if (dir && dir.includes('cloud0-e2e-')) {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}
