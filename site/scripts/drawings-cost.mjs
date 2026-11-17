// What Home's drawings cost a slow phone while they play: a trace of Chrome with its processor slowed down.
// Build and serve the site first (bun run build, bun run preview), then, from site/:
//   node scripts/drawings-cost.mjs [address] [times slower]
// For each drawing it prints the milliseconds of work the page's main thread did in a second of playing,
// how many frames the page itself drew in that second, and how many the screen showed.
// docs/site.md, "What the drawings cost a slow phone", says what to expect.
// It uses the browser Playwright installed. To use the Chrome on this machine, set CHANNEL=chrome.
// Measure on mains power: on a laptop low on battery Chrome saves energy by showing half the frames, and
// every number comes out lower. Two runs differ by a seventh or so, so compare several.
import { chromium } from '@playwright/test';

const [base = 'http://localhost:4321', rate = '6'] = process.argv.slice(2);
/**
 * Each drawing, how long it plays before it is measured, so that its opening is over, and how long it is
 * measured for, in milliseconds. The emblem is measured for two turns of its features, so that the changes
 * from one emblem to the next, which cost the most, are counted in full whenever the measuring starts.
 */
const DRAWINGS = [
  { name: 'sea', lead: 5000, span: 8000 },
  { name: 'line', lead: 2000, span: 8000 },
  { name: 'emblem', lead: 2000, span: 11200 },
];

const browser = await chromium.launch({ channel: process.env.CHANNEL || undefined });
for (const { name, lead, span } of DRAWINGS) {
  const context = await browser.newContext({
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 3,
    isMobile: true,
    hasTouch: true,
    colorScheme: 'dark',
  });
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await page.goto(`${base}/`);
  await page.evaluate(() => document.fonts.ready);
  await page.evaluate(
    (demo) => document.querySelector(`[data-demo="${demo}"]`)?.scrollIntoView({ block: 'center' }),
    name,
  );
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: Number(rate) });
  await page.waitForTimeout(lead);
  // Only the one being measured plays, so that its cost is not mixed with another's that is in view.
  await page.evaluate((demo) => {
    for (const [key, timeline] of Object.entries(window.PermailyDemos ?? {})) {
      if (key !== demo && timeline.state === 'playing') timeline.pause();
    }
  }, name);
  await page.waitForTimeout(600);
  await browser.startTracing(page, {
    categories: [
      '-*',
      'toplevel',
      'devtools.timeline',
      'disabled-by-default-devtools.timeline',
      'disabled-by-default-devtools.timeline.frame',
      'cc',
      'viz',
      'gpu',
    ],
  });
  await page.waitForTimeout(span);
  const playing = await page.evaluate(
    (demo) => window.PermailyDemos?.[demo]?.state === 'playing',
    name,
  );
  const trace = JSON.parse((await browser.stopTracing()).toString());
  await context.close();

  const events = trace.traceEvents ?? trace;
  // The page's own main thread: of the threads named CrRendererMain, the one that did the most.
  const names = new Map();
  for (const e of events) {
    if (e.ph === 'M' && e.name === 'thread_name') names.set(`${e.pid}:${e.tid}`, e.args.name);
  }
  const busy = new Map();
  for (const e of events) {
    const thread = `${e.pid}:${e.tid}`;
    if (e.ph === 'X' && names.get(thread) === 'CrRendererMain') {
      busy.set(thread, (busy.get(thread) ?? 0) + 1);
    }
  }
  const main = [...busy.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
  const onMain = (e) => `${e.pid}:${e.tid}` === main;
  // RunTask is everything the main thread did. It is slowed by the rate, as a slow phone's would be.
  const tasks = events.filter((e) => e.ph === 'X' && onMain(e) && e.name === 'RunTask');
  // A trace goes on a little after it is told to stop, so how long it was is read from the trace itself.
  const from = tasks.reduce((first, e) => Math.min(first, e.ts), Number.POSITIVE_INFINITY);
  const to = tasks.reduce((last, e) => Math.max(last, e.ts + (e.dur ?? 0)), 0);
  const seconds = (to - from) / 1e6;
  const within = (e) => e.ts >= from && e.ts <= to;
  const a = (n) => (n / seconds).toFixed(0);
  const work = tasks.reduce((total, e) => total + (e.dur ?? 0), 0) / 1000;
  const drawn = events.filter((e) => onMain(e) && e.name === 'BeginMainThreadFrame').length;
  const shown = events.filter((e) => e.name === 'DrawFrame' && within(e)).length;
  console.log(
    [
      name.padEnd(7),
      `${a(work).padStart(4)} ms of work a second`,
      `${a(drawn).padStart(3)} frames drawn by the page`,
      `${a(shown).padStart(3)} shown`,
      playing ? '' : 'NOT PLAYING: the numbers above are of a drawing that stood still',
    ].join('   '),
  );
}
await browser.close();
