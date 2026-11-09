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
bun run lint
```

The production build is embedded in the enclave binary (`internal/webui`) and is part of PCR0, so it must stay reproducible. See [build.md](build.md).

Design tokens live in `web/src/styles/index.css` as Tailwind theme values, such as `bg-paper`, `text-ink-2` and `bg-highlight`. Tailwind's default colors, type sizes and shadows are switched off, so only the design system's values are available. Colors follow the system's light or dark setting, and `data-theme="light"` or `"dark"` on `<html>` overrides it.

## Screens

Every screen runs on sample data in `web/src/data/sample.ts`, and nothing talks to a server yet. States that need special data are reached with a `?state=` parameter.

| Route | Screen | States |
| --- | --- | --- |
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

## Design language

- Black means withheld, and the highlight color means shared. Nothing else uses either.
- Monospace is for machine facts, such as addresses, hashes and timestamps.
- Hairline rules instead of cards, 2px corners, and no shadows or gradients.
- No explanatory text on the page. A fact worth keeping but not worth the space goes behind an info icon (`InfoTip`), and only when people may need it.
- Every screen works from a 375px phone up, and nothing scrolls sideways. Below 768px the sidebar becomes a top bar with a drawer, and below 1024px an icon rail. Pages use `pagePadding` from `web/src/lib/styles.ts`. Tables drop their secondary columns before they would overflow, by the width they get rather than the window's, and hide a column in every row so the rest stay aligned.
