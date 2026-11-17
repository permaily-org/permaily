// Each demo is a pure function of time, so it can be played, paused, scrubbed and captured.
// The HTML is written in its final state: without JavaScript, or with reduced motion, a demo shows its result.
//
// Two URL parameters help with review and screenshots:
//   ?still            shows every demo's final state
//   ?seek=sea:2700    freezes one demo at a moment

export const clamp = (x: number, a = 0, b = 1) => Math.min(b, Math.max(a, x));
export const seg = (t: number, a: number, b: number) => clamp((t - a) / (b - a));
export const out3 = (x: number) => 1 - (1 - x) ** 3;
export const out4 = (x: number) => 1 - (1 - x) ** 4;
export const lerp = (a: number, b: number, p: number) => a + (b - a) * p;
export const fmt = (n: number) => Math.round(n).toLocaleString('en-US');

// NoInfer keeps the default of HTMLElement when the result is passed straight to something that accepts null.
export const $ = <T extends Element = HTMLElement>(
  selector: string,
  root: ParentNode = document,
): NoInfer<T> => root.querySelector<T>(selector)!;
export const $$ = <T extends Element = HTMLElement>(
  selector: string,
  root: ParentNode = document,
) => [...root.querySelectorAll<T>(selector)];

export const setVar = (el: HTMLElement, name: string, value: number | string) =>
  el.style.setProperty(name, String(value));

const params = new URLSearchParams(location.search);
export const reducedMotion =
  matchMedia('(prefers-reduced-motion: reduce)').matches || params.has('still');
export const seekParam = (params.get('seek') ?? '').split(':');

declare global {
  interface Window {
    PermailyDemos: Record<string, unknown>;
  }
}
window.PermailyDemos ??= {};

interface TimelineOptions {
  name: string;
  root: HTMLElement;
  duration: number;
  render: (t: number) => void;
  threshold?: number;
  control?: HTMLElement | null;
  /** Keeps running after its opening has played, for motion with no end, such as drift. */
  ambient?: boolean;
  /** Told when the animation starts and stops moving, for motion that the browser runs by itself. */
  watch?: (moving: boolean) => void;
}

export class Timeline {
  name: string;
  root: HTMLElement;
  duration: number;
  render: (t: number) => void;
  control: HTMLElement | null;
  ambient: boolean;
  watch: ((moving: boolean) => void) | undefined;
  t: number;
  state: 'end' | 'idle' | 'playing' | 'paused' = 'end';
  started = false;
  private raf = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  /** How long the drawing asked to rest, and how long the frame that comes next was put off for it. */
  private wish = 0;
  private rested = 0;
  private moving = false;
  private last = 0;
  private target: number;
  private speed = 1;
  private resumeOnReturn = false;

