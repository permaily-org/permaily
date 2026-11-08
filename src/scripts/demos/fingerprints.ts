import { $, $$, Timeline, out3, paintHash, seg, setVar } from '../timeline';

/** Security: two fingerprints, computed apart, that turn out to be the same. */
export function initFingerprints(root: HTMLElement) {
  const hashes = $$('.hash', root);
  const equals = $('.eq span', root);
  const status = $('[data-status]', root);
  const T = { a: 300, b: 2000, len: 1500, eq: 3600, end: 4400 };

  const render = (t: number) => {
    const frame = Math.floor(t / 70);
    hashes.forEach((hash, i) => {
      const start = i ? T.b : T.a;
      paintHash(hash, seg(t, start, start + T.len), frame, i);
    });
    setVar(equals, '--o', out3(seg(t, T.eq, T.eq + 360)));
    setVar(status, '--o', seg(t, T.eq + 100, T.eq + 460));
  };

  new Timeline({ name: 'chain', root, duration: T.end, render, threshold: 0.35, control: $('.demo-ctl', root) });
}
