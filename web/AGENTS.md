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
- The same goes for a recovery code, what the browser answers about a passkey and the server's name for a ceremony: they stay in the memory of the component that uses them. Recovery codes are shown once and are gone when the person says they kept them (`src/pages/settings/signin/RecoveryCodes.tsx`).
- A refusal of a recovery code is one message for every reason (`recoveryRefused` in `src/pages/auth/messages.ts`): it must not say whether the address has an account. A passkey prompt opens only when the person asks, and one they closed is a status, not an error.
- Passkeys go through `src/lib/webauthn.ts` and nothing else calls `navigator.credentials`. Passkeys need the page's host to be the relying party: see "Passkeys, recovery codes and sessions" in docs/web.md for how to run them locally.
- Opening a sign-in link never signs anyone in: the link may come from someone else, to get a person into an account that is not theirs. The page asks `auth/email/look` whose sign-in it is, shows the address the server gave and calls `auth/email/finish` only after the person says so (`src/pages/auth/LinkConfirmation.tsx`). Keep both actions of that screen the same weight, and focus off them when it opens.
- The text after starting a sign-in must not say or imply whether the address has an account, nor promise a code: the message may carry only the link.
- The app leaves for another site in one place, `leaveFor` in `src/lib/providers.ts`, and only for an address the server answered when a sign-in with a provider was started: never for one read from the address bar. Offer only the providers `auth/providers` lists, in the fixed order, named in words; draw or load no mark of theirs.
- How a sign-in with a provider ended arrives in the fragment (`#provider_confirm`, `#provider_error=<reason>`, `#linked=<provider>`) and is taken out by `src/lib/arrival.ts` like the sign-in link. Anyone can write a fragment: turn it only into fixed words (`providerProblems`, `linkProblem` in `src/pages/auth/messages.ts`), say no more than the reason does (`provider_failed` covers many causes: do not guess), and say "linked" only when the server's list agrees. Nothing of it is stored; the router's state carries only that a confirmation was asked for and where the person was going.
- The code step (`src/pages/auth/CodeStep.tsx`) is shared by signing in by mail and by confirming an address for a provider: a change to it changes both.
- Every route except `/sign-in` and `/sign-up` sits inside `RequireSession` in `src/App.tsx`. A new route goes there.
- An account that is being deleted shows `AccountClosing` and nothing else of the app: `RequireSession` does that for every route inside it, so a new route needs nothing more. A request refused with `account_closing` is told to the session by `src/lib/api.ts`; a part of the app that calls the API passes its failures through `failed` of `useSession`, which answers null when the session ended or the account is closing, and then says nothing.
- Deleting the account never happens by default: the dialog takes focus on its question, Enter deletes nothing, and the person types the account's address first (`src/pages/settings/account/DeleteAccount.tsx`). Dates go through `Intl` (`src/lib/closing.ts`), in the person's own time zone.

## Commands

```sh
bun install
bun run dev             # needs an enclave in dev mode for /api: see "Develop" in docs/web.md
bun run build
bun run --cwd .. lint   # Biome for the whole repo
bun run typecheck
```
