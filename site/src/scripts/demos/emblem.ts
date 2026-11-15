import type { Feature } from '../../data/security';
import { $, $$, clamp, reducedMotion, Timeline } from '../timeline';
import {
  between,
  type Crossing,
  crossings,
  EMBLEMS,
  ROWS,
  stretches,
  stretchesOf,
} from './emblems';
import { copies, drifting, LOOK, measure, type Stretch, sameStretches, windowOf } from './rows';

/**
 * Home, security: an emblem for each of four features, cut out of rows of mail that drift to the left.
 * The mail is one field, and nothing that happens to the emblem touches it: it is never swapped, and it
 * never starts again. A change of feature moves only the outline, which melts from one emblem into the
 * next. Like the hero's iceberg, a character is in or out by where its middle is, so the outline steps along
 * the letters and follows them as they drift.
 * The features take turns until a person picks one. Then that one stays, and the control under the drawing,
 * once it has paused and resumed, starts the turns again from it.
 *
 * It is made to cost a slow phone as little as it can. The browser moves the rows by itself, and fills the
 * line over the feature whose turn it is, so the script writes nothing to the page for either. While an
 * emblem is settled the script only looks, a few times a second, whether a character has crossed the
 * outline, and writes a row's window when one has. Only during the melt, under a second long, does it work
 * more often than that.
 */

/**
 * When the first emblem starts to grow, how long a change takes, and how long a turn is. During a change
 * the outline is drawn again every `melt` milliseconds, which is as often as the sketch it was chosen from.
 */
const T = { start: 250, change: 900, turn: 5600, melt: 33 };

/** A row of the drawing. */
interface Row {
  /** The row's mail, cut to the stretches that show. It rides on a run that the browser moves. */
  el: HTMLElement;
  /** Characters in one copy of the mail. */
  n: number;
  /** The stretches last written as its window, so that nothing is written twice. */
  last: Stretch[] | null;
}

