# Public site

The public site for Permaily: the marketing pages, pricing, security, changelog, and the docs at `/docs`. Signing up and signing in happen in the web app at `app.permaily.com` (see [web.md](web.md)), not here. It's a static site built with [Astro](https://astro.build), and the docs use [Starlight](https://starlight.astro.build).

It lives in `site/` and is not part of the enclave image: it is hosted on AWS Amplify (`main` at permaily.com, `develop` at beta.permaily.com, pull request previews; see [deploy.md](deploy.md)) and changing it never changes the PCRs.

## Run it

Use bun, then install the dependencies and start the dev server at `http://localhost:4321`:

```bash
cd site
bun install
bun run dev
```

| Command | What it does |
| --- | --- |
| `bun run dev` | Starts the dev server |
| `bun run build` | Clears Astro's content cache, then builds the static site and the docs search into `dist/` |
| `bun run preview` | Serves `dist/`, to check the real build. Docs search only works here, not in `bun run dev` |
| `bun run check` | Type-checks pages, components and scripts |
| `bun run test` | Checks the built pages: metadata, one h1, CSP, structured data, internal links, the release measurement, that every sign-up and log-in link goes to the app, that no page holds a sign-in or sign-up form, and the redirects of the old addresses. It also runs the animation engine against a clock the test controls, with the rests a settled drawing takes between two looks, and checks the emblems of Home's security section: their shapes, how one melts into the next, and the windows a row of mail is shown through |
| `bun run test:e2e` | axe (WCAG 2.2 AA) and CSP violations in Chromium, desktop and mobile, light and dark, that `/signup/` and `/login/` land in the app (stubbed), that each sentence of Home's hero keeps to one line on a desktop, and, in Home's security section, that picking a feature shows its emblem and only its own words, leaves the drawing's mail as it was and the page under it where it was, and that without the script the first emblem and every feature's words are there. It also checks what keeps Home's drawings cheap: that no move is left waiting when nothing moves, that a drawing out of the window is not drawn and keeps its size, and that a link to a section still lands on it |

Lint and format with Biome from the repo root (`bun run lint`).

## Where things are

| Path | What's there |
| --- | --- |
| `src/pages/` | One file per page. Styles that only one page uses sit at the bottom of that file |
| `src/layouts/Base.astro` | The document head, header and footer |
| `src/components/` | Shared parts, each with its own styles |
| `src/components/demos/` | The eight animations: markup and styles |
| `src/components/brands/` | The "Sign up with Google" button, which no page shows at the moment. Its colours and font are the web app's (see [Sign up and log in](#sign-up-and-log-in)) |
| `src/scripts/demos/` | What each animation does over time |
| `src/scripts/timeline.ts` | The engine they all use |
| `src/scripts/site.ts` | The phone menu, the theme switch and form handling |
| `src/styles/tokens.css` | Type and spacing. Colours and fonts come from `packages/ui/colors.css` |
| `src/styles/global.css` | The reset, layout, type and parts used across pages |
| `src/config/site.ts` | Every outside link, so each can be changed in one place, including the app's sign-up and sign-in addresses |
| `src/config/release.ts` | Reads the latest release's PCR0 at build time for the footer |
| `integrations/csp-inline-hashes.mjs` | Adds hashes for inline scripts Astro's CSP misses (Starlight) |
| `src/data/` | Sample mail, sample vendors, logo shapes, the mail providers in the band of logos under Home's hero, and the four features of Home's security section |
| `src/assets/fonts/` | The bars font of the drawings on Home, made by `scripts/bars-font.py` |
| `scripts/` | Two tools that are run by hand: `bars-font.py` makes the bars font, and `drawings-cost.mjs` measures what Home's drawings cost a slow phone |
| `src/content/docs/docs/` | The docs pages. A file's path is its address, so `queries.md` is `/docs/queries/` |
| `src/components/docs/` | What replaces parts of Starlight, so the docs use the site's header, footer and theme |
| `src/styles/docs.css` | Starlight's look, mapped onto the site's tokens. Only docs pages load it |
| `src/scripts/docs.ts` | The theme switch on docs pages |
| `src/content/i18n/en.json` | Starlight's interface wording, where it differs from the default |
| `astro.config.mjs` | The docs sidebar, the Starlight parts replaced, how code blocks look, and the redirects of old addresses |

