import { $, $$, fmt, lerp, out3, reducedMotion, seekParam, seg } from '../timeline';

const COUNTS: Record<string, number> = {
  flights: 47,
  invoices: 212,
  receipts: 388,
  news: 1204,
  rest: 10129,
};

/** Home: the access grid. One scripted turn-off on first view, then every square and switch works. */
export function initGrid(root: HTMLElement) {
  const connections = ['trip', 'claude', 'ledgerly'];
  const num = $('[data-count]', root);
  const connCount = $('[data-conns]', root);
  const connWord = $('[data-pl]', root);
  const reset = $('[data-reset]', root);
  const hint = $('[data-hint]', root);
  const cells = $$('.cell', root);
  const switches = $$('.sw', root);
  const initial = cells.map((c) => c.dataset.on!);
  const remembered: Record<string, string[]> = {};
  let shown = 647;
  let frame = 0;

  const switchOf = (c: string) => switches.find((s) => s.dataset.c === c)!;
  const rowOf = (c: string) => cells.filter((x) => x.dataset.c === c);
  const setCell = (cell: HTMLElement, on: string) => {
    cell.dataset.on = on;
    cell.setAttribute('aria-pressed', on);
  };

  const update = (instant = false) => {
    const shared = new Set<string>();
    let connected = 0;
    for (const c of connections) {
      const mine = rowOf(c).filter((x) => x.dataset.on === 'true');
      if (mine.length && switchOf(c).getAttribute('aria-checked') === 'true') connected++;
      mine.forEach((x) => shared.add(x.dataset.k!));
    }
    let total = 0;
    shared.forEach((k) => (total += COUNTS[k]!));

    connCount.textContent = String(connected);
    connWord.textContent = connected === 1 ? 'connection' : 'connections';
    const from = shown;
    shown = total;
    cancelAnimationFrame(frame);
    if (instant || reducedMotion) {
      num.textContent = fmt(total);
      return;
    }
    const start = performance.now();
    const step = (now: number) => {
      const p = out3(seg(now, start, start + 500));
      num.textContent = fmt(lerp(from, total, p));
      if (p < 1) frame = requestAnimationFrame(step);
    };
    frame = requestAnimationFrame(step);
  };

  const touched = () => {
    reset.hidden = false;
    hint.hidden = true;
  };

  const setSwitch = (c: string, on: boolean, quiet = false) => {
    switchOf(c).setAttribute('aria-checked', String(on));
    if (quiet) return;
    const row = rowOf(c);
    if (!on) remembered[c] = row.map((x) => x.dataset.on!);
    const next = on ? (remembered[c] ?? row.map(() => 'true')) : row.map(() => 'false');
    row.forEach((cell, i) => setTimeout(() => setCell(cell, next[i]!), reducedMotion ? 0 : i * 40));
  };

  cells.forEach((cell) =>
    cell.addEventListener('click', () => {
      setCell(cell, String(cell.dataset.on !== 'true'));
      const c = cell.dataset.c!;
      setSwitch(
        c,
        rowOf(c).some((x) => x.dataset.on === 'true'),
        true,
      );
      touched();
      update();
    }),
  );

  switches.forEach((s) =>
    s.addEventListener('click', () => {
      setSwitch(s.dataset.c!, s.getAttribute('aria-checked') !== 'true');
      touched();
      setTimeout(() => update(), reducedMotion ? 0 : 180);
    }),
  );

  reset.addEventListener('click', () => {
    cells.forEach((cell, i) => setCell(cell, initial[i]!));
    switches.forEach((s) => s.setAttribute('aria-checked', 'true'));
    reset.hidden = true;
    hint.hidden = false;
    update();
  });

  const turnOffLedgerly = () => {
    const ledgerly = switchOf('ledgerly');
    setTimeout(() => ledgerly.classList.add('ring'), 700);
    setTimeout(() => {
      ledgerly.classList.remove('ring');
      if (ledgerly.getAttribute('aria-checked') !== 'true') return;
      setSwitch('ledgerly', false);
      setTimeout(() => {
        update();
        reset.hidden = false;
      }, 180);
    }, 1500);
  };

  window.PermailyDemos.grid = { set: (c: string, on: boolean) => (setSwitch(c, on), update(true)) };

  if (seekParam[0] === 'grid' && seekParam[1] === 'off') {
    switchOf('ledgerly').setAttribute('aria-checked', 'false');
    rowOf('ledgerly').forEach((cell) => setCell(cell, 'false'));
    update(true);
    reset.hidden = false;
  } else if (!reducedMotion && !seekParam[0]) {
    new IntersectionObserver(
      (entries, observer) => {
        if (entries.some((e) => e.isIntersecting)) {
          observer.disconnect();
          turnOffLedgerly();
        }
      },
      { threshold: 0.5 },
    ).observe(root);
  }
  root.dataset.ready = 'true';
}
