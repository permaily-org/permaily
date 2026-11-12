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
- Storage holds three things and no more: the theme; the day a closing account is removed, in `localStorage`, with no address, id or name (`src/lib/closing.ts`); and an invitation in `sessionStorage`, only between leaving for a provider and the next load (`keepInvitation` in `src/lib/arrival.ts`). Read each back only as what it is, and wrap every use in `try`: storage can be unavailable, and the app must work without it. Anything new there needs a line in docs/threat-model.md.
- The same goes for a recovery code, what the browser answers about a passkey and the server's name for a ceremony: they stay in the memory of the component that uses them. Recovery codes are shown once and are gone when the person says they kept them (`src/pages/settings/signin/RecoveryCodes.tsx`).
- A refusal of a recovery code is one message for every reason (`recoveryRefused` in `src/pages/auth/messages.ts`): it must not say whether the address has an account. The one exception is `account_blocked`, which the server answers only after the address and the code were right. A passkey prompt opens only when the person asks, and one they closed is a status, not an error.
- Passkeys go through `src/lib/webauthn.ts` and nothing else calls `navigator.credentials`. Passkeys need the page's host to be the relying party: see "Passkeys, recovery codes and sessions" in docs/web.md for how to run them locally.
- Opening a sign-in link never signs anyone in: the link may come from someone else, to get a person into an account that is not theirs. The page asks `auth/email/look` whose sign-in it is, shows the address the server gave and calls `auth/email/finish` only after the person says so (`src/pages/auth/LinkConfirmation.tsx`). Keep both actions of that screen the same weight, and focus off them when it opens.
- The text after starting a sign-in must not say or imply whether the address has an account, nor promise a code: the message may carry only the link.
- The app leaves for another site in one place, `leaveFor` in `src/lib/providers.ts`, and only for an address the server answered when a sign-in with a provider was started: never for one read from the address bar. Offer only the providers `auth/providers` lists, in the fixed order, named in words; draw or load no mark of theirs. A provider's name on screen comes from `providerNames` in `src/lib/providers.ts` and nowhere else: `meta` is "Facebook" to a person and `meta` everywhere else.
- How a sign-in with a provider ended arrives in the fragment (`#provider_confirm`, `#provider_error=<reason>` with `&provider=<id>` when the server still knew the trip, `#linked=<provider>`) and is taken out by `src/lib/arrival.ts` like the sign-in link. A reason or a provider that is not one of the fixed sets is dropped. Anyone can write a fragment: turn it only into fixed words (`providerProblems`, `linkProblem` in `src/pages/auth/messages.ts`), say no more than the reason does (`provider_failed` covers many causes: do not guess), and say "linked" only when the server's list agrees. Nothing of it is stored; the router's state carries only that a confirmation was asked for and where the person was going.
- The code step (`src/pages/auth/CodeStep.tsx`) is shared by signing in by mail, by confirming an address for a provider (`CodeStep`, a screen) and by changing the contact address in Settings (`CodeForm`, the same form in a dialog): a change to it changes all three.
- A sign-in that ends with `account_blocked`, by a code, a link, a passkey, a recovery code or a provider, leads to the first step of the sign-in screen, through `signInBlocked` or `providerEnded` in `src/lib/arrival.ts` (the link through `linkProblem` of the session). The words are the fixed ones of `src/pages/auth/Blocked.tsx`, with `support@permaily.com` as a `mailto:` link, and name nobody. It is not an error: do not style it as one, and add no action that would try the same again.
- Changing the contact address (`src/pages/settings/account/ContactAddress.tsx`) keeps the new address, the code and the attempt in the component's memory only. That the address belongs to another account is said only after the code, as the server does.
- Every route except `/sign-in` and `/sign-up` sits inside `RequireSession` in `src/App.tsx`. A new route goes there. `RequireSession` also moves focus to the page's `h1` when the page is new to the person, so every page needs one, and a page that puts focus somewhere itself keeps it.
- Passkeys the server does not have are told to the authenticator through `forgetPasskey` and `acceptedPasskeys` in `src/lib/webauthn.ts`, and only when the server is known not to have them: never after a sign-in that failed, nor after a failure of the server's own.
- An account that is being deleted shows `AccountClosing` and nothing else of the app: `RequireSession` does that for every route inside it, so a new route needs nothing more. A request refused with `account_closing` is told to the session by `src/lib/api.ts`; a part of the app that calls the API passes its failures through `failed` of `useSession`, which answers null when the session ended or the account is closing, and then says nothing.
- A change the server may refuse with `confirmation_needed` runs through `useConfirmed()` (`src/components/Confirm.tsx`), which asks the person to confirm it's them and runs it again once. An action that ends with `isCancelled` (`src/lib/cancelled.ts`) did nothing and says nothing: put the screen back as it was. The control that starts such an action is `aria-disabled` while it waits, never `disabled`: the browser gives focus back to it when the dialog closes, and cannot to a disabled one (the tests' dialog does not show this). Every admin change uses it too (`useAdminChange` in `src/pages/admin/shared.tsx`). See "Confirming it's you" in docs/web.md.
- The admin panel (`src/pages/admin/`) is reached only through `AdminGate`: anyone whose `me` has no `admin` is led to `/`, and the sidebar shows "Admin" to admins only, before anything asks the server; an admin whose `me` is not `passkey_only` gets the screen that asks for it, and the panel asks nothing either. A provider's client secret is in its form only until it is sent: the field is emptied at once, whatever the answer. Invitation codes are shown once, in memory, like recovery codes.
- Deleting the account never happens by default: the dialog takes focus on its question, Enter deletes nothing, and the person types the account's address first (`src/pages/settings/account/DeleteAccount.tsx`). Dates go through `Intl` (`src/lib/closing.ts`), in the person's own time zone.

## Commands

```sh
bun install
bun run dev             # needs an enclave in dev mode for /api: see "Develop" in docs/web.md
bun run build
bun run test            # type check the tests, run them (Vitest, jsdom, axe) and hold the coverage thresholds
bun run --cwd .. lint   # Biome for the whole repo
bun run typecheck
```

## Tests

Tests live in `tests/`, outside `src/`, with their own `vitest.config.ts` and `tsconfig.test.json`: nothing of them is part of `tsc -b` or `vite build`. How they are written is in "Tests" of [../docs/web.md](../docs/web.md).

- Only `fetch` is played (`tests/helpers/server.ts`). Start the app with `open(address)` from `tests/helpers/app.tsx`; calling it again is a reload.
- A bug fix starts with a test that fails. A new screen that talks to the server gets a test file that runs `expectAccessible()` and, where it handles a secret, `expectNotKept(...)`.
- Find controls by role and label. A control whose last words are for screen readers only is found with `named('Edit name')`.
- After a `findBy`, React may still be moving focus: wrap what you say of focus in `waitFor`, and call `settle()` before saying something did not happen.
- Coverage thresholds in `vitest.config.ts` only go up. `src/pages/rules`, `src/pages/activity` and the other screens on sample data have none yet.
