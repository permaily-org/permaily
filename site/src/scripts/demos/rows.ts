// Rows of sample mail that drift, shared by the drawings on Home.
// A row holds its text more than once, so it can move by one copy's length and look the same again.
// Nothing here runs until it is called, so the build and the tests can read it too.

interface Run {
  el: HTMLElement;
  /** Characters in one copy of the row. */
  n: number;
  /** Characters a second. */
  v: number;
  /** Characters the row is ahead by when it starts, so that rows do not all start alike. */
  o: number;
}

/**
 * How often a drawing that has settled looks whether a character has crossed one of its edges, in
 * milliseconds. Between two looks the mail moves half a pixel at the most, so an edge steps no later than
 * that, and the page is left alone the rest of the time.
 */
export const LOOK = 50;

/**
 * Rows whose mail the browser moves by itself. Each run slides by one copy's length at its own steady pace
 * and starts over, which looks the same, since a run holds its mail more than once. The moves are handed
 * over when the rows start, and nothing is written to the page while they go on, so the script has no work
 * left to do for them. Rows that show the same mail in different layers carry the same numbers, which keeps
 * them in step. A direction of -1 moves the mail left, and 1 moves it right.
 *
 * A row has a move only while it is moving. Stopped, it stands where the clock says, as a plain place on
 * the page: a move that is left waiting costs the browser a frame of work each time anything else on the
 * page is drawn.
 *
 * A moving row is also a layer of its own, and every layer adds to the cost of each frame the page draws,
 * whoever asked for the frame. So a row is hinted as a layer only while it moves, and a stopped row is
 * drawn with the rest of the page, which also sets its text as sharp as it can be.
 *
 * The same goes for the rows themselves: the more there is on a page, the longer each of its frames takes,
 * and these drawings are hundreds of rows. So while the drawing at `root` is out of the window, with a
 * quarter of the window's height to spare, the browser is told not to draw `field`, the part that holds the
 * rows, at all. Its size is fixed by the styles, so the page does not move. `nearby` is told each time that
 * changes, for whatever else the drawing moves.
 */
export function drifting(
  els: HTMLElement[],
  direction: 1 | -1,
  {
    root,
    field,
    nearby,
  }: { root?: HTMLElement; field?: HTMLElement; nearby?: (near: boolean) => void } = {},
) {
  const runs: (Run & { move?: Animation })[] = els.map((el) => ({
    el,
    n: Number(el.dataset.n),
    v: Number(el.dataset.v),
    o: Number(el.dataset.o) || 0,
  }));
  /** How long a run would have taken to get as far ahead as it starts, in milliseconds. */
  const lead = (run: Run) => (run.o / run.v) * 1000;
  // The rows' clock is kept here while there is no move to keep it.
  let held = 0;
  let moving = false;
  let ch = 0;
  let near = true;
  let wanted = true;

  const clock = () => {
    const first = runs.find((run) => run.move);
    return first?.move ? Number(first.move.currentTime) - lead(first) : held;
  };

  /** Gives each row its move, sets it right, or stands it where the clock says, by what holds now. */
  const settle = () => {
    held = clock();
    for (const run of runs) {
      const loop = run.n * ch;
      // A row that is not on the page at all, as some are on a phone, is left alone: a move running on it
      // would have the browser work out the page's styles again on every frame, for nothing.
      const there = near && wanted && loop > 0 && run.v > 0 && run.el.getClientRects().length > 0;
      // A row that is not moving is not hinted as a layer. One that is, the styles decide.
      run.el.style.willChange = moving && near && wanted ? '' : 'auto';
      if (!there || !moving || !run.el.animate) {
        run.move?.cancel();
        run.move = undefined;
        if (!there) {
          run.el.style.transform = '';
          continue;
        }
        const along = (((held / 1000) * run.v + run.o) * ch) % loop;
        const x = direction < 0 ? -along : along - loop;
        run.el.style.transform = `translate3d(${x.toFixed(2)}px,0,0)`;
        continue;
      }
      const far = `translate3d(${(-loop).toFixed(2)}px,0,0)`;
      const frames = [{ transform: 'translate3d(0,0,0)' }, { transform: far }];
      if (direction > 0) frames.reverse();
      if (run.move) {
        (run.move.effect as KeyframeEffect).setKeyframes(frames);
        continue;
      }
      run.move = run.el.animate(frames, {
        duration: (run.n / run.v) * 1000,
        iterations: Number.POSITIVE_INFINITY,
      });
      run.move.currentTime = held + lead(run);
      run.el.style.transform = '';
    }
  };

  if (root && typeof IntersectionObserver !== 'undefined') {
    // A quarter of the window's height of warning, so the rows are drawn before any of them is seen.
    new IntersectionObserver(
      (entries) => {
        const last = entries[entries.length - 1];
        if (!last || last.isIntersecting === near) return;
        near = last.isIntersecting;
        // Drawn again first, so the rows can be placed. Placed nowhere first, and then not drawn.
        if (near) field?.style.removeProperty('content-visibility');
        settle();
        if (!near) field?.style.setProperty('content-visibility', 'hidden');
        nearby?.(near);
      },
      { rootMargin: '25% 0px' },
    ).observe(root);
  }

  return {
    /** Says how wide a character is, in pixels. Call it again whenever that changes, or a row comes or goes. */
    size(width: number) {
      ch = width;
      settle();
    },
    /** Says whether the rows have anything to show. Rows that have not give their moves up until they do. */
    keep(on: boolean) {
      if (on === wanted) return;
      wanted = on;
      settle();
    },
    /** Starts the rows, and stops them. They go on from where they stopped. */
    play() {
      if (moving) return;
      moving = true;
      settle();
    },
    pause() {
      if (!moving) return;
      // The clock is read while the moves still keep it, and then they are given up.
      held = clock();
      moving = false;
      settle();
    },
    /** The rows' clock, in milliseconds. Setting it places every row. */
    get time() {
      return clock();
    },
    set time(ms: number) {
      held = ms;
      let ran = false;
      for (const run of runs) {
        if (!run.move) continue;
        run.move.currentTime = ms + lead(run);
        ran = true;
      }
      if (!ran) settle();
    },
    /** How far a row has moved within one copy at a moment of that clock, in characters. */
    along(i: number, time: number) {
      const run = runs[i];
      return run ? ((time / 1000) * run.v + run.o) % run.n : 0;
    },
  };
}

