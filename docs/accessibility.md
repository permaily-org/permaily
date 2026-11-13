# Accessibility

Target: WCAG 2.2 AA for the web app and the public site.

## How it is checked

- `site/e2e/a11y.spec.ts`: axe (WCAG 2.0/2.1/2.2 A and AA tags) on every public page, in light and dark themes, on desktop and mobile Chromium, with reduced motion emulated. Any violation fails CI.
- Biome's a11y rules on the web app and the site. Some are warnings today (TODO below).
- Before each release, manually: keyboard only, a screen reader (VoiceOver or NVDA), 200% zoom, and reduced motion.
- The message view's frame and the link page, by hand in the same pass (ADR 0026, "Dark theme, height, keyboard, zoom"): Tab goes through the head's controls, into the frame's links in order and out; a screen reader announces the frame by its title ("Message: <subject>"), the label above it as text, a link by its text, the box of a remote image as "Remote image not loaded" and its `alt`; at 200% zoom the frame scrolls inside itself and the label stays in view; on the link page focus starts on the `h1`, then the address, the button "Open <host>" and "Back to the message". `web/tests/pages/inbox/message-html.test.tsx` runs axe and the focus order in jsdom; the frame's content is outside the app's DOM there.

## Rules

- Accessible names contain the visible text (`label-content-name-mismatch`); add context with visually hidden text (`.sr-only`), not with an `aria-label` that replaces it.
- Scrollable regions are focusable (`tabindex="0"` on overflowing `<pre>`).
- Body text and code comments use `--ink-2` or stronger on `--sunken`; `--ink-3` is for large or non-essential text only.
- Animations respect `prefers-reduced-motion` and the HTML holds their final state.

## TODO

- [ ] Fix the web app a11y warnings Biome reports (`useSemanticElements`, `noAutofocus`, `useKeyWithClickEvents`, `noStaticElementInteractions`, `noNoninteractiveTabindex`, `useAriaPropsSupportedByRole`), then make those rules errors in `biome.json`.
- [ ] axe tests for the web app screens (Playwright, both themes) once it is served by a dev enclave in CI.