## The words of Home's hero

Home's hero says two sentences: the headline, and under it, dimmer and a size smaller, what to do about it. Above 760px each keeps to one line, the layout the maintainer chose on 14 November 2026. The type is sized from the width of the block the words sit in, with a container unit (`4.32cqw`, in `src/pages/index.astro`): the second sentence is the longer one, 22.3 times its type's size, so that type is set a little under a 22.3rd of the block, and the line fits at any width. The headline is 1.3 times that size, at the maintainer's word, and being the shorter sentence it still fits. It is not sized from the window. A window's width counts its scrollbar and the block's does not, so where a scrollbar always shows, type sized in `vw` was a few pixels too wide and the second sentence broke in two. A headless browser hides its scrollbar and shows nothing wrong, so `e2e/hero.spec.ts` makes the page narrower by a scrollbar's width and counts the lines. If the words change, measure the longer sentence again and set the number a little under 100 divided by its width in ems. On a phone the type holds its size and the sentences wrap.

## Animations

Each animation is a pure function of time, so it can be played, paused and scrubbed. The HTML is written in its final state, so a visitor with reduced motion or no JavaScript sees the result.

Motion uses only five things: a highlight wipes on for shared, a black bar wipes on for withheld, numbers count along with them, mail drifts, and one outline melts into another. The mail that drifts is the hero's, with the row of logos under it, that of How it works, and that of the emblem in Home's security section, whose outline is the one that melts. Nothing else should use the highlight or the bar.

### The drawings on Home

Home's hero and its second section are drawings made of sample mail in the mono face ([ADR 0032](decisions/0032-drawings-made-of-sample-mail.md)). In the hero, mail drifts past the logo's iceberg: it stays readable on the highlight in the tip, and bars cover it in the mass. In the second section, mail drifts through three stretches, "Your inbox", "Permaily" and "Other apps", and what happens to it happens in the middle one: what an app asked for darkens as it enters Permaily, takes the highlight half-way and goes on to the apps, some of it with one detail covered, and a bar wipes over each word of the rest, one here and one there, until all of it is covered at Permaily's far side, where it stops.

