import { $, $$, clamp, out3, seg, setVar, Timeline } from '../timeline';

/** Home: an agent asks, and Permaily answers with only what was shared. */
export function initChat(root: HTMLElement) {
  const items = $$('[data-at]', root);
  const duration = Number(root.dataset.dur);

  const render = (t: number) => {
    for (const el of items) {
      const at = Number(el.dataset.at);
      if (el.classList.contains('res')) {
        setVar(el, '--w', out3(seg(t, at, at + 340)));
      } else if (el.dataset.stream) {
        const words = el.dataset.stream.split(' ');
        const shown = Math.floor(clamp((t - at) / 60, 0, words.length));
        $('.vis', el).textContent = words.slice(0, shown).join(' ');
        $('.inv', el).textContent = (shown ? ' ' : '') + words.slice(shown).join(' ');
        setVar(el, '--o', t >= at ? 1 : 0);
      } else {
        setVar(el, '--o', seg(t, at, at + 180));
      }
    }
  };

  new Timeline({
    name: 'chat',
    root,
    duration,
    render,
    threshold: 0.45,
    control: $('.demo-ctl', root),
  });
}
