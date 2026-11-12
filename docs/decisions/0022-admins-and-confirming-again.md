# 0022 Admins, and confirming again before a sensitive change

Status: Accepted. Built (2026-11-12), not released yet.
Date: 2026-11-12

## Context

Three things were decided by the maintainer on 2026-11-12, after the operator's tool of ADR 0017 was built.

The operator could cancel any deletion, also one the person asked for. A deletion is the person's own decision: whoever holds the admin token should not be able to undo it.

A session alone was enough for every change, also the ones that decide who gets into an account: adding or removing a passkey, linking or unlinking a provider, "passkey only", recovery codes, the contact address and deleting the account. ADR 0017 accepted that a stolen session could do all of it. That is no longer accepted.

The service is managed with an admin token and shell scripts on the API host. The maintainer wants to manage it from the web app instead, signed in to an account, and to let other people help without giving them the token.

## Options

For confirming again:

1. **Nothing beyond the session** (ADR 0017 until now).
2. **A proof for each change**: a passkey or a code before each sensitive request. Safe, and tiring when several changes are made together.
3. **A proof that lasts a short time**: the person proves who they are, and the changes of the next ten minutes need nothing more. A sign-in counts as a proof.

For admins:

1. **Keep the token only.** One person, scripts, and no way to share the work without sharing the token.
2. **Roles written in the table.** Whoever writes to the table would make themselves an admin.
3. **Roles in the signed record of the account, with one super admin named by the image.** Nobody makes an admin by writing to the table, and the root of trust is public: the address compiled into the image.

## Decision

**Only the person cancels a deletion they asked for.** The operator, and an admin, cancel only a deletion the operator asked for (`closing_by: operator`). One the person asked for answers 409 `not_cancellable` ("only the person can cancel a deletion they asked for"), changes nothing and counts nothing. A person whose account is blocked and closing is unblocked first, and then cancels from the app.

**Confirming again: option 3.**

- A new session is confirmed for ten minutes from when it is made. After that the person confirms again with a passkey of the account, or, unless the account is "passkey only", with a six-digit code sent to the contact address. The proof lasts ten minutes from then.
- When the proof was made is kept in the session's own entry, signed with the rest of it (`confirmed_at`). It is never in memory: a request that lands on another enclave sees it, and whoever writes to the table cannot make a session fresh.
- These answer 403 `confirmation_needed` ("confirm it is you to continue") and do nothing while the proof is older than ten minutes: making a passkey (`POST passkeys/start`), removing one, turning "passkey only" on or off, new recovery codes, linking a provider from Settings (`POST providers/start`), unlinking one, asking for a new contact address (`POST account/address/start`), deleting the account, and every admin route that changes something. Not what only protects: closing sessions, cancelling a deletion, signing out, the name.
- Only the start of a passkey and of a change of address needs it: the ceremony and the attempt that follow last five and ten minutes, and are bound to the account.
- `GET /api/v1/me` says until when the session's proof is fresh (`session.confirmed_until`, absent when it is not). `GET /api/v1/auth/confirm` says the ways the account can confirm now (`passkey` when it has one, `email` unless it is "passkey only").
- Confirming with a passkey asks only for the account's own passkeys (`allowCredentials`). By mail, the message has a code and no link, subject "Confirm it is you on Permaily". An account asks five times an hour; the address's messages are counted with the sign-in messages, and an address that took ten wrong codes today gets no code (`too_many_requests`, said to the person, since it is their own address). A "passkey only" account is told `passkey_required` (409, "this account confirms with a passkey").
- An attempt or a ceremony made to confirm a session confirms that session and nothing else: it signs nobody in, changes no address, and confirms no other session of the same account. Nothing made for a sign-in or for an address confirms. A closing account can confirm.

**Admins: option 3.**

