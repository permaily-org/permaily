import { $, $$, out3, reducedMotion, seg, Timeline } from '../timeline';
import { copies, drifting, LOOK, measure, type Stretch, sameStretches, windowOf } from './rows';

/**
 * Home, the hero: mail drifts past the logo's iceberg, and the picture is built from the mail's characters.
 * A character belongs to the tip, the mass or the sea by where its middle is, so the outline steps along the
 * letters and follows them as they drift.
 * It opens readable everywhere. Then one pass runs from the top down: the highlight comes down the tip, row by
 * row, and bars follow through the mass. After that only the drift goes on.
 * The sea answers the pointer: the words it passes over are covered by bars, and come back a moment later.
 * The row of logos under the drawing moves on the same clock, so the one Pause control stops it too.
 *
 * It is made to cost a slow phone as little as it can. The browser moves the mail, the logos and the light
 * in the water by itself, so the script writes nothing to the page for any of them. Once the opening has
 * played, the script only looks, a few times a second, whether a character has crossed the iceberg's
 * outline, and writes a row's window when one has.
 */

type Span = [number, number];

/** Half the logo's gap lies between the waterline and each part of the iceberg. */
const GAP = 1.647 / 2;
/** The iceberg in the logo's units: x from its centre, y up from the waterline. */
const TIP = (
  [
    [-5, 0],
    [-2.5, 4],
    [-1, 2.5],
    [1.5, 7],
    [5, 0],
  ] as Span[]
).map(([x, y]): Span => [x, y + GAP]);
const MASS = (
  [
    [-11, 0],
    [11, 0],
    [12, -4],
    [7.5, -9],
    [5, -8],
    [2.5, -14],
    [-4.5, -9],
    [-9.5, -7],
    [-12, -3.5],
  ] as Span[]
).map(([x, y]): Span => [x, y - GAP]);
/** The drawing is 25.4 units high, and the sea is at its darkest 17.5 units down. */
const HEIGHT = 25.4;
const DARKEST = 17.5;
/** How fast the row of logos under the drawing moves, in pixels a second. */
const NAMES_PACE = 26;
/** How many characters the pointer covers to each side: on its own row, and on the rows above and below. */
const REACH = { own: 4, next: 2 };
/** The most words the pointer keeps covered at once. */
const COVERED = 400;
/** A pace that comes and goes like a wave: slow at both ends, as a sine is. */
const WAVE = 'cubic-bezier(0.37, 0, 0.63, 1)';

/** Where a level line crosses a shape: its stretches, from left to right. */
const crossings = (shape: Span[], y: number): Span[] => {
  const xs: number[] = [];
  for (let a = 0, b = shape.length - 1; a < shape.length; b = a++) {
    const [x1, y1] = shape[a]!;
    const [x2, y2] = shape[b]!;
    if (y1 > y !== y2 > y) xs.push(x1 + ((x2 - x1) * (y - y1)) / (y2 - y1));
  }
  xs.sort((p, q) => p - q);
  const out: Span[] = [];
  for (let k = 0; k + 1 < xs.length; k += 2) out.push([xs[k]!, xs[k + 1]!]);
  return out;
};

/** One row of mail, as the script follows it. The browser moves it. */
interface Track {
  /** Characters in one copy of the row. */
  n: number;
  /** Characters a second. */
  v: number;
  /** Characters the row is ahead by when it starts, so that rows do not all start alike. */
  o: number;
  /** The row's mail in the sea, where the pointer's bars are put. Rows of the tip have none. */
  mail?: HTMLElement;
  /** How far the row has moved within one copy, in pixels, when it was last looked at. */
  along: number;
}

/** A row of the tip or the mass. Its mail shows through a window that is always whole characters wide. */
interface Cut {
  track: Track;
  /** The mail once the pass has reached it: highlighted in the tip, bars in the mass. */
  done: HTMLElement;
  /** The mail before that: readable. */
  plain: HTMLElement;
  /** The row as bars are set from it, when `done` is bars: every character an x, the spaces kept. */
  bars: string;
  /** Where the iceberg crosses the row, in pixels from the left of the drawing. */
  spans: Span[];
  /** The row's place in the opening pass, or -1 where the iceberg does not reach at this size. */
  order: number;
}

