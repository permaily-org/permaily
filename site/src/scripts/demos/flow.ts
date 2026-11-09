import { $, $$, Timeline, fmt, out3, seg, setVar } from '../timeline';

/** Home: how it works. The inbox connects, the person approves, and only matching mail goes through. */
export function initFlow(root: HTMLElement) {
  const inRows = $$('.fl-in', root);
  const outRows = $$('.fl-out', root);
  const wiresIn = $$('.wire.in', root);
  const wiresOut = $$('.wire.out', root);
  const steps = $$('.step', root);
  const states = $$('.gs', root);
  const allow = $('.fake-btn', root);
  const outNum = $('[data-out]', root);
  const would = $('[data-would]', root);
  const shared = $('[data-shared]', root);
  const held = $('[data-held]', root);
  const receipts = $('[data-receipts]', root);
  const T = { rowEach: 70, wire: 900, s2: 2500, ask: 2500, prev: 3050, prevEach: 230, press: 5900, s3: 6200, lane: 380, end: 9800 };
  const kept = outRows.filter((r) => r.dataset.keep === '1').length;

  const render = (t: number) => {
    inRows.forEach((row, i) => setVar(row, '--in', seg(t, i * T.rowEach, i * T.rowEach + 260)));

    wiresIn.forEach((wire, i) => {
      setVar(wire, '--w', out3(seg(t, T.wire + i * 45, T.wire + i * 45 + 420)));
      const start = T.s3 + i * T.lane;
      const p = seg(t, start, start + 300);
      setVar(wire, '--po', p > 0 && p < 1 ? 1 : 0);
      setVar(wire, '--px', `${p * Math.max(0, wire.clientWidth - 12)}px`);
    });

    let matched = 0;
    let arrived = 0;
    outRows.forEach((row, i) => {
      const s = T.s3 + i * T.lane + 260;
      const subject = $('.subj .t', row);
      if (row.dataset.keep === '1') {
        const k = matched++;
        setVar($('.subj .t', inRows[i]!), '--hl', out3(seg(t, T.prev + k * T.prevEach, T.prev + k * T.prevEach + 360)));
        setVar(wiresOut[i]!, '--w', out3(seg(t, s, s + 200)));
        setVar(subject, '--hl', out3(seg(t, s + 120, s + 460)));
        $('.tx', subject).style.opacity = String(seg(t, s + 120, s + 200));
        $('.from .tx', row).style.opacity = String(seg(t, s + 60, s + 180));
        arrived += seg(t, s, s + 300);
      } else {
        setVar(subject, '--bk', out3(seg(t, s, s + 340)));
        setVar($('.from .t', row), '--bk', out3(seg(t, s - 60, s + 160)));
        const tag = $('.never-tag', row) as HTMLElement | null;
        if (tag) setVar(tag, '--tag', seg(t, s + 300, s + 460));
      }
    });

    const released = fmt(47 * (kept ? arrived / kept : 0));
    outNum.textContent = released;
    shared.textContent = released;
    receipts.textContent = released;
    would.textContent = fmt(47 * out3(seg(t, T.prev, T.prev + (kept - 1) * T.prevEach + 360)));
    held.textContent = fmt(12257 * out3(seg(t, T.s3, T.end - 400)));

    setVar(states[0]!, '--o', t < T.ask - 150 ? 1 : Math.max(0, 1 - (t - (T.ask - 150)) / 150));
    setVar(states[1]!, '--o', t < T.ask ? 0 : t < T.s3 - 120 ? seg(t, T.ask, T.ask + 220) : Math.max(0, 1 - (t - (T.s3 - 120)) / 120));
    setVar(states[2]!, '--o', seg(t, T.s3, T.s3 + 220));
    allow.dataset.pressed = String(t >= T.press);

    const progress = [seg(t, 0, T.s2), seg(t, T.s2, T.s3), seg(t, T.s3, T.end)];
    steps.forEach((step, i) => {
      setVar(step, '--sp', progress[i]!);
      step.dataset.on = String(progress[i]! > 0);
    });
  };

  new Timeline({ name: 'flow', root, duration: T.end, render, threshold: 0.35, control: $('.demo-ctl', root) });
}
