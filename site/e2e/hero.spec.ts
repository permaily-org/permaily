// Home's hero says two sentences, and above the phone breakpoint each keeps to one line.
// A scrollbar that always shows takes width from the page and none from the window, so type sized from the
// window can be a few pixels too wide for its line. A test browser hides its scrollbar, so the page is made
// narrower by one here instead.

import { expect, test } from '@playwright/test';

/** The widest scrollbar a desktop browser draws. */
const SCROLLBAR = 17;

test.skip(({ isMobile }) => isMobile, 'On a phone the sentences wrap.');

for (const width of [761, 992, 1280, 1728]) {
  test(`each sentence of the hero keeps to one line at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/?still');
    // Set as a property, which the site's CSP allows. It refuses a style element added to the page.
    await page.evaluate((px) => {
      document.documentElement.style.paddingRight = `${px}px`;
    }, SCROLLBAR);
    await page.evaluate(() => document.fonts.ready);

    const lines = await page.evaluate(() =>
      [...document.querySelectorAll('.hero-say > *')].map((sentence) => {
        const range = document.createRange();
        range.selectNodeContents(sentence);
        return new Set([...range.getClientRects()].map((rect) => Math.round(rect.top))).size;
      }),
    );
    expect(lines, 'lines of the headline and of the sentence under it').toEqual([1, 1]);
  });
}
