# 0025 Mail with a design, written once and sent as text and HTML

Status: Accepted, built, not released.
Date: 2026-11-12

## Context

Every message the enclave sends (the sign-in message, the codes that confirm a session or a new address, the notices of ADR 0017) was plain text written in Go and sent through SES with `Body.Text` alone (ADR 0021). The maintainer asked for mail with a coherent design, the same product as the web app and the site, and to try React Email for it. Three things constrain how:

1. **No JavaScript runs in the enclave**, and everything compiled into it is covered by PCR0 and must build byte for byte the same on x86_64 and arm64 (ADR 0001).
2. **What a message says is security.** A sign-in message carries a code and a link; a notice carries no code, no link and no identifier, and names a browser only from fixed words (ADR 0017); the code that confirms an address carries no link (ADR 0017, ADR 0022). HTML must not add a link, a word, a fetched image or anything that identifies the person.
3. **A message's words should exist once.** Two copies (a text and an HTML template per message) drift, and the next messages (mailboxes, connections) would have to be designed one by one.

## Options

- **Keep plain text.** Nothing to verify, nothing to look at.
- **React Email rendering at run time** (a JavaScript runtime in the enclave, or HTML rendered on the parent instance). The first puts a runtime and its dependencies under PCR0; the second hands the words of every message, codes and links included, to the untrusted host.
- **HTML templates written by hand in Go.** No tooling, but no shared components with the design system either, and a design that is hard to look at while it is made.
- **React Email at authoring time only**, generating plain `html/template` files that are committed and embedded in the Go binary, filled by Go from one structure of blocks.

## Decision

The last one.

- **One source of words.** A message is a short list of blocks (`internal/domain/mail`): a lead, paragraphs, a code, an action (a link), facts (when, how, which browser) and small print. The words stay where they were, in `internal/services/accounts`, once. From the blocks Go writes both parts: `mail.Text`, which for every existing message is byte for byte the text sent before (golden files in `internal/services/accounts/testdata/mail`, written before the change), and `mail.HTML`.
- **React Email only at authoring time.** `packages/mail` holds one layout and one component for each kind of block, built with React Email's components. `bun run build` there renders them into `internal/domain/mail/templates/*.html`, with values as `html/template` actions. The templates are committed and embedded with `go:embed`: what is in the image is plain HTML anyone can read in the repository, the Go build needs no bun, and Nix needs nothing new. CI's `repo` job builds them again and fails when the committed files differ (`bun run check`); the build is deterministic (no time, no random identifier, prettier's fixed output).
- **Both parts in one message.** `sesmail` puts `Body.Html` next to `Body.Text` in the same `Simple` content, and SES sends multipart/alternative. No configuration set, no tracking, no other call (ADR 0021 stands). A message whose HTML could not be written goes as text alone.
- **Nothing remote.** No image (the wordmark is text), font, stylesheet, script, form, frame, `url(...)` or `@import`. The fonts are the system stacks the tokens fall back to.
- **No link the text does not have.** The only `href` is the action's, whose address the text shows. In the HTML the action is a button labelled "Sign in" (React Email's `Button`), with the address in full under it in small type, as text rather than a second link, so it can be read or copied and the message keeps one link. Notices name the app's host as text, as they did, and carry no link; the codes that confirm a session or an address carry none.
- **The same words.** The visible words of the HTML are the text's, in order, after the wordmark, with one exception: the label of the action's button ("Sign in"), fixed in the template, before the address. The plain text has no label: its link is the address on a line of its own, as before. No preheader, no footer of other words, no marketing, no unsubscribe link: these are messages about an account's security.
- **Escaped where inserted.** `html/template` escapes every value in its context; a link that is not `https` becomes `#ZgotmplZ`.
- **The design's colours are the tokens.** `packages/mail` reads `packages/ui/colors.css` when it builds; `scripts/check-design-system.ts` fails on a colour in the templates that is not a token's value. Black (the bars, `--bar`) and the highlight are not used. The action's button is the web app's primary button: filled with `--ink` and labelled in `--paper`, which turn over in the dark theme (a light button on a dark page), so it is the colour of text, not the black that means withheld. It is no wider than its label and 48px tall, lighter than the code, which stays the reason for the message.
- **A preview of what the service sends.** `internal/services/accounts` writes each message's blocks, with sample values, to `packages/mail/fixtures/*.json` and checks they are current (`TestThePreviewShowsWhatTheServiceSends`); `bun run dev` there shows them, so the preview cannot show a message the service does not send. The same fixtures drive the checks of the HTML in `internal/domain/mail`.

## Consequences

- The image holds seven small HTML templates and `html/template`, from the standard library; no new Go module. PCR0 changes with any template, which is a committed file, so reproducibility holds by construction.
- A new message gets the design by being written as blocks. A new kind of block needs a component, a template, its text form in `mail.Text` and the checks.
- React Email's package and its preview server (Next.js) are tooling of `packages/mail`, with their own `bun.lock`; nothing of them is in the image or the web app, so `web/bun.lock` and `nix/web.nix` do not change, and `third-party-notices.md`, which lists what is in an artifact, does not list them.
- Mail clients differ. What was checked: the rendered HTML of every message in Chromium at 360 and 720 pixels, light and dark, and at 320 pixels that the address under the button wraps rather than overflows. Outlook for Windows draws the button's fill and padding from its cell (`mso-padding-alt`), since `html/template` drops the conditional comments React Email's `Button` writes for it; that is reasoned, not seen. Not checked yet: Gmail (web and apps, which invert colours on their own), Apple Mail, Outlook (Windows, which ignores most CSS) and Outlook.com.
- HTML is a larger surface than text for a mail client's own bugs. What limits it: no remote content and no active content, which the tests fail on.

## What the tests hold

`internal/domain/mail` (100% of statements), on every fixture: `TestEveryMessageFetchesNothing`, `TestEveryLinkIsOneTheTextHas`, `TestTheHTMLSaysWhatTheTextSays` (the wordmark and the button's label are the only words the text does not have), `TestTheLinkIsTheButton`, `TestNothingIdentifyingIsInTheMarkup`, `TestEverythingInsertedIsEscaped`, `TestEveryMessageIsSmall` (under 40 KB; Gmail clips near 102 KB), `TestEveryMessageFollowsTheSystemTheme`, `TestTheCodeStaysOnOneLine`. `internal/services/accounts`: `TestEveryMessageReadsAsItsGoldenText`, `TestEveryMessageIsTextAndHTMLFromItsBlocks`, `TestThePreviewShowsWhatTheServiceSends`. `internal/sesmail`: `TestSend` (both parts), `TestAMessageWithoutHTMLGoesAsTextAlone`, and the errors still hold no address, subject, text or HTML.
