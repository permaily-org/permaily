# Permaily web app

The web app people use to decide what each app, agent or vendor can read from their mailbox.

## What it covers

| Section | Purpose |
| --- | --- |
| Overview | Your rules, then what each connection asks for, and anything waiting for a decision |
| Connections | Each app, agent and vendor: the query it asks for, the queries it ran, what it has read, and revoking access. Rules never appear here |
| Rules | The person's own filters. Each one holds back emails or removes details from them, for every connection or only some, before any query runs. New accounts get one suggested for sign-in and security mail |
| Activity | A receipt for every email that leaves, showing who got it, which parts and why |
| Accounts | Connected mailboxes and how Permaily reaches them |
| Settings | Profile, sign-in, notifications and your data |

Apps and agents also send people to the consent screen. There they see the query the connection asks for, and a preview of exactly what it would get, before they allow it. The approved query caps what that connection can fetch.

The proof that the running service matches the public code is a separate public page, not part of this app.

## Stack

React, TypeScript, Vite, Tailwind v4 and React Router. The app builds to static files, so it can ship inside the measured service image with the rest of Permaily. Fonts are bundled with the app, so it makes no requests to font services.

## Develop

```sh
npm install
npm run dev        # http://localhost:5173
npm run build      # type check, then build to dist/
npm run lint
```

Design tokens live in `src/styles/index.css` as Tailwind theme values, such as `bg-paper`, `text-ink-2` and `bg-highlight`. Tailwind's default colors, type sizes and shadows are switched off, so only the design system's values are available. Colors follow the system's light or dark setting, and `data-theme="light"` or `"dark"` on `<html>` overrides it.

## Screens

Every screen runs on sample data in `src/data/sample.ts`, and nothing talks to a server yet. States that need special data are reached with a `?state=` parameter.

| Route | Screen | States |
| --- | --- | --- |
| `/` | Overview: your rules, what connections ask for, and what needs a decision | `?state=first-use` |
| `/connections` | Every app, agent and vendor | `?state=empty` |
| `/connections/:id` | One connection, for example `claude` | Revoke shows what it may still hold |
| `/connections/new` | Connect an agent | |
| `/rules` | Your rules | `?state=empty` shows the suggested rule |
| `/rules/:id` | Rule editor with a live preview, for example `sign-in-and-security`, `home-address` or `new` | `/rules/new?suggested=sign-in-and-security`, `?add=<sender>` |
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