  constructor({
    name,
    root,
    duration,
    render,
    threshold = 0.4,
    control = null,
    ambient = false,
    watch,
  }: TimelineOptions) {
    this.name = name;
    this.root = root;
    this.duration = duration;
    this.render = render;
    this.control = control;
    this.ambient = ambient;
    this.watch = watch;
    this.t = duration;
    this.target = duration;
    window.PermailyDemos[name] = this;
    control?.addEventListener('click', () => this.toggle());

    if (seekParam[0] === name) {
      this.seek(Number(seekParam[1]) || 0);
      this.started = true;
    } else if (reducedMotion) {
      this.seek(duration);
      this.started = true;
    } else {
      this.seek(0);
      const observer = new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            if (entry.isIntersecting && !this.started) {
              this.started = true;
              this.play();
            } else if (!entry.isIntersecting && this.state === 'playing') {
              this.pause();
              this.resumeOnReturn = true;
            } else if (entry.isIntersecting && this.resumeOnReturn) {
              this.resumeOnReturn = false;
              this.play();
            }
          }
        },
        { threshold },
      );
      observer.observe(root);
    }
    root.dataset.ready = 'true';
    this.updateControl();
  }

  seek(t: number) {
    this.t = this.ambient ? Math.max(0, t) : clamp(t, 0, this.duration);
    this.render(this.t);
  }

  play() {
    // Ambient motion has no end to reach, so it runs until it is paused or scrolled away.
    this.target = this.ambient ? Number.POSITIVE_INFINITY : this.duration;
    this.speed = 1;
    this.run();
  }

  to(t: number, speed = 1) {
    this.started = true;
    this.resumeOnReturn = false;
    this.target = clamp(t, 0, this.duration);
    this.speed = speed;
    this.run();
  }

  pause() {
    this.halt();
    this.state = 'paused';
    this.updateControl();
  }

  private toggle() {
    if (this.state === 'playing') {
      this.pause();
      this.resumeOnReturn = false;
    } else if (this.state === 'paused') {
      this.play();
    } else {
      this.seek(0);
      this.play();
    }
  }

  /**
   * For motion with no end, said while it draws: nothing it draws will change for this many milliseconds, so
   * it need not be asked again until then, and the page is left alone in between. It counts for one frame.
   */
  rest(ms: number) {
    this.wish = ms;
  }

  /** Drops the frame that was asked for, and the rest that leads to one. */
  private halt() {
    cancelAnimationFrame(this.raf);
    clearTimeout(this.timer);
    this.rested = 0;
  }

  private run() {
    this.halt();
    this.last = performance.now();
    const step = (now: number) => {
      // A rest is time that passed on purpose. Anything longer was a stall, and the clock does not jump over it.
      const dt = Math.min(now - this.last, 64 + this.rested);
      this.last = now;
      const dir = this.target >= this.t ? 1 : -1;
      this.t += dir * dt * this.speed;
      if ((dir > 0 && this.t >= this.target) || (dir < 0 && this.t <= this.target)) {
        this.t = this.target;
        this.render(this.t);
        this.state = this.t >= this.duration ? 'end' : 'idle';
        this.updateControl();
        return;
      }
      this.wish = 0;
      this.render(this.t);
      // Only motion with no end can rest, and only once its opening has played.
      this.rested = this.ambient && this.t >= this.duration ? this.wish : 0;
      if (this.rested > 24) {
        // The timer ends a little early, so that the next frame is the one the rest ends on.
        this.timer = setTimeout(() => {
          this.raf = requestAnimationFrame(step);
        }, this.rested - 8);
      } else {
        this.rested = 0;
        this.raf = requestAnimationFrame(step);
      }
    };
    this.state = 'playing';
    this.updateControl();
    this.raf = requestAnimationFrame(step);
  }

  private updateControl() {
    const moving = this.state === 'playing';
    if (moving !== this.moving) {
      this.moving = moving;
      this.watch?.(moving);
    }
    if (!this.control) return;
    this.control.textContent =
      this.state === 'playing' ? 'Pause' : this.state === 'paused' ? 'Resume' : 'Replay';
  }
}

/** Keeps a before-and-after toggle in step with the timeline. */
export const setPressed = (buttons: HTMLElement[], after: boolean) =>
  buttons.forEach((b) =>
    b.setAttribute('aria-pressed', String((b.dataset.mode === 'after') === after)),
  );

/** Runs the timeline back to just before the flip, or forward to the end. */
export const bindToggle = (timeline: Timeline, buttons: HTMLElement[], flip: number, end: number) =>
  buttons.forEach((b) =>
    b.addEventListener('click', () => {
      if (b.dataset.mode === 'before') {
        timeline.to(flip - 40, 1.8);
      } else {
        if (timeline.t < flip) timeline.seek(flip);
        timeline.to(end, 1);
      }
    }),
  );

const noise = (i: number, frame: number) => {
  const x = Math.sin(i * 127.1 + frame * 311.7) * 43758.5453;
  return '0123456789abcdef'[Math.floor((x - Math.floor(x)) * 16)];
};

/** Paints a hash that resolves from left to right. Each line is painted on its own, so the block never reflows. */
export const paintHash = (hash: HTMLElement, progress: number, frame: number, salt: number) => {
  const value = hash.dataset.v!;
  const done = Math.floor(progress * value.length);
  let at = 0;
  for (const part of $$('.p', hash)) {
    const line = value.slice(at, at + Number(part.dataset.n));
    const resolved = clamp(done - at, 0, line.length);
    at += line.length;
    if (resolved >= line.length) {
      if (part.textContent !== line || part.firstElementChild) part.textContent = line;
      continue;
    }
    let rest = '';
    for (let i = resolved; i < line.length; i++) rest += noise(i + at + salt * 97, frame);
    part.textContent = line.slice(0, resolved);
    const unresolved = document.createElement('i');
    unresolved.textContent = rest;
    part.append(unresolved);
  }
};
