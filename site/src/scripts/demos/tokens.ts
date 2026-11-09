import { $, $$, bindToggle, out3, seg, setPressed, setVar, Timeline } from '../timeline';

/** Developers: ask for more fields, and each email costs more. */
export function initTokens(root: HTMLElement) {
  const headers = $$('[data-hd] .t', root);
  const fields = $$('[data-field] .t', root);
  const rows = $$('[data-new]', root);
  const blocks = $$('[data-b]', root);
  const count = $('[data-n]', root);
  const word = $('[data-tk]', root);
  const price = $('[data-p]', root);
  const buttons = $$('.seg button', root);
  const T = { on: 150, flip: 1700, go: 1850, each: 380, meter: 3100, end: 3700 };

  const render = (t: number) => {
    headers.forEach((h, i) =>
      setVar(h, '--hl', out3(seg(t, T.on + i * 110, T.on + i * 110 + 320))),
    );
    fields.forEach((f, i) => {
      const s = T.go + i * T.each;
      setVar(f, '--bk', 1 - out3(seg(t, s, s + 340)));
    });
    rows.forEach((r, i) => {
      const s = T.go + i * T.each + 100;
      setVar(r, '--w', out3(seg(t, s, s + 320)));
    });
    blocks.forEach((b, i) =>
      setVar(b, '--f', out3(seg(t, T.meter + i * 90, T.meter + i * 90 + 260))),
    );
    const full = t >= T.meter + 90;
    count.textContent = full ? '3' : '1';
    word.textContent = full ? 'tokens' : 'token';
    price.textContent = full ? '$0.03' : '$0.01';
    setPressed(buttons, t >= T.flip);
  };

  const timeline = new Timeline({
    name: 'tokens',
    root,
    duration: T.end,
    render,
    threshold: 0.35,
    control: $('.demo-ctl', root),
  });
  bindToggle(timeline, buttons, T.flip, T.end);
}
