import { $, $$, fmt, lerp, out3, out4, seg, setPressed, setVar, Timeline } from '../timeline';

const ALL = 12304;

/** How a row looks: covered by a bar, and whether its cut is covered on its own. */
interface Look {
  bk: number;
  cut: number;
}

/**
 * Home: the same inbox, with Gmail access and then with Permaily, as Trip App or Ledgerly sees it.
 * Each row moves from one look to another: from nothing covered to an app's view, or from one app's view to the other's.
 */
export function initHero(root: HTMLElement) {
  const num = $('[data-count]', root);
  const access = $$('button[data-mode]', root);
  const apps = $$('button[data-app]', root);
  const caps = $$('[data-cap]', root);
  const T = { on: 150, onEach: 45, onDur: 380, flip: 2500, go: 2650, span: 500 };
  const duration = T.go + T.span + 640;

  const lines = $$('.row', root).map((row) => {
    const [date, sender, subject] = $$('.t', row);
    return {
      date: date!,
      sender: sender!,
      subject: subject!,
      cut: row.querySelector<HTMLElement>('[data-cut]'),
      apps: row.dataset.apps!.split(' '),
    };
  });

  let app = apps.find((b) => b.getAttribute('aria-pressed') === 'true')!.dataset.app!;
  // Where the rows start from. Null is Gmail access, where nothing is covered.
  let from: string | null = null;
  let plan: { a: Look; b: Look; s: number }[] = [];

  const total = (id: string | null) =>
    id === null ? ALL : Number(apps.find((b) => b.dataset.app === id)!.dataset.total);

  const look = (line: (typeof lines)[number], id: string | null): Look => {
    const shared = id === null || line.apps.includes(id);
    return { bk: shared ? 0 : 1, cut: shared && line.cut?.dataset.cut === id ? 1 : 0 };
  };

  // The rows that change move one after another, over the same span however many there are.
  const replan = () => {
    const moves = lines.map((line) => {
      const a = look(line, from);
      const b = look(line, app);
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
    num.textContent = fmt(lerp(total(from), total(app), out4(seg(t, T.go, duration - 120))));
    const after = t >= T.flip;
    setPressed(access, after);
    caps.forEach((cap) => (cap.hidden = cap.dataset.cap !== (after ? app : 'before')));
  };

  replan();
  const timeline = new Timeline({ name: 'hero', root, duration, render, threshold: 0.15 });

  // Gmail access runs back to just before the flip. Permaily runs forward to the end.
  access.forEach((button) =>
    button.addEventListener('click', () => {
      from = null;
      replan();
      if (button.dataset.mode === 'before') {
        timeline.to(T.flip - 40, 1.8);
      } else {
        if (timeline.t < T.flip) timeline.seek(T.flip);
        timeline.to(duration, 1);
      }
    }),
  );

  // With Permaily, a new app moves each row from the old app's view. With Gmail access, nothing visible changes.
  apps.forEach((button) =>
    button.addEventListener('click', () => {
      const next = button.dataset.app!;
      if (next === app) return;
      const after = timeline.t >= T.flip;
      from = after ? app : null;
      app = next;
      apps.forEach((b) => b.setAttribute('aria-pressed', String(b === button)));
      replan();
      if (after) {
        timeline.seek(T.go);
        timeline.to(duration, 1);
      } else {
        timeline.seek(timeline.t);
      }
    }),
  );
}
