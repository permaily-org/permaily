# Permaily web app

The web app people use to decide what each app, agent or vendor can read from their mailbox.

## What it covers

| Section | Purpose |
| --- | --- |
| Overview | Who can read what, and anything waiting for a decision |
| Connections | Each app, agent and vendor, what it can read, what it has read, and revoking access |
| Rules | Rules that define which emails match, including community packs |
| Never share | Mail no connection can ever receive, such as password resets and login codes |
| Activity | A receipt for every email that leaves, showing who got it, which parts and why |
| Accounts | Connected mailboxes and how Permaily reaches them |
| Settings | Profile, sign-in, notifications and your data |

Apps and agents also send people to the consent screen. There they see a preview of exactly what would be shared before they allow it.

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

## Design language

- Black means withheld, and the highlight color means shared. Nothing else uses either.
- Monospace is for machine facts, such as addresses, hashes and timestamps.
- Hairline rules instead of cards, 2px corners, and no shadows or gradients.
