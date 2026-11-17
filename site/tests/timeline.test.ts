// The animation engine (src/scripts/timeline.ts), run against a clock the test controls.
// No browser is needed: the few things the engine asks of one are stood in for here.

import { beforeEach, describe, expect, it, vi } from 'vitest';

type Frame = (now: number) => void;
type Seen = (entries: { isIntersecting: boolean }[]) => void;

let now = 0;
let frames: Frame[] = [];
let seen: Seen = () => {};
/** The rests the engine has asked for: what to do when each ends, and how long it is. */
let rests: { end: () => void; ms: number }[] = [];

/** Moves the clock on, one frame at a time. */
const tick = (ms: number, steps = 1) => {
  for (let i = 0; i < steps; i++) {
    now += ms;
    const due = frames;
    frames = [];
    for (const frame of due) frame(now);
  }
};

const root = () => ({ dataset: {} }) as unknown as HTMLElement;

const control = () => {
  const listeners: (() => void)[] = [];
  return {
    textContent: '',
    addEventListener: (_type: string, listener: () => void) => listeners.push(listener),
    click: () => {
      for (const listener of listeners) listener();
    },
  };
};

const load = async (options: { reducedMotion?: boolean } = {}) => {
  vi.resetModules();
  vi.stubGlobal('matchMedia', () => ({ matches: options.reducedMotion ?? false }));
  return (await import('../src/scripts/timeline')).Timeline;
};

beforeEach(() => {
  now = 0;
  frames = [];
  seen = () => {};
  vi.stubGlobal('window', globalThis);
  vi.stubGlobal('location', { search: '' });
  vi.stubGlobal('performance', { now: () => now });
  vi.stubGlobal('requestAnimationFrame', (frame: Frame) => frames.push(frame));
  vi.stubGlobal('cancelAnimationFrame', () => {
    frames = [];
  });
  rests = [];
  vi.stubGlobal('setTimeout', (end: () => void, ms: number) => rests.push({ end, ms }));
  vi.stubGlobal('clearTimeout', () => {
    rests = [];
  });
  vi.stubGlobal(
    'IntersectionObserver',
    class {
      constructor(callback: Seen) {
        seen = callback;
      }
      observe() {}
    },
  );
});

describe('an animation with an end', () => {
  it('plays once it is in view, and rests on its result', async () => {
    const Timeline = await load();
    const timeline = new Timeline({ name: 'once', root: root(), duration: 1000, render: () => {} });
    expect(timeline.t).toBe(0);

    seen([{ isIntersecting: true }]);
    tick(16, 100);

    expect(timeline.t).toBe(1000);
    expect(timeline.state).toBe('end');
    expect(frames).toHaveLength(0);
  });
});

describe('ambient motion', () => {
  it('keeps running after its opening has played', async () => {
    const Timeline = await load();
    const times: number[] = [];
    const timeline = new Timeline({
      name: 'drift',
      root: root(),
      duration: 1000,
      render: (t) => times.push(t),
      ambient: true,
    });

    seen([{ isIntersecting: true }]);
    tick(16, 200);

    expect(timeline.t).toBeCloseTo(3200, 0);
    expect(timeline.state).toBe('playing');
    expect(Math.max(...times)).toBeGreaterThan(1000);
  });

  it('pauses when it is scrolled away, and goes on when it comes back', async () => {
    const Timeline = await load();
    const timeline = new Timeline({
      name: 'drift',
      root: root(),
      duration: 0,
      render: () => {},
      ambient: true,
    });
    seen([{ isIntersecting: true }]);
    tick(16, 10);

    seen([{ isIntersecting: false }]);
    const held = timeline.t;
    tick(16, 10);
    expect(timeline.state).toBe('paused');
    expect(timeline.t).toBe(held);

    seen([{ isIntersecting: true }]);
    tick(16, 10);
    expect(timeline.state).toBe('playing');
    expect(timeline.t).toBeGreaterThan(held);
  });

  it('can be paused and resumed by the visitor', async () => {
    const Timeline = await load();
    const button = control();
    const timeline = new Timeline({
      name: 'drift',
      root: root(),
      duration: 0,
      render: () => {},
      ambient: true,
      control: button as unknown as HTMLElement,
    });
    seen([{ isIntersecting: true }]);
    tick(16, 5);
    expect(button.textContent).toBe('Pause');

    button.click();
    const held = timeline.t;
    tick(16, 5);
    expect(timeline.state).toBe('paused');
    expect(timeline.t).toBe(held);
    expect(button.textContent).toBe('Resume');

    button.click();
    tick(16, 5);
    expect(timeline.state).toBe('playing');
    expect(timeline.t).toBeGreaterThan(held);
  });

  it('stays still for a visitor who asked for reduced motion', async () => {
    const Timeline = await load({ reducedMotion: true });
    const timeline = new Timeline({
      name: 'drift',
      root: root(),
      duration: 1000,
      render: () => {},
      ambient: true,
    });

    tick(16, 10);

    expect(timeline.t).toBe(1000);
    expect(timeline.state).toBe('end');
    expect(frames).toHaveLength(0);
  });
});

