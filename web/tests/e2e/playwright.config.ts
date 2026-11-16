import { defineConfig, devices } from '@playwright/test'

// V63 of ADR 0030: the consent screen in a real browser, against the app's dev server and a played vendor. The API is
// played by the tests (`specs/played.ts`) until the routes of pull request 6 are composed into a dev enclave; the
// vendor is a small server on another site (127.0.0.1, while the app is on localhost), so the browser arrives at the
// consent screen by a cross-site navigation, as from a real vendor. Chromium only: passkeys are given by its virtual
// authenticator, which only Chromium has.
const port = Number(process.env.E2E_APP_PORT ?? 5174)

export default defineConfig({
  testDir: 'specs',
  // A flaky test hides a race; fix it instead of retrying.
  retries: 0,
  forbidOnly: true,
  workers: 1,
  fullyParallel: false,
  timeout: 60_000,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  use: {
    baseURL: `http://localhost:${port}`,
    trace: 'retain-on-failure',
  },
  webServer: {
    // The app as `bun run dev` serves it, from web/. Its proxy to an enclave is never reached: the tests answer /api.
    command: `bun run dev --port ${port} --strictPort --host localhost`,
    cwd: '../..',
    url: `http://localhost:${port}/`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
