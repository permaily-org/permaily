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

/** Developers: the same feature, without paying an LLM to read the whole inbox. */
export function initLlmCost(root: HTMLElement) {
  const num = $('[data-count]', root);
  const oldLines = $$('.old .t', root);
  const oldCode = $('.old', root);
  const newLines = $$('.new', root);
  const captionA = $('[data-q=a]', root);
  const captionB = $('[data-q=b]', root);
  const money = $$('[data-money]', root);
  const swaps = $$('[data-a]', root);
  const buttons = $$('.seg button', root);
  const T = { flip: 2200, go: 2350 };
  const duration = T.go + 2300;

  const render = (t: number) => {
    oldLines.forEach((line, i) =>
      setVar(line, '--bk', out3(seg(t, T.go + i * 70, T.go + i * 70 + 320))),
    );
    oldCode.style.opacity = String(1 - seg(t, T.go + 760, T.go + 900));
    newLines.forEach((line, i) =>
      setVar(line, '--w', seg(t, T.go + 900 + i * 110, T.go + 900 + i * 110 + 280)),
    );
    const down = out4(seg(t, T.go, duration - 260));
    num.textContent = fmt(lerp(12304, 47, down));
    money.forEach((el) => {
      const value = lerp(Number(el.dataset.money), Number(el.dataset.to), down);
      const decimals = Number(el.dataset.dec);
      // A total can land on a value that isn't a round amount, such as "under 1¢".
      el.textContent =
        el.dataset.end && down >= 1
          ? el.dataset.end
          : `$${decimals ? value.toFixed(decimals) : fmt(value)}`;
    });
    swaps.forEach((el) => {
      el.textContent = (t >= T.go + 900 ? el.dataset.b : el.dataset.a) ?? '';
    });
    const swap = seg(t, T.go, T.go + 240);
    setVar(captionA, '--o', 1 - swap);
    setVar(captionB, '--o', swap);
    setPressed(buttons, t >= T.flip);
  };

  const timeline = new Timeline({ name: 'dev', root, duration, render, threshold: 0.25 });
  bindToggle(timeline, buttons, T.flip, duration);
}