- **They are HTML and CSS, not canvas.** Rows of text are moved with transforms and shown through windows cut with `clip-path`. So the HTML holds the result, and both themes come from the tokens.
- **The hero is built from whole characters.** A character belongs to the tip, the mass or the sea by where its middle is, so the outline steps along the letters and follows them as they drift. The script works out each row's window from the logo's geometry and the row's position (`scripts/demos/sea.ts`), and changes it only when a character crosses an edge. Without the script the tip and the mass are cut to the logo's outline instead, so the result is still there.
- **A bar is a word set in the bars font** (`src/assets/fonts/permaily-bars.woff2`, made by `scripts/bars-font.py`). The font has two blocks, each as wide as a character of the mono face: `x`, 9px high, for the hero, where bars lie on paper, and `X`, 13px high, for How it works, where they lie over the mail they cover. A covered row is written as x's with its spaces kept, so every word comes out as one bar, and the mail is never in that row. The font is 348 bytes and the build puts it inside the stylesheet, so it needs no request.
- **The mail is not page text.** Each row's mail sits in `data-` attributes and is drawn with CSS, inside layers marked `aria-hidden`, so sample senders and subjects stay out of search results, reader modes and screen readers. The figure's label says what the drawing shows.
- **How it works covers a word at a time.** Permaily's stretch is cut into six slices. Each word of mail that was not asked for is given the slice it is covered from, the same at every build, and each slice shows a row of bars holding the words covered by then. So a bar wipes over a word as the word crosses into its slice, and nothing is worked out while it plays: all that moves is the rows (`components/demos/LineDemo.astro`).
- **Some of the mail that goes on has a detail covered.** An email can be shared without all that is in it, so in How it works some of the emails an app asked for go on with a bar over one part: a card's last digits, a door code, an amount, an address, the name of the customer who wrote. The part is `detail` in `src/data/mail.ts`. It belongs to the email, so the same email is never covered in one place and whole in another. The bar is the bars font's `X`, on the highlight.
- **The sample is 72 emails, and what apps ask for is the mail services send by themselves:** codes, receipts, orders, bookings, what customers write back. What stays is a person's own mail, and a password reset. In How it works each row holds its own six emails (`block` in `mail.ts`), so no email is on two rows at once, and every six hold one or two that pass. The hero's tip is what an app can read, so it holds only mail an app asked for (`sharedOf`), and none with a detail, since the hero has no way to cover one.
- **The hero's sea answers the pointer.** The words a mouse or a pen passes over are covered by bars, and come back a moment later (`cover` in `scripts/demos/sea.ts`). Each bar is an element added to its row's mail, so it drifts with the word under it; the styles fade it out, and it is removed when they end. At most a few hundred exist at once. A finger does nothing, since it is scrolling the page, and with reduced motion a bar comes and goes without fading.
- **Rows start at different points.** Each row carries how far ahead it starts (`data-o`) beside its pace, so no two rows begin alike.
- **Drift has no end, so it is ambient motion** (`ambient` in `timeline.ts`). It goes on after the opening has played, pauses when the drawing is scrolled away, and has a Pause control under it. With reduced motion the drawing stays still, on its result.
- **A row holds its mail more than once**, so it can move by one copy's length and look the same again. Rows that show the same mail in different layers carry the same `data-n` and `data-v`, which is what keeps the layers in step.
- **Plain words, no example numbers.** The headline and the labels of both drawings name no figure and no app.

### The emblem in Home's security section

An emblem cut out of rows of sample mail, 18 rows high and as wide, beside the four features it stands for (`components/demos/EmblemDemo.astro`, `scripts/demos/emblem.ts`, `scripts/demos/emblems.ts`, `data/security.ts`). Each feature has its own: a pair of code brackets for open source, a rack of servers for self-hosted, a seal with a check mark for verifiable builds, and a padlock for zero trust. It is highlighted mail with nothing drawn around it, like the tip of the hero's iceberg. The maintainer chose it from a board of sketches on 15 November 2026, after asking that the words stay as they are when the feature changes, for a symbol of its own for each feature, and for a smaller figure with no background.

- **The mail is one field, and a change never touches it.** Each of the 18 rows drifts to the left at its own pace, on the drawing's clock. Picking a feature does not swap the mail and does not start it again: only the shape cut out of it changes. It is mail an app asked for (`sharedOf` in `data/mail.ts`), since it stands on the highlight.
- **A shape is a distance.** Each emblem is a function that says how far a place is from its edge, below zero inside (`EMBLEMS` in `scripts/demos/emblems.ts`). Part of the way between two emblems, a place is inside when the distance between theirs is below zero, so the outline flows from one shape into the other. The first emblem grows out of the middle instead.
- **It is built from whole characters.** A character is in or out by where its middle is, as in the hero, so the outline steps along the letters and follows them as they drift. With the script, each row is one run that holds its mail twice, which the browser moves, and in it the mail, shown through a window of stretches cut with `clip-path`. The window is in whole characters of the run, so it moves with the letters, and the script writes it again only when a character crosses an edge.
- **The features take turns** until a person picks one. Then that one stays, and the mail drifts on. The control under the emblem pauses all of it, and resuming starts the turns again from the emblem that shows. The line over a feature fills while it waits its turn.
- **Only the feature that shows says its piece.** The styles hide the words of the other three once the script is there (`data-ready`). The pieces are not the same length, so the script gives the list the height of its tallest state (`reserve` in `emblem.ts`), and the page under the section holds still while the features take their turns.
- **Without motion, or without the script, nothing is lost.** The page is written with the first emblem whole, as stretches of its rows' mail, and without the script every feature shows its words. With reduced motion a pick shows its emblem at once.
- **The type is sized from the emblem's column** in steps, each seven tenths of a whole number of pixels, which is its row's height: so every row starts on a pixel, and a character keeps its shape. At its middle size the emblem is 306 pixels square. Where the two columns become one, the emblem stands between the heading and the features, and keeps its middle size.
- **To change an emblem**, edit its shape in `emblems.ts` and keep it within 8.75 rows of the middle, so that no character reaches past the drawing's side. `tests/emblems.test.ts` checks that, and what each emblem leaves out.

