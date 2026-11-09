# web/AGENTS.md: SPA

The web app (React, TypeScript, Vite, Tailwind v4, React Router), managed with **bun** (not npm or pnpm). Screens and routes: [../docs/web.md](../docs/web.md). Visual design: [../docs/design.md](../docs/design.md). The build output is embedded in the enclave binary and is part of PCR0: [../docs/build.md](../docs/build.md).

## Rules

- Use bun for everything: `bun install`, `bun add <pkg>`, `bun run <script>`. Commit `bun.lock`; builds use `--frozen-lockfile`.
- After changing dependencies, update the `node_modules` hashes in [../nix/web.nix](../nix/web.nix) (Nix and CI print the expected value).
- The bun version must match nixpkgs in `flake.lock` (see the `web` job in `.github/workflows/ci.yml`).
- The build must be reproducible: no timestamps, build IDs, absolute paths or source maps in `dist/`.
- No external origins: the enclave sends `Content-Security-Policy: default-src 'self'`. Fonts and assets are bundled; no CDNs, analytics or remote fonts.
- Talk to the backend with relative URLs; the app is served on `app.permaily.com` by the enclave.

## Commands

```sh
bun install
bun run dev
bun run build
bun run --cwd .. lint   # Biome for the whole repo
bun run typecheck
```
