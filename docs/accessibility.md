# Accessibility

Target: WCAG 2.2 AA for the web app and the public site.

## How it is checked

- `site/e2e/a11y.spec.ts`: axe (WCAG 2.0/2.1/2.2 A and AA tags) on every public page, in light and dark themes, on desktop and mobile Chromium, with reduced motion emulated. Any violation fails CI.
- Biome's a11y rules on the web app and the site. Some are warnings today (TODO below).
- Before each release, manually: keyboard only, a screen reader (VoiceOver or NVDA), 200% zoom, and reduced motion.

## Rules

- Accessible names contain the visible text (`label-content-name-mismatch`); add context with visually hidden text (`.sr-only`), not with an `aria-label` that replaces it.
- Scrollable regions are focusable (`tabindex="0"` on overflowing `<pre>`).
- Body text and code comments use `--ink-2` or stronger on `--sunken`; `--ink-3` is for large or non-essential text only.
- Animations respect `prefers-reduced-motion` and the HTML holds their final state.

## TODO

- [ ] Fix the web app a11y warnings Biome reports (`useSemanticElements`, `noAutofocus`, `useKeyWithClickEvents`, `noStaticElementInteractions`, `noNoninteractiveTabindex`, `useAriaPropsSupportedByRole`), then make those rules errors in `biome.json`.
- [ ] axe tests for the web app screens (Playwright, both themes) once it is served by a dev enclave in CI.
