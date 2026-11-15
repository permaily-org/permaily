// Home's security section: an emblem cut out of rows of mail, beside four features. Picking a feature
// shows its emblem and its words, and leaves the mail as it was. Without motion a change is immediate,
// which is what these ask for.

import { expect, test } from '@playwright/test';

test.describe('with the script', () => {
  test.beforeEach(async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/');
    // The emblem is not drawn, and its mail not placed, until it is about to come into the window.
    await page.locator('[data-demo="emblem"]').scrollIntoViewIfNeeded();
    await expect
      .poll(() =>
        page.evaluate(() => {
          const run = document.querySelector('[data-demo="emblem"] .em-run');
          return run ? getComputedStyle(run).transform : 'none';
        }),
      )
      .not.toBe('none');
  });

  test('picking a feature shows its emblem, and only its own words', async ({ page }) => {
    const features = page.locator('[data-emblem] .feat');
    const emblem = page.locator('[data-demo="emblem"]');

    await expect(features).toHaveCount(4);
    await expect(features.nth(0)).toHaveAttribute('aria-pressed', 'true');
    await expect(emblem).toHaveAttribute('data-shows', 'source');
    await expect(features.nth(0).locator('.d')).toBeVisible();
    await expect(features.nth(2).locator('.d')).toBeHidden();

    await features.nth(2).click();
    await expect(features.nth(2)).toHaveAttribute('aria-pressed', 'true');
    await expect(features.nth(0)).toHaveAttribute('aria-pressed', 'false');
    await expect(emblem).toHaveAttribute('data-shows', 'builds');
    await expect(features.nth(2).locator('.d')).toBeVisible();
    await expect(features.nth(0).locator('.d')).toBeHidden();
  });

  test('a change of emblem moves the windows and leaves the mail alone', async ({ page }) => {
    // A row is a run of mail, which stands at a place, and in it the mail itself, behind a window.
    const rows = () =>
      page.evaluate(() =>
        [...document.querySelectorAll<HTMLElement>('[data-demo="emblem"] .em-run')].map((run) => {
          const mail = run.firstElementChild as HTMLElement;
          return {
            mail: mail.dataset.t,
            place: getComputedStyle(run).transform,
            window: mail.style.visibility === 'hidden' ? '' : mail.style.clipPath,
          };
        }),
      );
    const before = await rows();
    expect(before).toHaveLength(18);
    expect(before.filter((row) => row.window).length).toBeGreaterThan(10);

    await page.locator('[data-emblem] .feat').nth(3).click();
    await expect(page.locator('[data-demo="emblem"]')).toHaveAttribute('data-shows', 'trust');
    const after = await rows();
    expect(after.map((row) => row.mail)).toEqual(before.map((row) => row.mail));
    expect(after.map((row) => row.place)).toEqual(before.map((row) => row.place));
    expect(after.map((row) => row.window)).not.toEqual(before.map((row) => row.window));
  });

  test('the page under the features holds still when another is picked', async ({ page }) => {
    const height = () =>
      page.evaluate(() => document.querySelector('#security')?.getBoundingClientRect().height);
    const first = await height();
    for (const k of [3, 1, 2, 0]) {
      await page.locator('[data-emblem] .feat').nth(k).click();
      await expect(page.locator('[data-emblem] .feat').nth(k)).toHaveAttribute(
        'aria-pressed',
        'true',
      );
      expect(await height(), `with feature ${k + 1} picked`).toBe(first);
    }
  });
});

test.describe('without the script', () => {
  test.use({ javaScriptEnabled: false });

  test('the first emblem is there, and every feature says its piece', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('[data-demo="emblem"] .em-row')).toHaveCount(18);
    expect(await page.locator('[data-demo="emblem"] .em-row .h').count()).toBeGreaterThan(20);
    for (const say of await page.locator('[data-emblem] .feat .d').all()) {
      await expect(say).toBeVisible();
    }
  });
});
