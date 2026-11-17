// The emblems of Home's security section (src/scripts/demos/emblems.ts): the four shapes, how one melts
// into the next, and the windows a row of mail is shown through. Nothing here needs a browser.

import { describe, expect, it } from 'vitest';
import {
  between,
  CELL,
  COLS,
  crossings,
  EMBLEMS,
  ease,
  MID,
  ROWS,
  type Shape,
  stretches,
  stretchesOf,
} from '../src/scripts/demos/emblems';
import { copies, sameStretches, windowOf } from '../src/scripts/demos/rows';

type Inside = (x: number, y: number) => boolean;

/** The characters of the drawing that are inside, row by row. */
const cells = (inside: Inside) =>
  Array.from({ length: ROWS }, (_, r) => {
    const row = Array.from({ length: COLS }, () => false);
    for (const [first, last] of stretches(inside, r)) row.fill(true, first, last + 1);
    return row;
  });
const count = (grid: boolean[][]) => grid.flat().filter(Boolean).length;
const whole = (shape: Shape) => cells(between(null, shape, 1));
const keys = Object.keys(EMBLEMS) as (keyof typeof EMBLEMS)[];

describe('the four emblems', () => {
  it('has one for each feature', () => {
    expect(keys).toEqual(['source', 'hosted', 'builds', 'trust']);
  });

  for (const key of keys) {
    it(`${key} fills a fair part of the drawing`, () => {
      const share = count(whole(EMBLEMS[key])) / (ROWS * ROWS) / (1 / CELL);
      expect(share).toBeGreaterThan(0.2);
      expect(share).toBeLessThan(0.75);
    });

    it(`${key} keeps within 8.75 rows of the middle, so no character reaches past the side`, () => {
      whole(EMBLEMS[key]).forEach((row) =>
        row.forEach((on, c) => {
          if (on) expect(Math.abs((c + 0.5) * CELL - MID)).toBeLessThanOrEqual(8.75);
        }),
      );
    });
  }

  it('draws the brackets the same when turned upside down', () => {
    for (const [x, y] of [
      [-7.5, 9],
      [-3.3, 3.5],
      [0, 9],
      [1.75, 2.3],
      [5, 12],
    ] as const) {
      expect(EMBLEMS.source(-x, ROWS - y)).toBeCloseTo(EMBLEMS.source(x, y), 9);
    }
  });

  it('leaves the lights out of each server, and a row between two servers', () => {
    expect(EMBLEMS.hosted(3, 4)).toBeLessThan(0);
    expect(EMBLEMS.hosted(-5.15, 4)).toBeGreaterThan(0);
    expect(EMBLEMS.hosted(-2.15, 9)).toBeGreaterThan(0);
    expect(EMBLEMS.hosted(0, 6.5)).toBeGreaterThan(0);
  });

  it('leaves the check mark out of the seal', () => {
    expect(EMBLEMS.builds(0, 3)).toBeLessThan(0);
    expect(EMBLEMS.builds(-1.5, 12.2)).toBeGreaterThan(0);
    expect(EMBLEMS.builds(0, 0.2)).toBeGreaterThan(0);
  });

  it('draws the padlock the same on both sides, with its keyhole left out', () => {
    for (const [x, y] of [
      [3.7, 3],
      [6, 12],
      [2, 16.5],
      [0.4, 13],
    ] as const) {
      expect(EMBLEMS.trust(-x, y)).toBeCloseTo(EMBLEMS.trust(x, y), 9);
    }
    expect(EMBLEMS.trust(4, 12)).toBeLessThan(0);
    expect(EMBLEMS.trust(0, 11.4)).toBeGreaterThan(0);
    // Under the arch of the shackle there is nothing.
    expect(EMBLEMS.trust(0, 6)).toBeGreaterThan(0);
  });
});

describe('a change from one emblem to the next', () => {
  it('is slow at both ends and stays between nothing and all', () => {
    expect(ease(-1)).toBe(0);
    expect(ease(0)).toBe(0);
    expect(ease(0.5)).toBeCloseTo(0.5, 9);
    expect(ease(1)).toBe(1);
    expect(ease(2)).toBe(1);
    expect(ease(0.1)).toBeLessThan(0.1);
    expect(ease(0.9)).toBeGreaterThan(0.9);
  });

  it('starts on the one and ends on the other', () => {
    expect(cells(between(EMBLEMS.source, EMBLEMS.hosted, 0))).toEqual(whole(EMBLEMS.source));
    expect(cells(between(EMBLEMS.source, EMBLEMS.hosted, 1))).toEqual(whole(EMBLEMS.hosted));
  });

  it('is neither of them half-way, and not empty', () => {
    const half = cells(between(EMBLEMS.source, EMBLEMS.hosted, 0.5));
    expect(half).not.toEqual(whole(EMBLEMS.source));
    expect(half).not.toEqual(whole(EMBLEMS.hosted));
    expect(count(half)).toBeGreaterThan(count(whole(EMBLEMS.source)));
    expect(count(half)).toBeLessThan(count(whole(EMBLEMS.hosted)));
  });

  it('grows the first emblem out of the middle, from nothing', () => {
    expect(count(cells(between(null, EMBLEMS.builds, 0)))).toBe(0);
    const part = cells(between(null, EMBLEMS.builds, 0.5));
    expect(count(part)).toBeGreaterThan(0);
    expect(count(part)).toBeLessThan(count(whole(EMBLEMS.builds)));
    // What is there half-way is part of the emblem, and nothing else.
    const all = whole(EMBLEMS.builds);
    part.forEach((row, r) => row.forEach((on, c) => on && expect(all[r]?.[c]).toBe(true)));
  });
});

