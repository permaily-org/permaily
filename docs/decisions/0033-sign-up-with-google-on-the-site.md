# 0033 "Sign up with Google" on the public site

Status: Accepted, built, not in use.
Date: 2026-11-14

Later the same day the maintainer asked for one call to action in Home's hero, "Join the waitlist", with no line under it. No page shows this button now. The component, the classes it uses in the web app's folder and the dev server's setting stay, unused, for the day signing up opens. If that day's hero does not bring the button back, remove all three.

## Context

The maintainer asked for Home's hero to be one block of text across the page with two calls to action under it, "Get started" and "Sign up with Google". Four things constrain a Google button on the site:

1. **Google's rules for the button.** Its guidelines fix the light button's colours, the font (Google Sans Medium, 14 on 20), the padding and the "G" in its standard colours, and name "Sign up with Google" as one of the texts ([web.md](../web.md), "Each provider's mark").
2. **The site writes no colour.** Colours come from `packages/ui/colors.css`, and the design check refuses a raw colour anywhere under `site/` on purpose: its own cases include a file of providers' marks in the site.
3. **The site has no sign-up of its own.** Every sign-up link goes to the app's sign-up screen, and signing up is by invitation ([site.md](../site.md)).
4. **The app decides which providers are on.** Google and GitHub were on in production on the day of this record.

## Options

- **A plain secondary button with the words only.** There is no colour to write. Google's guidelines ask for the "G" on a button that offers its sign-in, and the maintainer asked for Google's button.
- **Google's button with its colours written in the site.** The simplest code. It needs the design check to let the site write providers' colours, which reverses a rule the check states on purpose.
- **Google's button with its colours kept where they are.** The site draws the "G" from a copy of its paths and takes every colour, and the font, from the web app's folder for providers' marks.

## Decision

The third.

- **`site/src/components/brands/GoogleSignUp.astro`** is Google's light button as the app draws it: white, a 1px border, the "G" at 20px, Google Sans Medium, 40px tall, in both themes. It holds the paths of the "G" and no colour. The colours inside the "G" that the app writes as attributes are also classes in `web/src/components/brands/brands.css`, for this copy.
- **It is a link to the app's sign-up screen**, like every sign-up link of the site. The trip to Google starts there, on the app's own button.
- **"Get started" stands beside it**, the design system's button at the same height. Under both, "Sign up for free. No card required.", in the maintainer's words. The site's other calls to action say "Get started" too, since the same day, and the app's sign-up screen is where the invitation is asked for.
- **The dev server may read `web/src/components/brands`**, since the font is a file there. The build needs no setting.

## Consequences

- The site's build now reads two files of the web app. Moving or renaming them breaks the site's build, which says so.
- The site ships Google Sans Medium, 23 KB, under the licence already listed in `third-party-notices.md`.
- Signing up with Google from the site takes two clicks. A link that starts the trip at once needs a way into the app's sign-up screen that does it, which is not built.
- If Google is turned off in the app, this button has to go. Nothing checks that.

## Not verified

- **Google's reading of a button that is a link.** Its guidelines describe the button that starts its sign-in. This one looks the same and leads to the screen that holds that button. Nobody asked Google.
- **The "G" outside Chrome.** It is the app's artwork unchanged, which uses a filter and a gradient inside the drawing, and here it was looked at in Chrome only.
