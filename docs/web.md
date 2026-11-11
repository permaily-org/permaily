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

Signing in, signing up, the session and the Sign-in section of Settings talk to the server. Every other screen, and the rest of Settings, still runs on sample data in `web/src/data/sample.ts`, behind the session. States that need special data are reached with a `?state=` parameter.

| Route | Screen | States |
| --- | --- | --- |
| `/sign-in` | Sign in: the address, then the code of the message, or a passkey, which asks for no address. Also where the link of the message arrives, and where a recovery code is used | `#attempt=<id>&token=<token>` asks whether to sign in with the link |
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
| `/settings` | Profile, sign-in, notifications, appearance and data. Sign-in is the server's: passkeys, "passkey only", recovery codes and sessions | |
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

## Passkeys, recovery codes and sessions

The API is the one of [ADR 0017](decisions/0017-accounts-and-sign-in.md), under `/api/v1/` (`web/src/lib/api.ts`): `auth/passkey/start` and `finish`, `auth/recovery`, `passkeys` (with `start`, `finish` and `{id}`), `account/passkey-only`, `account/recovery-codes`, and `sessions`.

- **The browser's part** is in `web/src/lib/webauthn.ts`. The server's options go through `PublicKeyCredential.parseCreationOptionsFromJSON` or `parseRequestOptionsFromJSON`, and the answer comes from `toJSON()`, when the browser has them; when it has not, the same shapes are made by hand, with binary values as base64url without padding. A browser without WebAuthn, or a page that is not a secure context, is offered no passkey anywhere.
- **Signing in with a passkey** is one action on the sign-in screen and asks for no address: the browser shows the passkeys it has, and the passkey says which account it is for (`web/src/pages/auth/PasskeySignIn.tsx`). The prompt opens only when the person asks. A prompt the person closed is said calmly ("No passkey was used"), as a status and not as an error: a browser does not say whether it was closed, ran out of time or found no passkey. A refusal by the server says what may be so (the passkey was removed from its account, or the wait was too long) and what to do. Leaving the screen closes a prompt that is still open.
- **A recovery code** is for someone who lost every passkey of an account that signs in with passkeys only: "Use a recovery code" on the sign-in screen asks for the address and one code (`web/src/pages/auth/RecoveryStep.tsx`). The code is typed with or without dashes, in any case, and is written in groups of four as it is typed (`web/src/lib/codes.ts`, shared with invitations). Every refusal by the server is one message, whether the code is wrong or used, the address has no account, the account takes no codes or too many were tried: it never says whether an address has an account. Only what cannot be a code at all (not 16 letters and digits) and no network read differently.
- **Settings, Sign-in** (`web/src/pages/settings/signin/`) reads `GET passkeys` and `GET sessions` when it opens and again after every change. When it cannot be read it says so and offers to try again; a session that ended leads to the sign-in screen.
  - **Passkeys**: each with its name, the day it was made and whether it is synced. "Add a passkey" asks for a name first, suggesting the browser and system ("Firefox on Mac", with a number when that is taken), then opens the browser's prompt. Should the server refuse the name, or not answer, the browser's answer is kept in the form's memory and sent again, so no second passkey is made. Rename is done in place. Remove asks first, and says that the passkey may stay listed on the device or in the password manager; for the last one it says that mail signs in afterwards. The last passkey of an account that signs in with passkeys only has no Remove: the row says why.
  - **"Passkey only"**: On or Off, with what it means behind the info icon and in the question that turning it on asks: a message to the address no longer signs in, recovery codes are the only other way in, and Permaily cannot let anyone in either. Without a passkey it is not offered, and the row says to add one. Turning it off asks too: mail signs in again and the codes stop working.
  - **Recovery codes** are shown once, when the server gives them, in a dialog that Escape and a click outside do not close (`RecoveryCodes.tsx`): ten numbered codes in monospace, "Copy all" (one a line), that they will not be shown again, and a box to tick ("I've kept these codes") before Done takes them away. Closing or reloading the tab meanwhile makes the browser ask first. The row shows how many are left. "Make new codes" asks first, saying how many codes stop working.
  - **Sessions**: how many devices are signed in, and under Review each one as a browser and a system read from what it said of itself (`web/src/lib/device.ts`), "This browser" for the current one and when the others were last used, to the hour at best, which is all the server keeps. Another device is signed out from here; this one from the account menu.
- **Focus** follows what changed: a form's field when it opens and the action that opened it when it closes; the heading of a question or of the codes, never the action that answers it, so Enter answers nothing unread; the action that opened a dialog when it closes; the section when the row that had focus is gone. What changed without moving focus is said in a status for screen readers.
- **Nothing of this is stored.** A recovery code, a browser's answer and the server's name for a ceremony never go to `localStorage`, `sessionStorage`, the address or a log. Recovery codes are in the page's memory only while they are on screen, and "Copy all" puts them on the clipboard because the person asked.

`Dialog` (`web/src/components/ui/Dialog.tsx`) takes `dismissable={false}` for a dialog that must be answered, and gives focus to the element marked `data-dialog-focus` when it opens.

To try passkeys by hand, the page's host must be the relying party and its certificate must be one the browser accepts, which the dev server on `localhost` is not. Serve the built app from the dev enclave on its own name (`-domain permaily.test -port 18443`, with `web/dist` copied to `internal/webui/dist` before building) and start Chromium with `--host-resolver-rules="MAP *.permaily.test 127.0.0.1"` and `--ignore-certificate-errors-spki-list=<base64 of the SHA-256 of the certificate's public key>`: Chromium refuses WebAuthn on a page with a certificate error, and with that flag the certificate counts as valid. A virtual authenticator from the DevTools protocol (`WebAuthn.enable`, `WebAuthn.addVirtualAuthenticator` with resident keys and user verification) stands in for a device.

The build keeps every asset as a file of its own (`assetsInlineLimit: 0` in `web/vite.config.ts`): a font put in the stylesheet as a `data:` URL is refused by the enclave's `Content-Security-Policy`.

Not built yet: signing in with Google, GitHub, Meta or Microsoft, changing the name or the contact address, and deleting the account. Outside its Sign-in section the Settings page still shows sample data.

## TODO

- [ ] After a sign-in with a recovery code the app opens where the person was going. Leading them to Settings to add a passkey would save a step.
- [ ] A passkey the server refused when it was made stays in the authenticator, and so does one that was removed here. `PublicKeyCredential.signalUnknownCredential` could tell the authenticator, where the browser has it.
- [ ] Try passkeys on real authenticators and in Safari and Firefox: the flows were run in Chromium with a virtual authenticator. Safari may want the prompt opened straight from the click, before the request for the options.