describe('the stretches of a row that are inside', () => {
  const band: Inside = (x) => Math.abs(x) <= 1;

  it('counts a character by its middle', () => {
    // A character is 0.42 rows wide. The middles of characters 19 to 23 lie within a row of the middle.
    expect(stretches(band, 0)).toEqual([[19, 23]]);
  });

  it('follows the mail as it drifts, a whole character at a time', () => {
    expect(stretches(band, 0, CELL, 0.5)).toEqual([[20, 23]]);
    expect(stretches(band, 0, CELL, 0.99)).toEqual([[20, 24]]);
  });

  it('finds every stretch, up to the last character', () => {
    const sides: Inside = (x) => Math.abs(x) >= 5;
    expect(stretches(sides, 0)).toEqual([
      [0, 9],
      [33, COLS - 1],
    ]);
  });

  it('finds none where the row is outside', () => {
    expect(stretches(() => false, 0)).toEqual([]);
  });
});

describe('where a settled emblem crosses a row', () => {
  it('is found to the edge, for a shape whose edges are known', () => {
    // A seal of radius 8.3 around the middle of the drawing: its row through the middle is as wide as it is.
    const round: Shape = (x, y) => Math.hypot(x, y - 8.5) - 8.3;
    const [only, ...rest] = crossings(round, 8);
    expect(rest).toEqual([]);
    expect(only?.[0]).toBeCloseTo(-8.3, 6);
    expect(only?.[1]).toBeCloseTo(8.3, 6);
  });

  it('gives a stretch for each part of an emblem that the row passes through', () => {
    // A row of a server with its two lights left out: three stretches. A row between two servers: none.
    expect(crossings(EMBLEMS.hosted, 3)).toHaveLength(3);
    expect(crossings(EMBLEMS.hosted, 6)).toEqual([]);
  });

  it('answers for every character as the shape itself would, wherever the mail has drifted to', () => {
    for (const key of keys) {
      const shape = EMBLEMS[key];
      const inside = between(null, shape, 1);
      for (let row = 0; row < ROWS; row++) {
        const cross = crossings(shape, row);
        for (const shift of [0, 0.13, 0.5, 0.77, 0.999]) {
          expect(stretchesOf(cross, CELL, shift), `${key}, row ${row}, drifted ${shift}`).toEqual(
            stretches(inside, row, CELL, shift),
          );
        }
      }
    }
  });
});

describe('the window of a run that the browser moves', () => {
  const list: [number, number][] = [
    [310, 314],
    [320, 330],
  ];

  it('is the stretches of the row itself while the run is far from starting over', () => {
    expect(copies(list, 300, 380)).toBe(list);
  });

  it('is cut in the copy before as well when the start-over is near', () => {
    // The run starts over after 380 characters, and character 350 stands at the row's left edge.
    expect(
      copies(
        [
          [360, 364],
          [370, 381],
        ],
        350,
        380,
      ),
    ).toEqual([
      [0, 1],
      [360, 364],
      [370, 381],
    ]);
  });

  it('is cut in the copy after as well just after the start-over', () => {
    expect(copies([[10, 14]], 1, 380)).toEqual([
      [10, 14],
      [390, 394],
    ]);
  });

  it('is written only when it changes', () => {
    expect(
      sameStretches(list, [
        [310, 314],
        [320, 330],
      ]),
    ).toBe(true);
    expect(
      sameStretches(list, [
        [310, 314],
        [320, 331],
      ]),
    ).toBe(false);
    expect(sameStretches(list, [[310, 314]])).toBe(false);
    expect(sameStretches([], [])).toBe(true);
  });
});

describe('the window a row is shown through', () => {
  it('is empty when nothing shows', () => {
    expect(windowOf([], 10)).toBe('');
  });

  it('is cut from both sides for one stretch', () => {
    expect(windowOf([[2, 4]], 10)).toBe('inset(0 calc(100% - 50.00px) 0 20.00px)');
  });

  it('is a box for each stretch, joined along the foot of the row', () => {
    expect(
      windowOf(
        [
          [2, 4],
          [7, 8],
        ],
        10,
      ),
    ).toBe(
      'polygon(20.00px 100%, 20.00px 0, 50.00px 0, 50.00px 100%, 70.00px 100%, 70.00px 0, 90.00px 0, 90.00px 100%)',
    );
  });
});
