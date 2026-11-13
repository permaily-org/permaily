# Design

Visual design of everything people see: the web app on `app.permaily.com`, the public site and the mail the service sends. Screens and routes of the web app: [web.md](web.md).

## Tokens

Colors and font stacks live in `packages/ui/colors.css`, the single source for the web app and the public site. The web app exposes them as Tailwind theme values in `web/src/styles/index.css`, such as `bg-paper`, `text-ink-2` and `bg-highlight`; the site uses the CSS variables directly and adds its own layout and type scale in `site/src/styles/tokens.css`. Tailwind's default colors, type sizes and shadows are switched off, so only the design system's values are available. Colors follow the system's light or dark setting, and `data-theme="light"` or `"dark"` on `<html>` overrides it.

Fonts are Instrument Sans for text and JetBrains Mono for machine facts, both bundled with the app (`@fontsource-variable`), so pages make no requests to font services. Google's sign-in button alone is in Google Sans Medium, which Google's guidelines fix, bundled as a file of its own (see "Providers' marks").

## Design language

- Black means withheld, and the highlight color means shared. Nothing else uses either.
- Monospace is for machine facts, such as addresses, hashes and timestamps.
- Hairline rules instead of cards, 2px corners, and no shadows or gradients.
- No explanatory text on the page. A fact worth keeping but not worth the space goes behind an info icon (`InfoTip`), and only when people may need it.
- Every screen works from a 375px phone up, and nothing scrolls sideways. Below 768px the sidebar becomes a top bar with a drawer, and below 1024px an icon rail. Pages use `pagePadding` from `web/src/lib/styles.ts`. Tables drop their secondary columns before they would overflow, by the width they get rather than the window's, and hide a column in every row so the rest stay aligned.

## The logo

An iceberg in two pieces beside the name: a small tip above a gap, and a large mass below it. It is measured from the name's own letters, in Instrument Sans at weight 600: the flat top of the mass sits on the x-height, the gap is the gap under the dot of the i, and the keel reaches the foot of the y.

- **Where it is.** The headers of the web app (`Logo` in `web/src/components/Logo.tsx`: the sidebar, the top bar and the pages that stand outside the shell) and of the public site and its docs (`site/src/components/Logo.astro`, in `Header.astro`). The mail keeps the name as text (see "Mail").
- **Its size.** The mark is 20px high, which sets the letters at the size the name had as text, so a header keeps its height. It is the drawing made for that size, with the tip, the gap and the mass on whole pixels: don't scale it.
- **Its colour.** The colour of the text around it. On a dark surface the tip alone takes the highlight: the tip is the part that shows, so the colour rule holds. On a light surface the logo is one colour.
- **Its name.** In the web app it is a picture named "Permaily"; on the site the link around it carries the name and the picture is hidden from assistive technology. It is not text, so it does not grow with the text size.
- **The icon.** `favicon.svg` (the drawing made for 16px), `favicon.ico` (16, 32 and 48px) and `apple-touch-icon.png` (180px, a full square with no alpha channel), in `web/public` and `site/public`: an ink tile, the mass in paper and the tip in the highlight. The two folders hold the same bytes.
- **Generated files.** `logo.ts` beside each component and the six icon files are made with the logo, outside this repository. Never edit them by hand, and replace them together. The icon files hold the values `--ink`, `--paper` and `--highlight` had when they were made, so they are made again when one of those changes.

## Mail

The messages the enclave sends (sign-in, codes, notices) look like the app: paper, ink and hairlines, 2px corners, no shadow, a single left-aligned column of 560px at most that works from 320px. The wordmark is text. Each message is a few blocks (ADR 0025): a lead in semibold, paragraphs at 16px, a code, a link, facts and small print in `--ink-2`.

- The code of a sign-in or a confirmation is the reason the message exists: monospace, 40px (34px on a phone), on `--sunken`, on one line that never wraps.
- The link is a button labelled "Sign in", the app's primary button: `--ink` filled, its label in `--paper`, 16px medium, 48px tall, as wide as its label and left-aligned. Ink is the colour of text and turns light in the dark theme, so it is not the black of the bars; neither the bars nor the highlight are used in mail. It stays lighter than the code above it. Under it is the address in full, in monospace at 13px in `--ink-2`, breaking anywhere so it never overflows: text to read or copy, not a second link.
- Facts (when, how, which browser) are rows between hairlines, the label in `--ink-2`; a time is in monospace.
- Fonts are the system stacks the tokens fall back to, and nothing is fetched: mail clients load no web font, and an image would tell the sender the message was opened.
- Light values are inline, so a client that ignores styles shows the light theme; the dark values are in a `prefers-color-scheme` rule for the clients that read it. Every pair is AA in both (the lowest is `--ink-2` on `--paper`, 6.3:1).
- No words beyond the text's but the wordmark and the button's label: no preheader, footer, marketing, illustrations, social links or unsubscribe link.

The components are in `packages/mail`, and the templates they make are in `internal/domain/mail/templates`. Mail clients have no CSS variables, so the values are copied there from `packages/ui/colors.css`, and `scripts/check-design-system.ts` fails on one that is no longer a token's.

## Providers' marks

The one exception to the tokens. A button that signs in with a provider shows the provider's mark in the provider's own colours, and Google's and Facebook's buttons take the colours their guidelines fix ([web.md](web.md), "Each provider's mark"). Those colours are written only in the files directly in `web/src/components/brands/` (`marks.tsx` and `brands.css`): they are not tokens, and nothing else uses them. The marks are the providers' artwork, never redrawn or recoloured, and appear only on the provider's sign-in button. Inside a mark, black and the highlight mean nothing of ours: GitHub's Invertocat is black in the light theme because GitHub asks for it.

## Checks

`scripts/check-design-system.ts` (part of `make check`) fails on raw color values outside `packages/ui/colors.css` and the providers' marks, on a colour in the mail's templates that is not a token's value, (the `.tsx` and `.css` files directly in `web/src/components/brands/`), and on em dashes used as punctuation in copy. It first runs fixed cases that show the exception lets nothing else through: a colour anywhere else in the app, in a folder below `brands/` or next to it, or in the site still fails. A line containing `design-check-ignore` is skipped.

## TODO

- [ ] Look at the mail in real clients: Gmail (web, Android, iOS, which invert colours on their own), Apple Mail, Outlook for Windows and Outlook.com. Only Chromium was tried (ADR 0025).

- [ ] Replace the arbitrary Tailwind values in the web app (`p-[13px]`, `w-[80px]`, about 100 today) with theme tokens, then make the rule an error. List them with `bun scripts/check-design-system.ts --warnings`.
- [ ] Remove the inline `style={{...}}` uses in the web app (8 today), then make the rule an error.
- [ ] Confirm the dark value of `--bar`: the web app used `#65665d` and the site `#3c3d37`; `packages/ui/colors.css` now uses `#65665d` for both.
