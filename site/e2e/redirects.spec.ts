// The site's old sign-up and log-in addresses land in the web app.
// The app is stubbed, so this checks the site's redirect without reaching app.permaily.com.

import { expect, test } from '@playwright/test';
import { LINKS } from '../src/config/site';

for (const [path, target] of [
  ['/signup/', LINKS.signup],
  ['/login/', LINKS.login],
] as const) {
  test(`${path} lands in the app`, async ({ page }) => {
    await page.route('https://app.permaily.com/**', (route) =>
      route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>app</title>' }),
    );
    await page.goto(path);
    await page.waitForURL(target);
    expect(page.url()).toBe(target);
  });
}
