# Web app

The web app people use to decide what each app, agent or vendor can read from their mailbox.

## What it covers

| Section | Purpose |
| --- | --- |
| Overview | Your rules, then each connection and what it asks for, and anything waiting for a decision. It opens at `/` and is out of the sidebar for now |
| Connections | Each vendor, such as Claude, with its connections underneath, which can be hidden. A vendor's page opens on its overview: its connections and what applies to the vendor as a whole, such as the mailboxes it reaches and revoking all of its connections. A connection is one query on your mail, and opens in its own tab on that page. The tab shows the query and its output for each email, readable or as the exact YAML file, when it runs (when asked, on a schedule, or on new mail through a webhook), the queries it ran and what it has read. It also lists the person's rules that apply, and ticking them there changes the rules themselves. Find any connection, and revoke one or all of a vendor's |
| Rules | The person's own filters. Each one holds back emails or removes details from them before any query runs, for every connection, every connection except some, or only the vendors and connections picked, set here or from a connection's page. New accounts get one suggested for sign-in and security mail |
| Activity | A receipt for every email that leaves, showing who got it, which parts and why |
| Accounts | Connected mailboxes and how Permaily reaches them |
| Settings | Profile, sign-in, notifications and your data |
| Sign in, sign up | The only screens open without a session. Everything else sits behind one |

Apps and agents also send people to the consent screen. There they see the query the connection asks for, and a preview of exactly what it would get, before they allow it. The approved query caps what that connection can fetch. A vendor that wants something else asks for another connection, which is approved, listed and revoked on its own.

The proof that the running service matches the public code is a separate public page, not part of this app.

## Stack

React, TypeScript, Vite, Tailwind v4 and React Router. The app builds to static files that ship inside the measured enclave image, served on `app.permaily.com`. Fonts are bundled with the app, so it makes no requests to font services.

## Develop

The app lives in `web/` and uses bun.

```sh
cd web
bun install
bun run dev        # http://localhost:5173
bun run build      # type check, then build to dist/
bun run --cwd .. lint   # Biome, from the repo root
```

The app sits behind a session, so `bun run dev` needs something to sign in to. The dev server hands `/api` and `/dev` to an enclave in dev mode on the same machine, by default the one `go run -tags dev ./cmd/permaily-enclave -port 8443 -admin-token-sha256 <hex>` starts (see [internal/AGENTS.md](../internal/AGENTS.md)). Set `PERMAILY_DEV_APP` when the enclave uses another domain or port, for example `PERMAILY_DEV_APP=https://app.permaily.test:18443 bun run dev`. Make an invitation with `scripts/invite.sh`, sign up at `/sign-up`, and read the message, which is kept instead of sent, at `http://localhost:5173/dev/outbox`. Its link names the enclave's own address: open its path and fragment (`/sign-in#attempt=...`) on the dev server instead. Without an enclave the app says it cannot be reached. The proxy belongs to the dev server only and is not part of the build.

The production build is embedded in the enclave binary (`internal/webui`) and is part of PCR0, so it must stay reproducible. See [build.md](build.md).

Design tokens, typography and the design language are in [design.md](design.md).

## Screens

Signing in, signing up and the session talk to the server. Every other screen still runs on sample data in `web/src/data/sample.ts`, behind the session. States that need special data are reached with a `?state=` parameter.

| Route | Screen | States |
| --- | --- | --- |
| `/sign-in` | Sign in: the address, then the code of the message. Also where the link of the message arrives | `#attempt=<id>&token=<token>` asks whether to sign in with the link |
| `/sign-up` | Create an account: the address and an invitation, then the code of the message | `?invitation=<code>` fills the invitation in |
| `/` | Overview: your rules, what connections ask for, and what needs a decision | `?state=first-use` |
| `/connections` | Every vendor and its connections, with search | `?state=empty` |
| `/connections/:vendor` | One vendor's overview, for example `claude`: its connections, the mailboxes they reach and Revoke all | |
| `/connections/:vendor/:connection` | One connection, in its own tab on the vendor's page, for example `claude/morning-summary` | Revoke shows what it may still hold |
| `/connections/new` | Connect an agent | |
| `/rules` | Your rules | `?state=empty` shows the suggested rule |
| `/rules/:id` | Rule editor with a live preview, for example `sign-in-and-security`, `home-address` or `new` | `/rules/new?suggested=sign-in-and-security`, `?add=<sender>`, `/rules/new?for=<vendor>/<connection>` |
| `/activity` | Receipts for every email that left | `?state=empty` |
| `/accounts` | Connected mailboxes | `?state=filter-behind`, `?state=mailbox-error` |
| `/accounts/new` | Connect a mailbox | |
| `/accounts/new/forwarding` | Forwarding setup | |
| `/settings` | Profile, sign-in, notifications, appearance and data | |
| `/consent/trip-app` | An app asks for access. The mailbox picker switches to the forwarding preview | `?state=loading`, `?state=error` |
| `/consent/claude` | An agent asks to connect and starts with nothing | `?state=loading`, `?state=error` |
| `/consent/ledgerly` | A connection asks for more | `?state=loading`, `?state=error` |

