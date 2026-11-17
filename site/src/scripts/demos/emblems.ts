// The emblems of Home's security section: a shape for each of its four features, and how one becomes the
// next. Nothing here touches the page, so the build draws the first emblem with it, the script draws all of
// them, and the tests check both.
//
// A shape answers with a distance: how far a place is from its edge, below zero inside. That is what lets
// one emblem melt into another. Part of the way between two, a place is inside when the distance between
// theirs is below zero, so the edge flows from one outline to the other.
//
// A place is x rows from the middle of the drawing and y rows down from its top. The drawing is 18 rows
// high and as wide. Every shape keeps within 8.75 rows of the middle, so a character that is half in never
// reaches past the drawing's side.

import type { Feature } from '../../data/security';
import type { Stretch } from './rows';

export const ROWS = 18;
export const MID = ROWS / 2;
/** How wide a character of the mono face is, in rows: 0.6 of the type's size, where a row is 10/7 of it. */
export const CELL = 0.6 / (10 / 7);
/** The characters of a row that can be in the drawing: one more than fit, since they drift by part of one. */
export const COLS = Math.ceil(ROWS / CELL) + 1;

export type Shape = (x: number, y: number) => number;
type Point = [x: number, y: number];

/** How far a place is from the stretch between two points. */
const toStretch = (x: number, y: number, [ax, ay]: Point, [bx, by]: Point) => {
  const ex = bx - ax;
  const ey = by - ay;
  const along = Math.min(1, Math.max(0, ((x - ax) * ex + (y - ay) * ey) / (ex * ex + ey * ey)));
  return Math.hypot(x - ax - along * ex, y - ay - along * ey);
};

/** A stroke through points, `half` thick to each side, with round ends. */
const stroke =
  (points: Point[], half: number): Shape =>
  (x, y) => {
    let nearest = Number.POSITIVE_INFINITY;
    for (let i = 0; i + 1 < points.length; i++) {
      nearest = Math.min(nearest, toStretch(x, y, points[i]!, points[i + 1]!));
    }
    return nearest - half;
  };

/** A box between two corners, with its own corners rounded by `round`. */
const box =
  (x0: number, y0: number, x1: number, y1: number, round = 0): Shape =>
  (x, y) => {
    const dx = Math.abs(x - (x0 + x1) / 2) - (x1 - x0) / 2 + round;
    const dy = Math.abs(y - (y0 + y1) / 2) - (y1 - y0) / 2 + round;
    return Math.hypot(Math.max(dx, 0), Math.max(dy, 0)) + Math.min(Math.max(dx, dy), 0) - round;
  };

const disc =
  (cx: number, cy: number, r: number): Shape =>
  (x, y) =>
    Math.hypot(x - cx, y - cy) - r;

/** Several shapes as one. */
const all =
  (...shapes: Shape[]): Shape =>
  (x, y) => {
    let nearest = Number.POSITIVE_INFINITY;
    for (const shape of shapes) nearest = Math.min(nearest, shape(x, y));
    return nearest;
  };

/** A shape with another left out of it. */
const without =
  (shape: Shape, hole: Shape): Shape =>
  (x, y) =>
    Math.max(shape(x, y), -hole(x, y));

// Open source: a pair of code brackets with a slash between them.
const BRACKET: Point[] = [
  [-3.3, 3.5],
  [-7.5, 9],
  [-3.3, 14.5],
];
const source = all(
  stroke(BRACKET, 1.25),
  stroke(
    BRACKET.map(([x, y]): Point => [-x, y]),
    1.25,
  ),
  stroke(
    [
      [1.75, 2.3],
      [-1.75, 15.7],
    ],
    1.25,
  ),
);

// Self-hosted: three servers in a rack, each four rows high, with its two lights left out.
const slab = (top: number) => box(-8.2, top, 8.2, top + 4, 1.2);
const lights = (top: number) =>
  all(box(-6.2, top + 1, -4.1, top + 3), box(-3.2, top + 1, -1.1, top + 3));
const hosted = without(all(slab(2), slab(7), slab(12)), all(lights(2), lights(7), lights(12)));

// Verifiable builds: a seal, with a check mark left out of it.
const builds = without(
  disc(0, MID, 8.3),
  stroke(
    [
      [-4.3, 9.4],
      [-1.5, 12.2],
      [4.7, 5.5],
    ],
    1.05,
  ),
);

// Zero trust: a padlock. A shackle that arches over a body, and a keyhole left out of the body.
const shackle: Shape = (x, y) =>
  Math.min(
    y <= 5 ? Math.abs(Math.hypot(x, y - 5) - 3.7) : Number.POSITIVE_INFINITY,
    toStretch(x, y, [-3.7, 5], [-3.7, 8.6]),
    toStretch(x, y, [3.7, 5], [3.7, 8.6]),
  ) - 1.05;
