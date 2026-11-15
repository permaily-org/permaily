# packages/ui/AGENTS.md: shared design tokens and base rules

Plain CSS shared by the web app (`web/`) and the public site (`site/`), and read by the mail's build (`packages/mail`), imported by relative path (no package manager involved, so the enclave build stays independent of the site). Visual rules: [../../docs/design.md](../../docs/design.md).

- `colors.css` is the only place colors are defined. Change a color here, never in `web/` or `site/`; `scripts/check-design-system.ts` fails on raw color values elsewhere.
- The web app is embedded in the enclave image, so any change here changes PCR0.
- The mail cannot use CSS variables: `packages/mail` copies the values into the templates when it builds. After changing a color here, run `bun run build` in `packages/mail` and commit the templates (that changes PCR0 too).
- `interaction.css` is the cursor rule of both apps (the hand on what can be clicked, not-allowed on what is disabled; docs/design.md, "Design language"). The web app imports it into `@layer base`, the site into `site/src/styles/global.css`.