## Signing in and the session

The screens talk to the accounts API of [ADR 0017](decisions/0017-accounts-and-sign-in.md) under `/api/v1/` on the app's own host (`web/src/lib/api.ts`): `POST auth/email/start`, `POST auth/email/look`, `POST auth/email/finish`, `GET me` and `POST auth/sign-out`. Requests are JSON and same origin, which is all the server lets through.

- **The session is the server's cookie and nothing else.** It is `HttpOnly`, so the app cannot read it: when the app opens it asks `GET /api/v1/me` (`web/src/lib/session.tsx`). Without a session every route leads to `/sign-in`, and after signing in the person lands where they were going. The destination travels in the router's state, never in the address. Someone who signs out on purpose is not taken back. When the server gives no answer the app says so and offers to try again; only the server saying so signs anyone out. Coming back to the tab asks again, so a link opened in another tab, or a session that ended there, shows here too.
- **Sign in and sign up are one flow** (`web/src/pages/auth/`): an address, and to sign up an invitation, then the six-digit code of the message. The invitation can be typed with or without dashes and is shown in groups of four, as the server writes it.
- **The link of the message** opens `/sign-in#attempt=<id>&token=<token>`. The fragment is read and taken out of the address bar before anything renders (`web/src/lib/arrival.ts`), also when the link is pasted over an open sign-in screen. The same goes for `?invitation=` on `/sign-up`.
- **Opening the link signs nobody in.** Someone can start a sign-in for their own account and send its link to another person, who would then be inside that account and connect their mailbox there. So the page first asks the server whose sign-in the link is (`POST auth/email/look`, which ends nothing), then who is signed in, and shows a question (`web/src/pages/auth/LinkConfirmation.tsx`): the address, that the link signs this browser in to its account, to go on only if the address is theirs and they asked for the message, and not to if someone sent them the link. The address shown is the server's answer, never anything read from the link. Only "Sign in" calls `finish`, with the token kept in the page's memory; "Don't sign in" forgets the link. Focus lands on the question, not on an action, and neither action is styled as the one to take.
  - A browser already signed in to the account of that address opens the app: nobody is signed in again and the link is forgotten unused.
  - A browser signed in to another account is told so, with both addresses: going on changes which account it is in ("Switch account"), and "Don't switch" leaves it where it was.
  - A link that was used, has ended or was cut short gets "That link no longer works" with the form to ask for another, unless the browser is signed in already, which just opens the app. The same when the link ends between the question and the answer; a browser that is in another account then is told it is still signed in there before the app opens.
  - No answer from the server keeps the link: "Permaily can't be reached" and Try again before the question, a message under the question after it.
  - Reloading while the question shows loses the link, which is nowhere but the page's memory: the ordinary sign-in screen follows. That is accepted, and the link is not stored to survive it.
- **The text after asking never says whether the address has an account**: the server answers the same either way, so the screen says "If this address can sign in, a message is on its way". It does not promise a code either: after too many wrong codes the message carries only the link, and the code field says to open the link instead.
- **Nothing typed or received here is stored.** No code, token or invitation goes to `localStorage`, `sessionStorage`, the address or a log. They live in the page's memory until they are used.
- **Every failure has words a person can act on** (`web/src/pages/auth/messages.ts`): an address the server cannot use, an invitation that is not valid or was used, a code that is wrong or has ended, too many attempts, no network, and anything else.
- **Who is signed in and the way out** are in the account menu at the foot of the sidebar, and of the drawer on a phone: the address, and Sign out.

The build keeps every asset as a file of its own (`assetsInlineLimit: 0` in `web/vite.config.ts`): a font put in the stylesheet as a `data:` URL is refused by the enclave's `Content-Security-Policy`.

Not built yet: passkeys, signing in with Google, GitHub, Meta or Microsoft, the list of sessions, and the account's settings. The Settings page still shows sample data.
