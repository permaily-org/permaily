import { $, $$, Timeline } from '../timeline';
import { drifting, measure } from './rows';

/**
 * Home, how it works: mail drifts from the inbox to one line. The styles decide what a row looks like on each
 * side of it, so all that moves here is the mail, and the browser moves it by itself: the script hands the
 * rows over once, and then has nothing to draw.
 */
export function initLine(root: HTMLElement) {
  const runs = drifting($$('.lx', root), 1, { root, field: $('.ln-field', root) });
  // The width of one character: two copies of a row's text, over the characters in them.
  const ch = measure(root, $('.ln-in .lx', root), 2, () => runs.size(ch()));
  runs.size(ch());

  let timeline: Timeline | undefined;
  timeline = new Timeline({
    name: 'line',
    root,
    // There is no opening to play. Without motion, the rows rest where they start.
    duration: 0,
    render: (t) => {
      // The clock places the rows once, before they first move: at their start, or at a moment asked for
      // in the address. After that they are never put back, and there is nothing to draw.
      if (!timeline) runs.time = t;
      else timeline.rest(1000);
    },
    threshold: 0.2,
    ambient: true,
    control: $('.demo-ctl', root.parentElement!),
    watch: (moving) => (moving ? runs.play() : runs.pause()),
  });
}