export function initEmblem(root: HTMLElement) {
  const scope = root.closest<HTMLElement>('[data-emblem]') ?? root.parentElement!;
  const picks = $$('.feat', scope);
  const keys = picks.map((pick) => pick.dataset.key as Feature['key']);
  const shapes = keys.map((key) => EMBLEMS[key]);
  const count = shapes.length;

  // From here on the script draws each row. In place of the stretches the page came with, a row gets one
  // run, which the browser moves, and in it the row's mail behind a window.
  const rows: Row[] = [];
  const movers: HTMLElement[] = [];
  for (const line of $$('.em-row', root)) {
    const mail = line.dataset.t ?? '';
    const run = document.createElement('span');
    run.className = 'em-run';
    run.dataset.n = String(mail.length);
    run.dataset.v = line.dataset.v ?? '';
    run.dataset.o = line.dataset.o ?? '';
    const el = document.createElement('span');
    el.className = 'em-x';
    el.dataset.n = String(mail.length);
    el.dataset.t = mail;
    el.style.visibility = 'hidden';
    run.append(el);
    line.replaceChildren(run);
    movers.push(run);
    rows.push({ el, n: mail.length, last: null });
  }
  const sample = rows[0]?.el;
  if (!sample) return;
  const runs = drifting(movers, -1, { root, field: root });

  /** Where each settled emblem crosses each row. Found the first time the emblem settles, and kept. */
  const found = new Map<number, Crossing[][]>();
  const cutsOf = (k: number) => {
    let cuts = found.get(k);
    if (!cuts) {
      const shape = shapes[k]!;
      cuts = rows.map((_, r) => crossings(shape, r));
      found.set(k, cuts);
    }
    return cuts;
  };

  /** The emblems taking turns: `first` began to arrive at `since`, out of `from`. A `from` of -1 is nothing. */
  let turns: { first: number; since: number; from: number } | null = {
    first: 0,
    since: T.start,
    from: -1,
  };
  /** An emblem a person picked: it began to arrive at `at`, out of `from`, and it stays. */
  let picked: { to: number; at: number; from: number } | null = null;

  /** What shows at a moment: emblem `to`, arriving out of `from` since `at`, on its turn or picked. */
  const showing = (t: number) => {
    if (picked && t >= picked.at) return { ...picked, turn: false };
    // A moment before anything that was set: the clock was taken back to the start.
    if (picked || !turns || t < turns.since - T.start) {
      picked = null;
      turns = { first: 0, since: T.start, from: -1 };
    }
    const n = Math.max(0, Math.floor((t - turns.since) / T.turn));
    return {
      to: (turns.first + n) % count,
      at: turns.since + n * T.turn,
      from: n ? (turns.first + n - 1) % count : turns.from,
      turn: true,
    };
  };

  /**
   * The line over the feature whose turn it is. While the drawing moves, the browser fills it by itself
   * over the length of a turn. Stopped part of the way, it stands as far as it had come: like a row, it has
   * a move only while it is moving. `k` is -1 when no feature is on its turn, and the styles draw the line.
   */
  const bars = picks.map((pick) => pick.querySelector<HTMLElement>('.bar'));
  let fill: { k: number; move: Animation } | null = null;
  let stood = -1;
  const fillFor = (k: number, elapsed: number, moving: boolean) => {
    if (fill && (fill.k !== k || !moving)) {
      fill.move.cancel();
      fill = null;
    }
    if (stood >= 0 && (stood !== k || moving)) {
      bars[stood]?.style.removeProperty('transform');
      stood = -1;
    }
    const bar = bars[k];
    if (!bar) return;
    if (!moving || !bar.animate) {
      bar.style.transform = `scaleX(${clamp(elapsed / T.turn).toFixed(4)})`;
      stood = k;
    } else if (!fill) {
      const frames = [{ transform: 'scaleX(0)' }, { transform: 'scaleX(1)' }];
      fill = { k, move: bar.animate(frames, { duration: T.turn, fill: 'both' }) };
      fill.move.currentTime = elapsed;
    } else if (Math.abs(Number(fill.move.currentTime) - elapsed) > 60) {
      // The line keeps its own time. It is put right if it has come apart from the clock.
      fill.move.currentTime = elapsed;
    }
  };

  // The width of one character, from a row's mail, and the height of a row.
  let lh = 0;
  const plan = () => {
    lh = sample.parentElement?.parentElement?.getBoundingClientRect().height ?? 0;
  };
  let pressed = -1;
  let timeline: Timeline | undefined;
  const ch = measure(root, sample, 2, () => {
    // The type changed size: the runs are told, and every window is cut again.
    runs.size(ch());
    plan();
    for (const row of rows) row.last = null;
    if (timeline && timeline.state !== 'playing') render(timeline.t);
  });
  runs.size(ch());
  plan();

  const render = (t: number) => {
    // The clock places the mail once, before it first moves: at its start, or at a moment asked for in the
    // address. After that the mail is never put back, whatever happens to the emblem.
    if (!timeline) runs.time = t;
    const w = ch();
    if (!w || !lh) return;
    const moving = timeline?.state === 'playing';
    const still = reducedMotion && !moving;
    const { to, from, at, turn } = showing(t);
    const p = still ? 1 : (t - at) / T.change;
    // A character's width in rows, and the characters of a row that can be in the drawing.
    const cell = w / lh;
    const cols = Math.ceil(ROWS / cell) + 1;
    // While one emblem melts into the next, every character asks the two shapes where it is. Once the
    // emblem has settled, where it crosses each row answers for all of them.
    const inside = p < 1 ? between(shapes[from] ?? null, shapes[to]!, p) : null;
    const cuts = inside ? null : cutsOf(to);
    const time = runs.time;

    rows.forEach((row, r) => {
      const along = runs.along(r, time);
      const whole = Math.floor(along);
      const shift = along - whole;
      const seen = inside
        ? stretches(inside, r, cell, shift, cols)
        : stretchesOf(cuts?.[r] ?? [], cell, shift, cols);
      // The window is in whole characters of the run, so it moves with the letters, and is written again
      // only when a character crosses an edge.
      const list = copies(
        seen.map(([first, last]): Stretch => [first + whole, last + whole]),
        whole,
        row.n,
      );
      if (row.last && sameStretches(list, row.last)) return;
      row.last = list;
      row.el.style.visibility = list.length ? '' : 'hidden';
      if (list.length) row.el.style.clipPath = windowOf(list, w);
    });

    if (to !== pressed) {
      pressed = to;
      picks.forEach((pick, k) => pick.setAttribute('aria-pressed', String(k === to)));
      root.dataset.shows = keys[to];
    }
    // The line over a feature fills while it waits its turn. Picked, or with nothing moving, it is full.
    fillFor(turn && !still ? to : -1, t - at, moving);
    // A settled emblem changes only when a character crosses its outline, so it is enough to look now and then.
    timeline?.rest(p >= 1 ? LOOK : T.melt);
  };

  // The styles hide the words of the features that do not show only once the script is there to show them.
  scope.dataset.ready = 'true';
  const control = $('.demo-ctl', root.parentElement!);
  timeline = new Timeline({
    name: 'emblem',
    root,
    duration: T.start + T.change,
    render,
    threshold: 0.35,
    ambient: true,
    control,
    watch: (moving) => {
      if (moving) runs.play();
      else runs.pause();
      // Stopped, the line over the feature stands where it was. Started, the next frame sets it going.
      if (!moving && timeline) render(timeline.t);
    },
  });

  picks.forEach((pick, k) =>
    pick.addEventListener('click', () => {
      if (!timeline) return;
      const t = timeline.t;
      const now = showing(t);
      turns = null;
      if (timeline.state !== 'playing') {
        // Nothing is moving: the emblem is there at once.
        picked = { to: k, at: t - T.change, from: -1 };
        timeline.seek(t);
        return;
      }
      // Picking the one that shows keeps it as it is, without drawing it again.
      picked =
        now.to === k ? { to: k, at: now.at, from: now.from } : { to: k, at: t, from: now.to };
      render(t);
    }),
  );

  // Resuming starts the turns again from the emblem that shows, which then waits its time like any other.
  control?.addEventListener('click', () => {
    if (!timeline || !picked || timeline.state !== 'playing') return;
    turns = { first: picked.to, since: timeline.t - T.change, from: -1 };
    picked = null;
  });

  // Only the feature that shows says its piece, and the pieces are not the same length. So the list is given
  // the height of its tallest state, and the page under it holds still while the features take their turns.
  const list = $('.trust-list', scope);
  const says = picks.map((pick) => $('.d', pick));
  let width = -1;
  const reserve = () => {
    width = list.offsetWidth;
    list.style.minHeight = '';
    for (const say of says) say.style.display = 'block';
    const heights = says.map((say) => say.getBoundingClientRect().height);
    for (const say of says) say.style.display = '';
    const tallest = Math.max(0, ...heights);
    // Heights come in parts of a pixel. Rounded up, the list is never lower than its tallest state.
    const now = list.getBoundingClientRect().height;
    list.style.minHeight = `${Math.ceil(now - (heights[pressed] ?? 0) + tallest)}px`;
  };
  reserve();

  // A drawing that is not playing is drawn again when its size or its font changes.
  const redraw = () => {
    plan();
    if (list.offsetWidth !== width) reserve();
    if (timeline && timeline.state !== 'playing') render(timeline.t);
  };
  new ResizeObserver(redraw).observe(scope);
  document.fonts?.ready.then(() => {
    reserve();
    redraw();
  });
}
