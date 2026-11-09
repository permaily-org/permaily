import {
  $,
  $$,
  bindToggle,
  fmt,
  lerp,
  out3,
  out4,
  seg,
  setPressed,
  setVar,
  Timeline,
} from '../timeline';

const ALL_MAIL = 1200000;

/** Companies: every vendor's reach shrinks to its job. */
export function initVendors(root: HTMLElement) {
  const rows = $$('.vrow', root);
  const num = $('[data-count]', root);
  const buttons = $$('.seg button', root);
  const total = rows.reduce((sum, row) => sum + Number(row.dataset.n), 0);
  const T = { flip: 1500, go: 1650, each: 230 };
  const duration = T.go + (rows.length - 1) * T.each + 900;

  const render = (t: number) => {
    rows.forEach((row, i) => {
      const s = T.go + i * T.each;
      setVar($('.vbar', row), '--p', out3(seg(t, s, s + 700)));
      $('.n', row).textContent = fmt(
        lerp(ALL_MAIL, Number(row.dataset.n), out4(seg(t, s, s + 760))),
      );
    });
    num.textContent = fmt(lerp(ALL_MAIL, total, out4(seg(t, T.go, duration - 200))));
    setPressed(buttons, t >= T.flip);
  };

  const timeline = new Timeline({ name: 'vendors', root, duration, render, threshold: 0.2 });
  bindToggle(timeline, buttons, T.flip, duration);
}