### What the drawings cost a slow phone

The three drawings move a few hundred rows of text, so they are built to leave a slow phone's processor alone. The measure is the work the page's main thread does in a second of playing, in Chrome at a phone's size with the processor slowed six times (`scripts/drawings-cost.mjs`). Before 15 November 2026 the script moved every row on every frame, and that second held 849 ms of work in the hero, 679 ms in How it works and 749 ms in the emblem. It now holds about 250 ms, 12 ms and 120 ms, and the screen still shows 60 frames a second. Five rules keep it there (`scripts/demos/rows.ts`, and `rest` and `watch` in `timeline.ts`):

- **The browser moves the mail, and the script does not.** Each row is handed over once, as a move of its `transform` with no end (the Web Animations API), which the browser runs away from the main thread. Nothing is written to the page for it afterwards. The same goes for the row of logos, the light in the hero's water, and the line that fills over a feature. A drawing's clock places its mail once, at the start or at the moment asked for with `?seek`, and after that the mail keeps its own time.
- **A move exists only while its drawing is moving.** Paused, with reduced motion, or out of the window, a row stands at a plain place, and it is not a layer of its own. A move that is left waiting is not free: Chrome then draws one frame more each time anything else on the page is drawn. A move on a row that the styles do not show at all, as some rows are on a phone, is worse: the browser works out the page's styles again on every frame. Such rows get no move.
- **A settled drawing only looks.** Once its opening has played, a drawing asks 20 times a second whether a character has crossed an edge (`LOOK` in `rows.ts`: 50 ms, in which the mail moves half a pixel at the most), and writes a row's window only when one has. An opening is drawn on every frame, and a change of emblem 30 times a second, which is as often as the sketch it was chosen from. How it works has nothing to look at, so its script does nothing while it plays.
- **A drawing out of the window is not drawn.** While it is more than a quarter of the window's height away, its rows are given `content-visibility: hidden`. The styles fix its size, so the page does not move and a link to a section lands where it should. Print draws all of them (`global.css`). Whole sections are not skipped this way: their sizes would be guesses, and a link to `#security` landed some 190 pixels off when that was tried.
- **Stopped text is drawn with the page.** A row is hinted as a layer only while it moves. Stopped, it adds nothing to the cost of a frame, and its text is set as sharp as the rest of the page. It can sit one device pixel away from where the same row is drawn while it moves.

`e2e/drawings.spec.ts` checks the second and the fourth rule, and `tests/timeline.test.ts` the rests of the third. To measure again, build and serve the site, then run `node scripts/drawings-cost.mjs http://localhost:4321` from `site/`, on mains power, since Chrome shows half the frames on a laptop low on battery. Two runs differ by a seventh or so. Not measured: a real phone, and any browser but Chrome.

### The band of logos under the hero

Under the hero's drawing, set apart by space and a hairline on each side, a band shows the mail providers Permaily works with: its label, "Works with", and under it a row of logos (`components/WorksWith.astro`, the list in `data/integrations.ts`). The row moves slowly from right to left on the hero's clock, so the hero's Pause control, under the drawing, stops it too. The names are real text and are read once: the second copy only fills the row as it comes round, and is hidden from screen readers. With reduced motion, or without the script, the row wraps and shows every logo at once.

