import { defineConfig, devices } from '@playwright/test';
import { config as loadDotEnv } from 'dotenv';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// Variables come from the repo-root .env (never overriding the process env).
loadDotEnv({ path: path.resolve(import.meta.dirname, '..', '.env'), override: false, quiet: true });

const port = Number(process.env.CLOUD0_E2E_PORT ?? '8097');
const baseURL = `http://127.0.0.1:${port}`;

// Every run gets its own SQLite file so e2e never touches the dev DB
// (globalTeardown removes the directory).
const dbDir = fs.mkdtempSync(path.join(os.tmpdir(), 'cloud0-e2e-'));
process.env.CLOUD0_E2E_DB_DIR = dbDir;
const dbPath = path.join(dbDir, 'cloud0.db');

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 60_000,
  globalTeardown: './e2e/global-teardown.ts',
  use: {
    baseURL,
    trace: 'retain-on-failure',
    ...devices['Desktop Chrome'],
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: "sh -c './bin/cloud0 migrate up && ./bin/cloud0 serve'",
    cwd: '..',
    url: `${baseURL}/api/v1/health`,
    reuseExistingServer: false,
    timeout: 60_000,
    env: {
      ...process.env,
      CLOUD0_PORT: String(port),
      CLOUD0_DB_PATH: dbPath,
    },
  },
});
