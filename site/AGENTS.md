# site/AGENTS.md: public site

Astro 7 + Starlight, managed with **bun**, hosted on AWS Amplify from `main` (`amplify.yml` at the repo root). Not part of the enclave image. Structure, commands and conventions: [../docs/site.md](../docs/site.md). Visual design: [../docs/design.md](../docs/design.md).

## Rules

- Colors and fonts come from `packages/ui/colors.css`; never define colors here. The "Sign up with Google" button takes Google's from `web/src/components/brands/`, and writes none itself.
- Keep the CSP strict: no external origins, no new inline event handlers. Inline `<script>` is hashed automatically; inline `style` attributes are tolerated only until the TODO in [../docs/threat-model.md](../docs/threat-model.md) is done.
- Never show an example hash as if it were the real build: the footer reads the latest release (`src/config/release.ts`).
- Copy is honest about status: say "planned" for anything not released.
- Signing up and signing in happen in the web app: link with `LINKS.signup` and `LINKS.login` from `src/config/site.ts`, never a hard-coded address, and don't add a sign-in or sign-up form here. Signing up is by invitation for now. Buttons say "Get started", and the one in Home's hero says "Join the waitlist"; Pricing, About, Terms and Privacy say that it is by invitation, and Home's hero and the block that closes Home don't ([../docs/site.md](../docs/site.md), "Sign up and log in").
- Home's drawings must stay cheap on a slow phone. The browser moves their rows, so don't set a transform from the script on every frame, and don't leave a move waiting on something that stands still. Read "What the drawings cost a slow phone" in [../docs/site.md](../docs/site.md) before changing one, and measure with `scripts/drawings-cost.mjs` after.
- `bun run check`, `bun run build`, `bun run test` and `bun run test:e2e` must pass.
