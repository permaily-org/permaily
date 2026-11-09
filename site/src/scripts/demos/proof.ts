import { $, $$, clamp, paintHash, seg, setVar, Timeline } from '../timeline';

/** Home: one command, and two hashes that resolve into the same value. */
export function initProof(root: HTMLElement) {
  const typed = $('.cmd .vis', root);
  const untyped = $('.cmd .inv', root);
  const command = root.dataset.cmd!;
  const lines = $$('.ln[data-at]', root);
  const hashes = $$('.hash', root);
  const T = { type: 400, cps: 34, hash: 2500, hashDur: 2400, end: 5800 };

  const render = (t: number) => {
    const n = Math.floor(clamp(((t - T.type) * T.cps) / 1000, 0, command.length));
    typed.textContent = command.slice(0, n);
    untyped.textContent = command.slice(n);
    lines.forEach((line) =>
      setVar(line, '--o', seg(t, Number(line.dataset.at), Number(line.dataset.at) + 120)),
    );
    const frame = Math.floor(t / 70);
    const progress = seg(t, T.hash, T.hash + T.hashDur);
    hashes.forEach((hash, i) => paintHash(hash, progress, frame, i));
  };

  new Timeline({
    name: 'proof',
    root,
    duration: T.end,
    render,
    threshold: 0.4,
    control: $('.demo-ctl', root.parentElement!),
  });
}