const trust = without(
  all(box(-6.3, 8, 6.3, 17, 1.3), shackle),
  all(disc(0, 11.4, 1.35), box(-0.62, 11.7, 0.62, 14.7)),
);

export const EMBLEMS: Record<Feature['key'], Shape> = { source, hosted, builds, trust };

/** Slow at both ends. */
export const ease = (p: number) => {
  const v = Math.min(1, Math.max(0, p));
  return v < 0.5 ? 4 * v ** 3 : 1 - (-2 * v + 2) ** 3 / 2;
};

/** How far from the middle an emblem that comes from nothing has grown at the end, in rows: past its corners. */
const GROWN = 13.5;

/**
 * What is inside the drawing `p` of the way through a change, from 0 to 1: `from` melting into `to`.
 * From nothing, `to` grows out of the middle.
 */
export const between = (
  from: Shape | null,
  to: Shape,
  p: number,
): ((x: number, y: number) => boolean) => {
  const e = ease(p);
  if (e >= 1 || from === to) return (x, y) => to(x, y) <= 0;
  if (!from) return (x, y) => Math.hypot(x, y - MID) <= e * GROWN && to(x, y) <= 0;
  return (x, y) => {
    const d = from(x, y);
    return d + (to(x, y) - d) * e <= 0;
  };
};

/**
 * The characters of a row that are inside, as stretches of whole characters. A character counts by its
 * middle, so none is cut in two. `cell` is a character's width in rows, and `shift` is how far the row's
 * mail has drifted past a whole character, as a part of one.
 */
export const stretches = (
  inside: (x: number, y: number) => boolean,
  row: number,
  cell = CELL,
  shift = 0,
  cols = COLS,
): Stretch[] => {
  const found: Stretch[] = [];
  const y = row + 0.5;
  let first = -1;
  for (let c = 0; c <= cols; c++) {
    const on = c < cols && inside((c + 0.5 - shift) * cell - MID, y);
    if (on && first < 0) first = c;
    if (!on && first >= 0) {
      found.push([first, c - 1]);
      first = -1;
    }
  }
  return found;
};

/** A stretch of a row that is inside a shape, from one edge to the other, in rows from the middle. */
export type Crossing = [from: number, to: number];

/**
 * Where a shape crosses a row. An emblem that has settled does not change, so this is found once for it,
 * and from then on the characters that are inside follow from where the mail has drifted to, with no shape
 * to ask: see `stretchesOf`. The row is walked from left to right. A shape says how far a place is from its
 * edge, and it never says further than it is, so each step can be that long and still pass over nothing:
 * not even the sliver between two parts that almost touch. Each edge is then closed in on.
 */
export const crossings = (shape: Shape, row: number): Crossing[] => {
  const y = row + 0.5;
  const inside = (x: number) => shape(x, y) <= 0;
  /** The shortest step, in rows: far finer than a character, which is the least that can show. */
  const LEAST = 0.001;
  const end = MID + 0.5;
  const edge = (out: number, into: number) => {
    let a = out;
    let b = into;
    for (let i = 0; i < 32; i++) {
      const mid = (a + b) / 2;
      if (inside(mid)) b = mid;
      else a = mid;
    }
    return b;
  };
  const found: Crossing[] = [];
  let from: number | null = null;
  let before = -end;
  for (let x = -end; x <= end; ) {
    const far = shape(x, y);
    if (far <= 0 && from === null) from = edge(before, x);
    if (far > 0 && from !== null) {
      found.push([from, edge(x, before)]);
      from = null;
    }
    before = x;
    x += Math.max(Math.abs(far), LEAST);
  }
  return found;
};

/**
 * The same stretches of whole characters as `stretches` finds, for a shape whose crossings of the row are
 * known. A character's middle is at `(c + 0.5 - shift) * cell - MID`, so the characters between two edges
 * follow from the edges alone.
 */
export const stretchesOf = (cross: Crossing[], cell = CELL, shift = 0, cols = COLS): Stretch[] => {
  const found: Stretch[] = [];
  for (const [from, to] of cross) {
    const first = Math.max(0, Math.ceil((from + MID) / cell - 0.5 + shift));
    const last = Math.min(cols - 1, Math.floor((to + MID) / cell - 0.5 + shift));
    if (first > last) continue;
    const prev = found[found.length - 1];
    // Two stretches with no character between them are one.
    if (prev && first <= prev[1] + 1) prev[1] = last;
    else found.push([first, last]);
  }
  return found;
};
