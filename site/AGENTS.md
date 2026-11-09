# site/AGENTS.md: public site

Astro 7 + Starlight, managed with **bun**, hosted on AWS Amplify from `main` (`amplify.yml` at the repo root). Not part of the enclave image. Structure, commands and conventions: [../docs/site.md](../docs/site.md). Visual design: [../docs/design.md](../docs/design.md).

## Rules

- Colors and fonts come from `packages/ui/colors.css`; never define colors here.
- Keep the CSP strict: no external origins, no new inline event handlers. Inline `<script>` is hashed automatically; inline `style` attributes are tolerated only until the TODO in [../docs/threat-model.md](../docs/threat-model.md) is done.
- Never show an example hash as if it were the real build: the footer reads the latest release (`src/config/release.ts`).
- Copy is honest about status: say "planned" for anything not released.
- `bun run check`, `bun run build`, `bun run test` and `bun run test:e2e` must pass.