describe('motion that the browser runs by itself', () => {
  /** A drawing that asks to rest half a second each time it is drawn, and notes when it was. */
  const resting = async (duration: number) => {
    const Timeline = await load();
    const times: number[] = [];
    const told: boolean[] = [];
    const button = control();
    let timeline: InstanceType<typeof Timeline> | undefined;
    timeline = new Timeline({
      name: 'rest',
      root: root(),
      duration,
      ambient: true,
      control: button as unknown as HTMLElement,
      render: (t) => {
        times.push(t);
        timeline?.rest(500);
      },
      watch: (moving) => told.push(moving),
    });
    return { timeline, times, told, button };
  };

  it('is left alone for as long as its drawing says nothing will change', async () => {
    const { timeline, times } = await resting(0);
    seen([{ isIntersecting: true }]);
    tick(16);

    // It was drawn once, and asked for no frame: only to be woken a little before the rest is over.
    const drawn = times.length;
    expect(frames).toHaveLength(0);
    expect(rests.map((rest) => rest.ms)).toEqual([492]);

    now += 492;
    rests.shift()?.end();
    expect(frames).toHaveLength(1);
    tick(8);

    // The next frame draws it again, and the clock has gone on by the whole rest.
    expect(times).toHaveLength(drawn + 1);
    expect(timeline.t).toBeCloseTo(516, 0);
  });

  it('does not rest while its opening plays', async () => {
    const { timeline, times } = await resting(1000);
    seen([{ isIntersecting: true }]);
    tick(16, 20);

    expect(rests).toHaveLength(0);
    expect(times.length).toBeGreaterThan(20);
    expect(timeline.t).toBeCloseTo(320, 0);
  });

  it('does not count a stall as a rest', async () => {
    const { timeline } = await resting(0);
    seen([{ isIntersecting: true }]);
    tick(16);
    rests.shift()?.end();

    // The page was busy for three seconds. The clock goes on by the rest and one frame's grace, no more.
    tick(3000);
    expect(timeline.t).toBeCloseTo(16 + 500 + 64, 0);
  });

  it('drops its rest when it is paused', async () => {
    const { timeline, button } = await resting(0);
    seen([{ isIntersecting: true }]);
    tick(16);
    expect(rests).toHaveLength(1);

    button.click();
    expect(timeline.state).toBe('paused');
    expect(rests).toHaveLength(0);
    expect(frames).toHaveLength(0);
  });

  it('tells its drawing when it starts and stops moving', async () => {
    const { told, button } = await resting(0);
    expect(told).toEqual([]);

    seen([{ isIntersecting: true }]);
    tick(16);
    expect(told).toEqual([true]);

    button.click();
    expect(told).toEqual([true, false]);

    button.click();
    expect(told).toEqual([true, false, true]);

    seen([{ isIntersecting: false }]);
    expect(told).toEqual([true, false, true, false]);
  });
});
