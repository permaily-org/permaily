import { $, $$, lerp, out3, seg, setVar, Timeline } from '../timeline';

/** How a row looks: covered by a bar, and whether its cut is covered on its own. */
interface Look {
  bk: number;
  cut: number;
}

/**
 * Companies: the same company inbox, as each vendor sees it. With Permaily off, nothing is covered and every
 * vendor can read all of it. With it on, each row moves to the chosen vendor's view.
 */
export function initVendors(root: HTMLElement) {
  const power = $('[data-switch]', root);
  const vendors = $$('button[data-vendor]', root);
  const T = { on: 150, onEach: 45, onDur: 380, flip: 2300, go: 2450, span: 500 };
  const duration = T.go + T.span + 640;

  const lines = $$('.vi-row', root).map((row) => {
    const [date, sender, subject] = $$('.t', row);
    return {
      date: date!,
      sender: sender!,
      subject: subject!,
      cut: row.querySelector<HTMLElement>('[data-cut]'),
      apps: row.dataset.apps!.split(' '),
    };
  });

  let vendor = vendors.find((b) => b.getAttribute('aria-pressed') === 'true')!.dataset.vendor!;
  // Where the rows start from. Null is Permaily off, where nothing is covered.
  let from: string | null = null;
  // What the visitor set the switch to. Until they touch it, it follows the animation.
  let chosen: boolean | null = null;
  let plan: { a: Look; b: Look; s: number }[] = [];

  const look = (line: (typeof lines)[number], id: string | null): Look => {
    const shared = id === null || line.apps.includes(id);
    return { bk: shared ? 0 : 1, cut: shared && line.cut?.dataset.cut === id ? 1 : 0 };
  };

  // The rows that change move one after another, over the same span however many there are.
  const replan = () => {
    const moves = lines.map((line) => {
      const a = look(line, from);
      const b = look(line, vendor);
      // A bar over the whole row hides its cut, so the cut holds still while that bar moves.
      if (a.bk) a.cut = b.cut;
      if (b.bk) b.cut = a.cut;
      return { a, b, moving: a.bk !== b.bk || a.cut !== b.cut };
    });
    const moving = moves.filter((m) => m.moving).length;
    const each = moving > 1 ? Math.min(100, T.span / (moving - 1)) : 0;
    let k = 0;
    plan = moves.map(({ a, b, moving }) => ({ a, b, s: T.go + (moving ? k++ : 0) * each }));
  };

  const render = (t: number) => {
    lines.forEach((line, i) => {
      const start = T.on + i * T.onEach;
      setVar(line.subject, '--hl', out3(seg(t, start, start + T.onDur)));
      const { a, b, s } = plan[i]!;
      setVar(line.date, '--bk', lerp(a.bk, b.bk, out3(seg(t, s, s + 160))));
      setVar(line.sender, '--bk', lerp(a.bk, b.bk, out3(seg(t, s + 60, s + 280))));
      setVar(line.subject, '--bk', lerp(a.bk, b.bk, out3(seg(t, s + 130, s + 550))));
      if (line.cut) setVar(line.cut, '--cut', lerp(a.cut, b.cut, out3(seg(t, s + 130, s + 450))));
    });
    power.setAttribute('aria-checked', String(chosen ?? t >= T.flip));
  };

  replan();
  const timeline = new Timeline({ name: 'vendors', root, duration, render, threshold: 0.15 });

  // Off runs back to just before the bars. On runs forward to the end.
  power.addEventListener('click', () => {
    chosen = power.getAttribute('aria-checked') !== 'true';
    from = null;
    replan();
    if (chosen) {
      if (timeline.t < T.flip) timeline.seek(T.flip);
      timeline.to(duration, 1);
    } else {
      timeline.to(T.flip - 40, 1.8);
    }
  });

  // With Permaily on, a new vendor moves each row from the old vendor's view. With it off, nothing visible changes.
  vendors.forEach((button) =>
    button.addEventListener('click', () => {
      const next = button.dataset.vendor!;
      if (next === vendor) return;
      const on = timeline.t >= T.flip;
      from = on ? vendor : null;
      vendor = next;
      vendors.forEach((b) => b.setAttribute('aria-pressed', String(b === button)));
      replan();
      if (on) {
        timeline.seek(T.go);
        timeline.to(duration, 1);
      } else {
        timeline.seek(timeline.t);
      }
    }),
  );
}