/**
 * Pulls a stretch of bars in to its first and last word. A block of the bars font reaches a hair past its own
 * cell, so a window that began on a space would show the edge of the block before it. The spaces it leaves
 * out are empty in the sea behind them too, so nothing else changes.
 */
const toWords = (cells: Stretch[], text: string): Stretch[] =>
  cells.flatMap(([from, to]): Stretch[] => {
    while (from <= to && text[from % text.length] === ' ') from++;
    while (to >= from && text[to % text.length] === ' ') to--;
    return from <= to ? [[from, to]] : [];
  });

export function initSea(root: HTMLElement) {
  // The styles show the mail under the pass, and step their own outlines aside, once the script is there.
  root.dataset.ready = 'true';
  const seaRows = $$('.sea-water .sea-row', root);
  const massRows = $$('.sea-mass .sea-row', root);
  // In the page, the tip's rows are counted up from the waterline.
  const tipRows = $$('.sea-tip .sea-row', root);
  const shafts = $$('.sea-shafts i', root);
  // The row of logos under the drawing. It holds its logos twice, and moves by one copy's length.
  const names = root.parentElement?.querySelector<HTMLElement>('.works-track');

  const track = (el: HTMLElement, mail?: HTMLElement): Track => ({
    n: Number(el.dataset.n),
    v: Number(el.dataset.v),
    o: Number(el.dataset.o) || 0,
    mail,
    along: 0,
  });
  // A row of the mass carries the same mail as the sea row behind it, and moves with it.
  const seaTracks = seaRows.map((row) => track($('.sx', row), $('.sx', row)));
  const tipTracks = tipRows.map((row) => track($('.sx', row)));
  const tracks = [...tipTracks, ...seaTracks];

  // A row of the tip or the mass is cut to a window, and it moves. The two are kept apart: the row rides on
  // a run that the browser moves, and the script cuts the row itself, so neither gets in the other's way.
  const ride = (el: HTMLElement) => {
    const run = document.createElement('span');
    run.className = 'sea-run';
    run.dataset.n = el.dataset.n ?? '';
    run.dataset.v = el.dataset.v ?? '';
    run.dataset.o = el.dataset.o ?? '';
    el.replaceWith(run);
    run.append(el);
    return run;
  };
  const cut = (row: HTMLElement, of: Track): Cut => {
    const done = $('.hl, .br', row);
    return {
      track: of,
      done,
      plain: $('.pl', row),
      bars: done.classList.contains('br') ? (done.dataset.t ?? '') : '',
      spans: [],
      order: -1,
    };
  };
  const tipCuts = tipRows.map((row, j) => cut(row, tipTracks[j]!));
  const massCuts = massRows.map((row, i) => cut(row, seaTracks[i]!));
  const cuts = [...tipCuts, ...massCuts];
  // The mail that shows from the opening on, and the readable mail under the pass, which is needed only
  // until the pass has gone by.
  const mail = drifting(
    [...seaRows.map((row) => $('.sx', row)), ...cuts.map((c) => ride(c.done))],
    -1,
    {
      root,
      field: root,
      nearby: (near) => {
        nearby = near;
        tend();
      },
    },
  );
  const early = drifting(
    cuts.map((c) => ride(c.plain)),
    -1,
    { root },
  );
  let opening = true;

  const T = { start: 900, each: 55, wipe: 420 };
  const duration = T.start + (cuts.length - 1) * T.each + T.wipe;

  // The moves that are not rows of mail: the logos, and the light in the water. They keep the mail's clock,
  // each ahead of it by its own `lead`. Like a row, each has a move only while the drawing is moving and in
  // or near the window. Stopped, it stands where the clock says, by `still`. A move that is left waiting
  // costs the browser a frame of work each time anything else on the page is drawn.
  interface Extra {
    el: HTMLElement;
    frames: Keyframe[];
    timing: OptionalEffectTiming;
    lead: number;
    /** The one property it moves, and its value at a moment of the mail's clock, in seconds. */
    property: string;
    still: (s: number) => string;
    move?: Animation;
  }
  const extras = new Map<string, Extra>();
  let nearby = true;
  /** Gives each of them its move, sets it right, or stands it where the clock says, by what holds now. */
  const tend = () => {
    const moving = timeline?.state === 'playing';
    const s = mail.time / 1000;
    for (const extra of extras.values()) {
      if (!nearby || !moving || !extra.el.animate) {
        extra.move?.cancel();
        extra.move = undefined;
        extra.el.style.setProperty(extra.property, nearby ? extra.still(s) : '');
      } else if (extra.move) {
        const effect = extra.move.effect as KeyframeEffect;
        effect.setKeyframes(extra.frames);
        effect.updateTiming(extra.timing);
      } else {
        extra.move = extra.el.animate(extra.frames, extra.timing);
        extra.move.currentTime = mail.time + extra.lead;
        extra.el.style.removeProperty(extra.property);
      }
    }
  };
  /** Says what one of them does, the first time and whenever it changes. */
  const extra = (key: string, what: Omit<Extra, 'move'>) => {
    extras.set(key, { ...what, move: extras.get(key)?.move });
  };
  /** Sets every clock to a moment: the mail's, and those that keep it. */
  const place = (ms: number) => {
    mail.time = ms;
    early.time = ms;
    tend();
  };

  // What each window was last given. An empty one hides its row.
  const shown = new Map<HTMLElement, Stretch[]>();
  let timeline: Timeline | undefined;
  // The width of one character: three copies of a row's text, over the characters in them.
  const ch = measure(root, $('.sea-water .sx', root), 3, () => {
    // The type changed size: the rows are told, and every window is cut again.
    mail.size(ch());
    early.size(ch());
    shown.clear();
    redraw();
  });
  mail.size(ch());
  early.size(ch());

  let unit = 1;
  let lap = 0;
  const plan = () => {
    lap = names?.firstElementChild instanceof HTMLElement ? names.firstElementChild.offsetWidth : 0;
    const rowHeight = seaRows[0]?.offsetHeight || 15;
    unit = root.clientHeight / HEIGHT;
    const centre = root.clientWidth / 2;
    const at = (shape: Span[], y: number) =>
      crossings(shape, y).map(([a, b]): Span => [centre + a * unit, centre + b * unit]);
    // Rows are counted from the waterline. The row on the waterline itself stays empty.
    tipCuts.forEach((c, j) => {
      c.spans = at(TIP, ((j + 1) * rowHeight) / unit);
    });
    massCuts.forEach((c, i) => {
      c.spans = at(MASS, (-(i + 1) * rowHeight) / unit);
    });
    // The pass goes from the peak of the tip to the keel. Rows the iceberg does not reach at this size are
    // left out, so it never waits on a row nobody can see.
    let order = 0;
    for (const c of [...tipCuts].reverse()) c.order = c.spans.length ? order++ : -1;
    for (const c of massCuts) c.order = c.spans.length ? order++ : -1;
    // Deeper rows are darker, in five steps.
    seaRows.forEach((row, i) => {
      const depth = Math.min(1, ((i + 1) * rowHeight) / unit / DARKEST);
      const level = String(Math.min(4, Math.round(depth * 4.2)));
      if (row.dataset.d !== level) row.dataset.d = level;
    });

    // The logos move left by one copy's length, over and over. With reduced motion they are laid out to be
    // seen at once, so they stay where they are.
    if (names && lap && !reducedMotion) {
      const to = (x: number) => `translate3d(${x.toFixed(2)}px,0,0)`;
      extra('names', {
        el: names,
        frames: [{ transform: to(0) }, { transform: to(-lap) }],
        timing: { duration: (lap / NAMES_PACE) * 1000, iterations: Number.POSITIVE_INFINITY },
        lead: 0,
        property: 'transform',
        still: (s) => to(-((s * NAMES_PACE) % lap)),
      });
    }
    // The light in the water comes and goes, and sways a little, each as a slow wave of its own length. A
    // wave goes one way for half its length and back for the other half, and starts a quarter in, where a
    // sine starts. The sway is a part of the drawing's size.
    const wave = (seconds: number): OptionalEffectTiming => ({
      duration: Math.PI * seconds * 1000,
      direction: 'alternate',
      iterations: Number.POSITIVE_INFINITY,
      easing: WAVE,
    });
    const sway = (to: number) => `translate3d(${(to * 3 * unit).toFixed(1)}px,0,0)`;
    const quarter = Math.PI / 2;
    shafts.forEach((el, i) => {
      const glow = i ? 3.7 : 2.9;
      const slow = i ? 13 : 9;
      extra(`glow ${i}`, {
        el,
        frames: [{ opacity: 0 }, { opacity: 1 }],
        timing: wave(glow),
        lead: (i * 2.1 + quarter) * glow * 1000,
        property: 'opacity',
        still: (s) => (0.5 + 0.5 * Math.sin(s / glow + i * 2.1)).toFixed(3),
      });
      extra(`sway ${i}`, {
        el,
        frames: [{ transform: sway(-1) }, { transform: sway(1) }],
        timing: wave(slow),
        lead: (i * 1.3 + quarter) * slow * 1000,
        property: 'transform',
        still: (s) => sway(Math.sin(s / slow + i * 1.3)),
      });
    });
    tend();
  };
  plan();

  /** The characters of a row that are covered or highlighted, and the ones still readable, at progress p. */
  const windows = (c: Cut, w: number, p: number): [Stretch[], Stretch[]] => {
    const done: Stretch[] = [];
    const plain: Stretch[] = [];
    const first = c.spans[0];
    const last = c.spans[c.spans.length - 1];
    if (!first || !last) return [done, plain];
    // A character counts by its middle, so one is never cut in two.
    const cell = (x: number) => (x + c.track.along) / w - 0.5;
    const edge = first[0] + (last[1] - first[0]) * p;
    const reached = p >= 1 ? Infinity : p <= 0 ? -Infinity : Math.floor(cell(edge));
    for (const [a, b] of c.spans) {
      const from = Math.ceil(cell(a));
      const to = Math.floor(cell(b));
      if (to < from) continue;
      const split = Math.min(to, reached);
      if (split >= from) done.push([from, split]);
      if (split < to) plain.push([Math.max(from, split + 1), to]);
    }
    return [done, plain];
  };

  /** Gives a row its window: stretches of whole characters of its run. It is written only when it changes. */
  const show = (el: HTMLElement, list: Stretch[], w: number) => {
    const last = shown.get(el);
    if (last && sameStretches(last, list)) return;
    shown.set(el, list);
    el.style.visibility = list.length ? '' : 'hidden';
    if (list.length) el.style.clipPath = windowOf(list, w);
  };

  // The words the pointer has covered, by row and place in the row's mail. A bar is a child of its row's mail,
  // so it drifts with the word under it. The styles draw it and fade it out, and it is removed when they end.
  const covers = new Map<string, HTMLElement>();
  const cover = (i: number, cell: number, w: number) => {
    const row = seaTracks[i];
    const text = row?.mail?.dataset.t;
    if (!row?.mail || !text || covers.size >= COVERED) return;
    const at = (c: number) => text[((c % row.n) + row.n) % row.n];
    if (at(cell) === ' ') return;
    // The whole word the character belongs to.
    let from = cell;
    let to = cell;
    while (at(from - 1) !== ' ' && cell - from < row.n) from--;
    while (at(to + 1) !== ' ' && to - cell < row.n) to++;
    const key = `${i}:${((from % row.n) + row.n) % row.n}`;
    if (covers.has(key)) return;
    // A word behind the mass is hidden already.
    const middle = ((from + to + 1) / 2) * w - row.along;
    if (massCuts[i]?.spans.some(([a, b]) => middle > a && middle < b)) return;
    const bar = document.createElement('b');
    bar.textContent = 'x'.repeat(to - from + 1);
    bar.style.left = `${(from * w).toFixed(2)}px`;
    bar.dataset.k = key;
    bar.addEventListener('animationend', () => {
      bar.remove();
      covers.delete(key);
    });
    covers.set(key, bar);
    row.mail.append(bar);
  };
  /** Takes a row's bars away, when its mail has come round and they would no longer be over their words. */
  const uncover = (row: Track) => {
    if (!row.mail?.lastElementChild) return;
    for (const bar of $$('b', row.mail)) {
      covers.delete(bar.dataset.k ?? '');
      bar.remove();
    }
  };

  /** Where every row's mail is now, by the clock the browser moves it on. */
  const follow = (w: number) => {
    const s = mail.time / 1000;
    for (const row of tracks) {
      const before = row.along;
      // The mail moves left, and comes round again after one row's length.
      row.along = ((s * row.v + row.o) * w) % (row.n * w);
      if (row.along < before) uncover(row);
    }
  };

  const render = (t: number) => {
    // The clock places the mail once, before it first moves: at its start, or at a moment asked for in the
    // address. After that the mail is never put back.
    if (!timeline) place(t);
    const w = ch();
    if (!w) return;
    follow(w);
    for (const c of cuts) {
      const at = T.start + c.order * T.each;
      const p = c.order < 0 ? 1 : out3(seg(t, at, at + T.wipe));
      const [done, plain] = windows(c, w, p);
      const whole = Math.floor(c.track.along / w);
      show(c.done, copies(c.bars ? toWords(done, c.bars) : done, whole, c.track.n), w);
      show(c.plain, copies(plain, whole, c.track.n), w);
    }
    // The readable mail under the pass has nothing to show once the pass has gone by, and gives its moves up.
    if (opening !== t < duration) {
      opening = t < duration;
      if (opening) early.time = mail.time;
      early.keep(opening);
    }
    // After the opening a window changes only when a character crosses an edge: looking now and then is enough.
    if (!opening) timeline?.rest(LOOK);
  };

  timeline = new Timeline({
    name: 'sea',
    root,
    duration,
    render,
    threshold: 0.1,
    ambient: true,
    control: $('.demo-ctl', root.parentElement!),
    watch: (moving) => {
      for (const rows of [mail, early]) {
        if (moving) rows.play();
        else rows.pause();
      }
      tend();
    },
  });

  // The pointer covers the words it passes over. Its moves are taken once a frame, and every step of the way
  // from the last place is covered, so a fast pointer leaves no gaps.
  const water = $('.sea-water', root);
  let seen: { x: number; y: number } | undefined;
  let next: { x: number; y: number } | undefined;
  const sweep = () => {
    const w = ch();
    const to = next;
    next = undefined;
    if (!w || !to) return;
    // The rows have moved since the script last looked.
    follow(w);
    const rowHeight = seaRows[0]?.offsetHeight || 15;
    const from = seen ?? to;
    const steps = Math.min(
      60,
      Math.max(1, Math.ceil(Math.hypot(to.x - from.x, to.y - from.y) / (rowHeight / 2))),
    );
    for (let k = 1; k <= steps; k++) {
      const x = from.x + ((to.x - from.x) * k) / steps;
      const y = from.y + ((to.y - from.y) * k) / steps;
      const under = Math.floor(y / rowHeight);
      for (let i = under - 1; i <= under + 1; i++) {
        const row = seaTracks[i];
        if (!row) continue;
        const middle = Math.floor((x + row.along) / w);
        const reach = i === under ? REACH.own : REACH.next;
        for (let c = middle - reach; c <= middle + reach; c++) cover(i, c, w);
      }
    }
    seen = to;
  };
  root.addEventListener('pointermove', (event) => {
    // A finger is scrolling the page, not pointing at a word.
    if (event.pointerType === 'touch') return;
    const box = water.getBoundingClientRect();
    if (!next) requestAnimationFrame(sweep);
    next = { x: event.clientX - box.left, y: event.clientY - box.top };
  });
  root.addEventListener('pointerleave', () => {
    seen = undefined;
  });

  // A drawing that is not playing is drawn again when its size or its font changes.
  function redraw() {
    plan();
    if (timeline && timeline.state !== 'playing') render(timeline.t);
  }
  new ResizeObserver(redraw).observe(root);
  document.fonts?.ready.then(redraw);
}