Every entry is a mark beside its name: a single-colour shape from Simple Icons, in the colour of the text, as on the Companies page. Gmail's, iCloud's, Google's and Proton Mail's are in the current release. Outlook's and Microsoft's are from release 12.4.0 and Yahoo's from 9.21.0: later releases no longer carry them. Google Workspace stands beside Google's "G" and Microsoft 365 beside Microsoft's squares, since neither has a mark of its own in the set. Each mark is its owner's trademark, so read the owner's rules before adding one. An entry with no such shape is left out, not set as a name alone.

A few URL parameters help with review and screenshots:

| Parameter | What it does |
| --- | --- |
| `?still` | Shows every animation's final state |
| `?seek=sea:2700` | Freezes one animation at a moment, in milliseconds. The names are `sea`, `line`, `chat` and `emblem` on Home, then `dev`, `tokens`, `vendors` and `chain` |
| `?theme=dark` or `?theme=light` | Forces a theme. This works on docs pages too |

## Docs

The docs are part of this site, so there's one build and one deploy, and `/docs` needs no routing. They come out of the same `dist/` as everything else.

### Add a page

1. Add a file to `src/content/docs/docs/`, with a `title` and a `description`. Use `.md`, or `.mdx` if the page needs its own markup.
2. Add it to `sidebar` in `astro.config.mjs`.

Only add a page once it has real content. The first paragraph becomes the page's intro, and headings use sentence case with no period at the end. A table too wide for a phone goes inside `<div class="tbl-scroll" tabindex="0">`.

### How it's put together

- **The site's header and footer are the real ones.** `src/components/docs/` drops them into Starlight's frame. On a phone, Starlight's menu replaces the site's, and also holds the site's links.
- **The site's styles sit below Starlight's.** `docs.css` imports `global.css` into a cascade layer that ranks under Starlight's layers. Parts such as `.btn` and `.mk` still work, and the site's reset can't undo the docs' lists and spacing. Starlight's reset can win the other way, which is why `docs.css` restores the `.wrap` margin.
- **The theme choice is shared.** Docs pages read and write the same `permaily-theme` as the website. They always set `data-theme` to light or dark, because Starlight's styles and code blocks read it, so without JavaScript a docs page stays dark.
- **Colour still means something.** Code blocks are one ink colour, with dimmer comments. Don't use highlight or bar in the docs, except to mean shared and withheld.
- **There are no "Edit this page" links.** This repository is private, so they'd show outside readers a 404. If it goes public, set `editLink.baseUrl` in the Starlight config.
- **"On this page" only shows from 1152px up.** Narrower screens have no contents bar, by design, so `src/components/docs/MobileTableOfContents.astro` is deliberately empty.

### Why the build clears a cache

Astro caches rendered Markdown between builds, and that cache misses changes to Starlight's code block settings. A cached page then links to a stylesheet that no longer exists, and its code blocks lose their styling with no error. So `npm run build` passes `--force`. If it happens in development, restart with `npx astro dev --force`.

### Generated pages, not wired up yet

The API reference should be generated from the code, so it can't drift from it. It comes from [`permaily-org/permaily`](https://github.com/permaily-org/permaily), which is empty today, so there's nothing to generate it from.

It should read from a tagged release of that repository, never `main`, with the tag pinned here so a build can be repeated. A new release should trigger a rebuild, through a deploy hook from whichever host is chosen.

1. Add the API spec to the core repository. OpenAPI is the assumption, and neither the format nor the path is decided.
2. Fetch it at build time from the pinned tag.
3. Turn it into pages. Try the `starlight-openapi` plugin first, and check it supports this Starlight version. If not, write a small script that generates Markdown into `src/content/docs/docs/api/`.
4. Add a Reference group to the sidebar. SDKs and Errors can join it once they have content.

Missing: the API itself, its spec, and tagged releases.

## Sign up and log in

