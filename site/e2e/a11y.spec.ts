// Accessibility (WCAG 2.2 AA) and CSP checks in a real browser, in both themes.
// Run with the built site served by `astro preview` (see playwright.config.ts).

import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';

const PAGES = [
  '/',
  '/developers/',
  '/companies/',
  '/security/',
  '/pricing/',
  '/about/',
  '/changelog/',
  '/contact/',
  '/privacy/',
  '/terms/',
  '/data-processing/',
  '/subprocessors/',
  '/data-deletion/',
  '/docs/',
  '/docs/verifying-the-build/',
  '/404.html',
];

for (const theme of ['light', 'dark'] as const) {
  for (const path of PAGES) {
    test(`${path} in ${theme} theme`, async ({ page }) => {
      const cspViolations: string[] = [];
      page.on('console', (msg) => {
        if (/Content Security Policy|Content-Security-Policy/i.test(msg.text())) {
          cspViolations.push(msg.text());
        }
      });
      await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
      await page.goto(`${path}${path.includes('?') ? '&' : '?'}theme=${theme}&still`);
      await page.waitForLoadState('networkidle');

      const results = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
        .analyze();
      const summary = results.violations.map(
        (v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`,
      );
      expect(summary, 'axe violations').toEqual([]);
      expect(cspViolations, 'CSP violations').toEqual([]);
    });
  }
}