- **The super admin** is whichever account has the address compiled into the image as its contact address: `super_admin` in `deploy/enclave.json` (`hello@permaily.com`), passed as `-super-admin`. It is worked out from the signed record each time and never stored.
- **Other admins** are accounts whose signed record says so: who made them one, and when (`admin: {by, since}`). Only the super admin sets and unsets it, through the enclave, through `change` like everything else of the record. A list of pointers (`pk=admins`) finds them; it proves nothing, the record does.
- **An admin's powers work only while the account is "passkey only"**: every admin route answers 403 `passkey_only_needed` ("turn on passkey only to use the admin panel") otherwise. So whoever reads an admin's mail, and a provider that lies, is not an admin. Every admin route that changes something needs the fresh proof, which for such an account is a passkey.
- **What an admin does** is what the operator's routes do, through the same narrow `OperatorService`, plus the admins, the log and the counts. It holds nothing that changes an address or a way in. An admin who is not the super admin acts on no admin's account, theirs included, and does not set or remove a provider's application: 403 `super_admin_only` ("only the super admin can do that"). Nobody blocks or deletes the super admin's account through the panel (403 `super_admin_only`, "the super admin's account is not blocked or deleted here"), and nobody removes the super admin (403 `super_admin_only`, "the super admin stays the super admin: its address is compiled into the image").
- **Someone who is not an admin** gets 404 `not_found` on every route under `/api/v1/admin/`, whatever path and method: the same answer as for a route that does not exist. Signed out, every route answers 401 `unauthorized`, as every route with a session does.
- **The admin log.** Every admin action through the panel writes an entry: when, which admin, what kind, to which account or invitation. It is signed with where it is stored, kept 400 days by the time signed in it (`pk=admin-log`), and read by admins, the newest first. It names accounts by their identifier; the address shown is read from the account's record when the log is read, so an account that is gone shows none. It is never in the enclave's log, which stays kinds and counts.
- **The counts on `/status`** count admin actions with the kinds that exist, plus `admin_made` and `admin_removed`. They stay the public trace.
- **The person is told by mail** when the account is made an admin ("Your Permaily account is now an admin") and when it stops being one ("Your Permaily account is no longer an admin"), with the rules of the other notices.
- **The token stays.** `/v1/admin/...` on the API host and the scripts stay, for the first invitation and for automation. They cannot make admins, and are not written in the admin log.

**Two enclaves (a rule from now on).** Everything a request may need on another enclave lives in the storage; memory holds only what can be lost (a cache) or counted apart by each enclave (a limit). The proof's time is in the session's entry, the roles in the records, the log in entries. The attempts and ceremonies of confirming use the machinery of sign-in, which is in the storage since 2026-11-12 (ADR 0017, "Two enclaves at once"): a confirmation started through one enclave ends through the other.

## Decided while building

- Every answer that is the person's own account (`GET me`, a confirmation, the name, the contact address, deleting and cancelling it) says what it is in the admin panel (`admin`) and whether it signs in with passkeys only (`passkey_only`), so the web app shows the panel's "turn on passkey only" screen without a request that is refused.
- `PUT` and `DELETE /api/v1/admin/admins/{id}` answer the list of admins after the change (200). Making an admin of who is one, or of the super admin, changes nothing; removing who is no admin changes nothing. An account that is not there is 404.
- An admin's request is decided with the record read when the request starts: whether it is an admin, the super admin, "passkey only", and whether its proof is fresh. The limits on the target account are checked again inside the change of the target's record.
- A deletion asked through the panel is `closing_by: operator`, as one asked with the token.
- The log has one entry per invitation made, with the invitation's identifier, and one per provider's application set or removed, without an account. The entry is written after the action; when it cannot be written, the action stands and the enclave logs that an entry of that kind was not written. Two entries in the same nanosecond are in no fixed order.
- `GET stats` counts at most 1000 accounts, read from their signed records, and the invitations as their list shows them (1000 at most). `complete` is false when there were more, or when a record could not be read.
- Pages of the log take `limit` 1 to 100 (50 without one) and `after`, the `next` of the page before.
- Dev mode takes the super admin's address from `-super-admin`, `admin@permaily.test` by default.

## What it rests on, and what is left

- **The super admin is whoever controls `hello@permaily.com`** until that account is "passkey only": whoever reads that mailbox can sign up or sign in as the super admin. Turning "passkey only" on is the first thing to do with it. Changing the address means a new image.
- **The log can be deleted by whoever writes to the table.** Entries cannot be written or changed by hand, since they are signed, but they can be removed. The counts on `/status` stay the public trace, and they are one enclave's and in memory.
- **The admin token remains**, with the powers of ADR 0017, outside the admin log. Whoever holds it can still block and delete every account, the super admin's included, and cannot make admins.
- **An admin's power over availability** is the operator's: they can block and delete accounts that are not admins'. The person is told by mail of each, as with the token.
- **A stolen session that is still fresh** (ten minutes after a sign-in or a confirmation) can still make the sensitive changes. After that it needs a passkey or the mailbox.
- **Attempts and ceremonies of confirming are sealed entries** (2026-11-12, ADR 0017): they end through either enclave, once, and their tries are counted in each enclave's memory, so with two enclaves a code takes ten tries at most.