Every link that invites someone to sign up goes to `https://app.permaily.com/sign-up`, and every one to log in goes to `https://app.permaily.com/sign-in`. Both live in `LINKS` in `src/config/site.ts`, and pages use `LINKS.signup` and `LINKS.login`, never the address itself. Signing up is by invitation for now, which the app's sign-up screen asks for. Since 14 November 2026, at the maintainer's word, the buttons no longer say so: every call to action says "Get started", except the one button of Home's hero, which says "Join the waitlist" and has no line under it. That button goes to the app's sign-up screen like the others, and nothing keeps a waiting list yet (see [Not connected yet](#not-connected-yet)). The words around the buttons still say that it is by invitation, on Pricing, About, Terms and Privacy, and in the block that closes Pricing, so keep them when those words change. The block that closes Home no longer says so: since the same day it reads "Your inbox, your rules", then what a person decides and that Personal is free with no card. It says what a visitor gets and what it costs, where the one before it ("Start with nothing") described how far the project was. The block that closes About has the same title and button, and under them the sentence that the legal review applied in pull request 148 wrote for it: what an invitation gives today, and that the consent screen says when OpenAI is involved. Keep that sentence as the review left it.

No page shows "Sign up with Google" at the moment. Home's hero had it beside "Get started" for part of 14 November 2026, until the maintainer asked for one button there, and the component stays for the day signing up opens. It is Google's own light button, drawn as the web app draws it ([web.md](web.md), "Each provider's mark"), and it is a link to the app's sign-up screen like every other: the trip to Google starts there, on the app's own button, so it takes two clicks ([ADR 0033](decisions/0033-sign-up-with-google-on-the-site.md)). The site still writes no colour of its own. The button's colours, the colours inside the "G" and Google Sans are in `web/src/components/brands/`, which `components/brands/GoogleSignUp.astro` brings in, and `astro.config.mjs` lets the dev server read that one folder. Keep the button for a provider the app has on: `GET /api/v1/auth/providers` on the app says which are.

The site has no sign-up or log-in page of its own. The old addresses `/signup/` and `/login/` are Astro `redirects` in `astro.config.mjs`: the build writes a small page at each, with no index and a `<meta http-equiv="refresh">` to the app, which the site's CSP does not restrict, and the sitemap leaves them out. The host serves them with status 200, not a 301.

## Not connected yet

- **The waiting list.** Home's hero says "Join the waitlist" and leads to the app's sign-up screen, which asks for an invitation. Nothing takes an address and keeps it for later: not the site, which may not hold a sign-up form, and not the app. Until one of them does, someone without an invitation has nothing to join.
- **The licence and the words "Open source".** Home's security section calls the code open source, at the maintainer's word (15 November 2026). [The licence](../license.md) says in its own words that it is not an open source licence: personal use is free, and an organisation needs a commercial licence. One of the two has to change before this is released.
- **Contact form.** It doesn't send anything. It's marked `data-no-backend`.
- **Agreeing to the terms.** The legal pages (Terms, Privacy, Data processing, Subprocessors, Deleting your data) are written, without a lawyer, and `docs/legal.md` holds the reasoning and what is open. The app records which version of the terms each account agreed to, and when, since v0.11.0 (engineering item 1 there); `TERMS_VERSION` in `src/config/legal.ts` is the service's and the web app's too.
- **The domain.** `permaily.com` is set in `astro.config.mjs` and is used for canonical links, the sitemap and the social image.
- **API reference.** See [Generated pages](#generated-pages-not-wired-up-yet).
- **A 301 for the old sign-up and log-in addresses.** A rule on the Amplify app (`deploy/tofu/site.tf`) could send `/signup/` and `/login/` to the app with a 301, so links elsewhere pass on. The meta refresh pages work until then.

The product docs list the rest, including sample numbers and claims to check before publishing: see `docs/10-website.md` in the Permaily project folder.

## Publishing

Any static host works. Publish `dist/`, and set up the host to serve `404.html` for pages it can't find. The docs are in the same folder, so `/docs` needs no rewrite rule, and `public/robots.txt` already points to the one sitemap, which includes them.

`public/og.png` is the image shown when a link is shared. It's a capture of the Home hero at 1200 by 630, so capture it again if the hero changes.
