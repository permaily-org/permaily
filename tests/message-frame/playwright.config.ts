import { defineConfig, devices } from '@playwright/test'

// The browser suite of ADR 0026 ("Tests", 26 to 31): real browsers load hostile messages in the real frame,
// served by internal/mailhtml/frametest. Run it with run.sh, which puts the browsers on a network with no route
// out: their only proxy is the server's (the app's host is the server, every other host a listener that records
// what reaches it) and their only DNS server is the server's resolver, which answers no name.
const proxy = process.env.FRAMETEST_PROXY
if (!proxy) throw new Error('run the suite with tests/message-frame/run.sh')
const browsers = (process.env.FRAMETEST_BROWSERS ?? 'chromium,firefox,webkit').split(',')

export default defineConfig({
  testDir: 'specs',
  // A flaky test hides a race; fix it instead of retrying.
  retries: 0,
  forbidOnly: true,
  // One server, one record of requests: tests run one at a time.
  workers: 1,
  fullyParallel: false,
  timeout: 60_000,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  use: {
    baseURL: 'https://app.permaily.test',
    ignoreHTTPSErrors: true,
    proxy: { server: proxy },
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'] } },
  ].filter((p) => browsers.includes(p.name)),
})
