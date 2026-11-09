# packages/ui/AGENTS.md: shared design tokens

Plain CSS shared by the web app (`web/`) and the public site (`site/`), imported by relative path (no package manager involved, so the enclave build stays independent of the site). Visual rules: [../../docs/design.md](../../docs/design.md).

- `colors.css` is the only place colors are defined. Change a color here, never in `web/` or `site/`; `scripts/check-design-system.ts` fails on raw color values elsewhere.
- The web app is embedded in the enclave image, so any change here changes PCR0.
