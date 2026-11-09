# Design

Visual design of everything people see: the web app on `app.permaily.com` and the public site. Screens and routes of the web app: [web.md](web.md).

## Tokens

Colors and font stacks live in `packages/ui/colors.css`, the single source for the web app and the public site. The web app exposes them as Tailwind theme values in `web/src/styles/index.css`, such as `bg-paper`, `text-ink-2` and `bg-highlight`; the site uses the CSS variables directly and adds its own layout and type scale in `site/src/styles/tokens.css`. Tailwind's default colors, type sizes and shadows are switched off, so only the design system's values are available. Colors follow the system's light or dark setting, and `data-theme="light"` or `"dark"` on `<html>` overrides it.

Fonts are Instrument Sans for text and JetBrains Mono for machine facts, both bundled with the app (`@fontsource-variable`), so pages make no requests to font services.

## Design language

- Black means withheld, and the highlight color means shared. Nothing else uses either.
- Monospace is for machine facts, such as addresses, hashes and timestamps.
- Hairline rules instead of cards, 2px corners, and no shadows or gradients.
- No explanatory text on the page. A fact worth keeping but not worth the space goes behind an info icon (`InfoTip`), and only when people may need it.
- Every screen works from a 375px phone up, and nothing scrolls sideways. Below 768px the sidebar becomes a top bar with a drawer, and below 1024px an icon rail. Pages use `pagePadding` from `web/src/lib/styles.ts`. Tables drop their secondary columns before they would overflow, by the width they get rather than the window's, and hide a column in every row so the rest stay aligned.

## Checks

`scripts/check-design-system.ts` (part of `make check`) fails on raw color values outside `packages/ui/colors.css` and on em dashes used as punctuation in copy. A line containing `design-check-ignore` is skipped.

## TODO

- [ ] Replace the arbitrary Tailwind values in the web app (`p-[13px]`, `w-[80px]`, about 100 today) with theme tokens, then make the rule an error. List them with `bun scripts/check-design-system.ts --warnings`.
- [ ] Remove the inline `style={{...}}` uses in the web app (8 today), then make the rule an error.
- [ ] Confirm the dark value of `--bar`: the web app used `#65665d` and the site `#3c3d37`; `packages/ui/colors.css` now uses `#65665d` for both.
