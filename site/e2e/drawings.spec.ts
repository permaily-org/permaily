// What keeps Home's drawings cheap for a slow phone. The browser moves their mail by itself, and a move
// exists only while its drawing is moving and in or near the window: a move that is left waiting costs
// the page a frame of work each time anything else on it is drawn. A drawing that is out of the window is
// not drawn at all, it keeps its size, and a link to a section still lands on it.

import { expect, type Page, test } from '@playwright/test';

/** How many moves the browser is holding for things inside a part of the page. */
const moves = (page: Page, within: string) =>
  page.evaluate(
    (sel) =>
      document.getAnimations().filter((move) => {
        const target = (move.effect as KeyframeEffect | null)?.target;
        return Boolean(target?.closest(sel));
      }).length,
    within,
  );

test('with nothing moving, no move is left waiting anywhere on Home', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/');
  await page.evaluate(() => document.fonts.ready);
  expect(await page.evaluate(() => document.getAnimations().length)).toBe(0);
});

test('the hero gives its moves up when it is scrolled away, and its mail goes on from where it was', async ({
  page,
}) => {
  await page.goto('/');
  await expect.poll(() => moves(page, '.sea-wrap')).toBeGreaterThan(20);
  /** How far the first row of the sea has moved to the left, in pixels. */
  const moved = () =>
    page.evaluate(() => {
      const run = document.querySelector<HTMLElement>('.sea-water .sx');
      return run ? -new DOMMatrixReadOnly(getComputedStyle(run).transform).m41 : 0;
    });
  await page.waitForTimeout(400);
  const before = await moved();
  expect(before).toBeGreaterThan(0);

  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
  await expect.poll(() => moves(page, '.sea-wrap')).toBe(0);

  await page.evaluate(() => window.scrollTo(0, 0));
  await expect.poll(() => moves(page, '.sea-wrap')).toBeGreaterThan(20);
  await page.waitForTimeout(400);
  // The mail has gone on from where it was, and has not started again.
  expect(await moved()).toBeGreaterThan(before);
});

test('a drawing that is out of the window is not drawn, and keeps its size', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/');
  const emblem = page.locator('[data-demo="emblem"]');
  const size = () =>
    emblem.evaluate((el: HTMLElement) => `${el.offsetWidth} by ${el.offsetHeight}`);

  await expect.poll(() => emblem.evaluate((el) => el.style.contentVisibility)).toBe('hidden');
  const away = await size();

  await emblem.scrollIntoViewIfNeeded();
  await expect.poll(() => emblem.evaluate((el) => el.style.contentVisibility)).toBe('');
  expect(await size()).toBe(away);
});

test('a link to a section lands on it, under the header of the page', async ({ page }) => {
  await page.goto('/#security');
  const top = () =>
    page.evaluate(() => document.querySelector('#security')?.getBoundingClientRect().top ?? 999);
  // The page scrolls there smoothly, so the place is asked for until it has arrived.
  await expect.poll(top, { timeout: 8000 }).toBeLessThan(120);
  expect(await top()).toBeGreaterThan(40);
});
