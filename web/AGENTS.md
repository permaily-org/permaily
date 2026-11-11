# web/AGENTS.md: SPA

The web app (React, TypeScript, Vite, Tailwind v4, React Router), managed with **bun** (not npm or pnpm). Screens and routes: [../docs/web.md](../docs/web.md). Visual design: [../docs/design.md](../docs/design.md). The build output is embedded in the enclave binary and is part of PCR0: [../docs/build.md](../docs/build.md).

## Rules

- Use bun for everything: `bun install`, `bun add <pkg>`, `bun run <script>`. Commit `bun.lock`; builds use `--frozen-lockfile`.
- After changing dependencies, update the `node_modules` hashes in [../nix/web.nix](../nix/web.nix) (Nix and CI print the expected value).
- The bun version must match nixpkgs in `flake.lock` (see the `web` job in `.github/workflows/ci.yml`).
- The build must be reproducible: no timestamps, build IDs, absolute paths or source maps in `dist/`.
- No external origins: the enclave sends `Content-Security-Policy: default-src 'self'`. Fonts and assets are bundled; no CDNs, analytics or remote fonts.
- Talk to the backend with relative URLs; the app is served on `app.permaily.com` by the enclave.
- The session is the server's `HttpOnly` cookie and nothing else. Never put a sign-in code, a link's token, an invitation or anything of the session in `localStorage`, `sessionStorage`, the address or a log; take them out of the address bar before anything renders (`src/lib/arrival.ts`).
- The text after starting a sign-in must not say or imply whether the address has an account, nor promise a code: the message may carry only the link.
- Every route except `/sign-in` and `/sign-up` sits inside `RequireSession` in `src/App.tsx`. A new route goes there.

## Commands

```sh
bun install
bun run dev             # needs an enclave in dev mode for /api: see "Develop" in docs/web.md
bun run build
bun run --cwd .. lint   # Biome for the whole repo
bun run typecheck
```