/**
 * The width of one character, read from a row that holds its text `copies` times.
 * It is measured again when the drawing changes size or its font arrives, and `changed` is told when the
 * width is no longer what it was.
 */
export function measure(
  root: HTMLElement,
  sample: HTMLElement,
  copies: number,
  changed?: () => void,
) {
  let ch = 0;
  let first = true;
  const read = () => {
    const before = ch;
    ch = sample.getBoundingClientRect().width / (copies * Number(sample.dataset.n));
    // Whoever placed something by the old width is told. Not the first time, when nothing was placed yet.
    if (!first && ch !== before) changed?.();
    first = false;
  };
  read();
  new ResizeObserver(read).observe(root);
  document.fonts?.ready.then(read);
  return () => ch;
}

/** Characters of a row that show, from the first to the last of a stretch. */
export type Stretch = [first: number, last: number];

/** Whether two lists of stretches are the same, so that a window is written only when it changes. */
export const sameStretches = (a: Stretch[], b: Stretch[]) =>
  a.length === b.length && a.every(([first, last], i) => first === b[i]?.[0] && last === b[i]?.[1]);

/** How many characters before a run starts over its window is cut in the copy before as well. */
const NEAR = 64;

/**
 * A row's window for a run that the browser moves. The stretches are characters of the run, and `whole` is
 * the character of the run that stands at the row's left edge. A run holds its mail more than once, `n`
 * characters each time, and the browser moves it by one copy and starts it over with no word to the script.
 * So while the start-over is near, the window is cut in the copy before as well, and whichever of the two
 * the browser is showing at that moment, the window is there. The same holds, for a moment, just after it.
 */
export const copies = (list: Stretch[], whole: number, n: number): Stretch[] => {
  const before: Stretch[] = [];
  const after: Stretch[] = [];
  if (whole >= n - NEAR) {
    for (const [first, last] of list) {
      if (last - n >= 0) before.push([Math.max(0, first - n), last - n]);
    }
  }
  if (whole < 2) for (const [first, last] of list) after.push([first + n, last + n]);
  return before.length || after.length ? [...before, ...list, ...after] : list;
};

/** A row's window as CSS: its stretches, at `w` pixels to a character. Empty when nothing shows. */
export const windowOf = (list: Stretch[], w: number) => {
  const px = (cell: number) => `${(cell * w).toFixed(2)}px`;
  const [one] = list;
  if (!one) return '';
  if (list.length === 1) return `inset(0 calc(100% - ${px(one[1] + 1)}) 0 ${px(one[0])})`;
  // Each stretch is a box from the row's foot up to its top and back, and the boxes are joined along the foot.
  const corners = list.flatMap(([first, last]) => [
    `${px(first)} 100%`,
    `${px(first)} 0`,
    `${px(last + 1)} 0`,
    `${px(last + 1)} 100%`,
  ]);
  return `polygon(${corners.join(', ')})`;
};
