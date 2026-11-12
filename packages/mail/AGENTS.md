# packages/mail/AGENTS.md: the mail's design

React Email components that write the HTML templates of the mail the enclave sends (ADR 0025). Nothing here runs in the enclave: `bun run build` writes `internal/domain/mail/templates/*.html`, which are committed and embedded in the Go binary. The words are not here; they are blocks in Go (`internal/domain/mail`, `internal/services/accounts`). Visual rules: [../../docs/design.md](../../docs/design.md), "Mail".

## Rules

- One component for each kind of block in `src/blocks.tsx`, and a kind is added in Go (`mail.Kind`, `mail.Text`) and here together. Values are markers (`@@.Name@@`), control flow our own elements (`<pm-range>`, `<pm-if>`, `<pm-else>`), turned into `html/template` actions in `src/templates.tsx`.
- Nothing remote: no image (the wordmark is text), font, stylesheet, script, `url(...)` or `@import`. No words beyond the text's but the wordmark and the action's button label (`actionLabel`, "Sign in"): no preheader, footer of other words, marketing or unsubscribe link. Only the action block links, once: its button; the address under it is text. `internal/domain/mail`'s tests fail on each of these.
- Colours come from `packages/ui/colors.css` through `src/tokens.ts`, never written here; `scripts/check-design-system.ts` checks the templates. Black and the highlight are not used.
- The build is deterministic: commit the templates it writes, and `bun run check` must pass (CI's `repo` job).
- Own `bun.lock`, built with the bun of `flake.lock` (1.3.13); exact versions, at least two weeks old. Nothing of this package is in the image or the web app.
- The preview's "Send" and "Spam" tabs send the message to a third party (Resend, a spam checker): never use them. It shows sample values only.
- `fixtures/` is written by Go (`TestThePreviewShowsWhatTheServiceSends -update` in `internal/services/accounts`); never by hand.

## Commands

```sh
bun install --frozen-lockfile
bun run build       # write the templates into internal/domain/mail/templates
bun run check       # fail when the committed templates are not what the components make
bun run dev         # preview every message the service sends, with its sample values, on :3030
bun run test
bun run typecheck
```
