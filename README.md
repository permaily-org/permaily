# Permaily website

The public site for Permaily: the marketing pages, pricing, security, changelog, the sign-up and log-in screens, and the docs at `/docs`. It's a static site built with [Astro](https://astro.build), and the docs use [Starlight](https://starlight.astro.build).

## Run it

Use Node 24 (see `.nvmrc`), then install the dependencies and start the dev server at `http://localhost:4321`:

```bash
npm install
```

```bash
npm run dev
```

| Command | What it does |
| --- | --- |
| `npm run dev` | Starts the dev server |
| `npm run build` | Clears Astro's content cache, then builds the static site and the docs search into `dist/` |
| `npm run preview` | Serves `dist/`, to check the real build. Docs search only works here, not in `npm run dev` |
| `npm run check` | Type-checks pages, components and scripts |

## Where things are

| Path | What's there |
| --- | --- |
| `src/pages/` | One file per page. Styles that only one page uses sit at the bottom of that file |
| `src/layouts/Base.astro` | The document head, header and footer |
| `src/components/` | Shared parts, each with its own styles |
| `src/components/demos/` | The nine animations: markup and styles |
| `src/scripts/demos/` | What each animation does over time |
| `src/scripts/timeline.ts` | The engine they all use |
| `src/scripts/site.ts` | The phone menu, the theme switch and form handling |
| `src/styles/tokens.css` | Colours, type and spacing, with the same values as the design system |
| `src/styles/global.css` | The reset, layout, type and parts used across pages |
| `src/config/site.ts` | Every outside link, so each can be changed in one place |
| `src/data/` | Sample mail, sample vendors and logo shapes |
| `src/content/docs/docs/` | The docs pages. A file's path is its address, so `packs.md` is `/docs/packs/` |
| `src/components/docs/` | What replaces parts of Starlight, so the docs use the site's header, footer and theme |
| `src/styles/docs.css` | Starlight's look, mapped onto the site's tokens. Only docs pages load it |
| `src/scripts/docs.ts` | The theme switch on docs pages |
| `src/content/i18n/en.json` | Starlight's interface wording, where it differs from the default |
| `astro.config.mjs` | The docs sidebar, the Starlight parts replaced, and how code blocks look |

## Animations

Each animation is a pure function of time, so it can be played, paused and scrubbed. The HTML is written in its final state, so a visitor with reduced motion or no JavaScript sees the result.

Motion uses only three things: a highlight wipes on for shared, a black bar wipes on for withheld, and numbers count along with them. Nothing else should use those two meanings.

A few URL parameters help with review and screenshots:

| Parameter | What it does |
| --- | --- |
| `?still` | Shows every animation's final state |
| `?seek=hero:2700` | Freezes one animation at a moment, in milliseconds |
| `?seek=grid:off` | Shows the access grid after its connection turns off |
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

The API reference and the pack catalogue should be generated from the code, so neither can drift from it. Both come from [`permaily-org/permaily`](https://github.com/permaily-org/permaily), which is empty today, so there's nothing to generate them from. The Packs page has one hand-written row until then.

Both should read from a tagged release of that repository, never `main`, with the tag pinned here so a build can be repeated. A new release should trigger a rebuild, through a deploy hook from whichever host is chosen.

**API reference**

1. Add the API spec to the core repository. OpenAPI is the assumption, and neither the format nor the path is decided.
2. Fetch it at build time from the pinned tag.
3. Turn it into pages. Try the `starlight-openapi` plugin first, and check it supports this Starlight version. If not, write a small script that generates Markdown into `src/content/docs/docs/api/`.
4. Add a Reference group to the sidebar. SDKs and Errors can join it once they have content.

Missing: the API itself, its spec, and tagged releases.

**Pack catalogue**

1. Add `packs/` to the core repository, with one YAML file per pack. The docs already show `packs/travel.bookings.yaml`.
2. Add a `packs` collection to `src/content.config.ts`, with a loader that reads those files from the pinned tag, and a schema that mirrors the pack format, so a malformed pack fails the build.
3. Replace the table under Catalogue in `packs.md` with a component that renders the collection. Give each pack a page through `src/pages/docs/packs/[id].astro`, using Starlight's `StarlightPage`.

Missing: the `packs/` folder, a final pack format, as `travel.bookings` is the only example, and the contribution process.

## Not connected yet

- **Forms.** Contact, Sign up and Log in don't send anything. They're marked `data-no-backend`, and the Google and Microsoft buttons do nothing.
- **Sign up and Log in links.** They go to the pages here until the app has its own address. Change them in `src/config/site.ts`.
- **Terms, Data processing and Subprocessors.** These links are placeholders until the pages have had legal review.
- **The domain.** `permaily.com` is set in `astro.config.mjs` and is used for canonical links, the sitemap and the social image.
- **API reference and pack catalogue.** See [Generated pages](#generated-pages-not-wired-up-yet).

The product docs list the rest, including sample numbers and claims to check before publishing: see `docs/10-website.md` in the Permaily project folder.

## Publishing

Any static host works. Publish `dist/`, and set up the host to serve `404.html` for pages it can't find. The docs are in the same folder, so `/docs` needs no rewrite rule, and `public/robots.txt` already points to the one sitemap, which includes them.

`public/og.png` is the image shown when a link is shared. It's a capture of the Home hero at 1200 by 630, so capture it again if the hero changes.
