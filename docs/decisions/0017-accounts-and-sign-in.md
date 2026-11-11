# 0017 Accounts, sign-in and how account data is kept

Status: Accepted. Not built.
Date: 2026-11-11 (decided on 2026-11-09)

## Context

F1 has one test mailbox behind a token. F2 needs people: accounts, ways to sign in, sessions, and a rule for what the operator may see of them. The operator must be able to help a user (see who is registered and their contact address), and must not be able to take an account over by writing to the table.

## Decision

**Ways to sign in.** Passkeys (relying party `app.permaily.com`), email (one message with a link and a six-digit code), Google, GitHub, Meta and Microsoft. A provider account is linked to an existing account on its own when the provider says the address is verified and it is the account's. Signing in with ChatGPT is left out for now.

**Contact address.** Every account has one, verified. Recovery goes through it. An account can switch to "passkey only": email and every provider are turned off for sign-in, and recovery codes are issued instead.

**Sign-up by invitation.** Single-use codes, made by an operator with a command-line tool and the admin token, of which the image holds only the hash.

**Sessions.** Seven days without use, thirty days at most, listed and closed per device. Cookies carry the `__Host-` prefix, so the public site at the apex cannot read or overwrite them.

**No second check for sensitive actions.** Changing how one signs in, approving a connection or deleting the account asks for nothing beyond the session. Accepted risk: a stolen session can do all of it until it ends.

**Deleting an account.** Its connections are revoked at once and the account is locked; after seven days in which it can be cancelled, its keys are destroyed.

**Four ways to keep a piece of data.**

| Treatment | What | Why |
|---|---|---|
| Sealed (encrypted, only an enclave of the deployment reads it) | Bodies, attachments, raw mail, sender, recipients, subject, snippet, attachment names, Gmail confirmation codes, the text of the user's rules, the approved query of each connection, receipts, connection tokens, webhook and provider secrets | The operator must never see it |
| Readable and signed (plain text with a code only the enclave can make) | Contact address, name, language, account state, dates, ways to sign in (kind, passkey public key, provider and subject), mailbox (forwarding address, state, retention), connection (vendor, state, dates, schedule) | The operator sees it to give support, and cannot change it: an address written into the table by hand would be a way into the account |
| Hash only | Sessions, invitations, email codes, recovery codes | Whoever reads the table cannot use them |
| Plain | Identifiers, sizes, expiry times, counters | Needed to index and expire |

The public promise becomes: we see your account data, never your mail.

**Changes to account data go through the enclave** (the operator's tool), never by hand in the table: what is signed would stop verifying.

**Outgoing mail** (sign-in links and codes, verification, notices) goes through Amazon SES from a subdomain of its own, with the role the enclave already has. Sign-in codes pass through AWS, which is trusted already. By email the user is told only of security and access: a sign-in from a new device, a connection approved, a connection about to end, a change in how the account is signed into. Everything else is shown in the app.

**English only** in the first version, for the app and for the mail it sends.

## What it rests on, and what is left

- The records whose going back would matter here (ways to sign in, the "passkey only" switch, account state) go through `store.Anchored` (ADR 0016), so an earlier copy of them is refused.
- A provider that lies about a verified address can take over the account with that address. The "passkey only" mode is the way out for whoever does not accept that.
- SES starts in a sandbox: production access has to be asked of AWS.
- To design while building: the operator's tool, rate limits on sign-in and codes, and the exact wording of each message.
