# 0026 Mailboxes per account: how they are built

Status: Proposed. Designed on 2026-11-12 for step 2 of F2 (docs/plan.md); not built. The owner decided its open choices on 2026-11-12 (listed in "What it rests on, and what is left"), among them that messages are shown as HTML from the start ("Showing a message's HTML"). Revised the same day after an adversarial review; what changed is listed in the pull request that proposes it.
Date: 2026-11-12

## Context

ADR 0018 decided what mailboxes are: forwarding only, several per account, nothing from before the forwarding started, retention as F1 has it, a full inbox in the app, disconnecting deletes the mail, and which account is behind a mailbox is verified for Gmail by Google's signed confirmation. This record says how, exactly enough that the store, the server, the web app and the operator's side can be built at the same time by different people against interfaces that are already in the repository (see "Implementation pull requests").

It builds on: the records that refuse an earlier copy, `store.Anchored` (ADR 0016), which it changes ("Changes to the store"); the patterns of accounts (ADR 0017): signed records changed only at the version read, pointers that prove nothing, sealed entries, limits; two enclaves at once (ADR 0017, ADR 0024); confirming again and the admins (ADR 0022); public logs (ADR 0015); mail only over TLS (ADR 0011). The F1 test mailbox (ADR 0009) is retired, not moved ("The test mailbox: retired").

The rules this design keeps, from the maintainer: nothing that decides anything lives only in one enclave's memory; the operator and admins can see that mailboxes exist and how many, and cannot read mail or enter an account; logs never hold an address, a subject, a sender, content, a token, or an account, mailbox or message identifier; whoever writes to the table or the bucket cannot read mail, move a mailbox to another account, make a disconnected mailbox accept mail again, or put a mailbox back to an earlier state where that matters; mail that a sender was told is delivered is stored.

## Decision in short

- **A mailbox is an anchored record**, readable and signed, with a sealed part: who owns it, its state, when it started receiving, whether Google confirmed its Gmail account, its data key wrapped under the root secret, and sealed inside it the address and the Gmail account. What a person edits freely (the label, the retention) is a signed, versioned entry next to it that is not anchored.
- **An account's mailboxes are listed in one anchored record**, which also fences creation and removal: identifiers are reserved there, a record is made only where no commit was ever made for its name, and the list is closed before an account's mailboxes are removed. The limit of five holds with any number of enclaves.
- **An address finds its mailbox through a pointer** named by an HMAC of the address, holding only the mailbox's identifier. Ownership and state are read only from the record.
- **One random data key per mailbox**, never cached across requests. Disconnecting writes the record without it: from then on no request of any enclave opens the mailbox, and its mail is deleted by a sweep. That is a refusal enforced by approved code, not destruction proof against a later theft of the root secret ("What disconnecting does to the key").
- **Delivery never loses mail.** A message is claimed per mailbox before it is stored, stored under the identifier of the claim so that a retry writes the same items, and marked stored only after every part of it is; `250` only when every accepted mailbox holds it. Memory is bounded before anything is buffered.
- **A record whose commits were forked** by whoever writes to the bucket fails closed on every enclave and after every restart, instead of one of the two commits being chosen.
- **The inbox** lists, opens, deletes and searches one mailbox at a time, each page reading a bounded number of stored items, and shows a message formatted in a sandboxed frame or as text.
- **The test mailbox is retired**: the release that builds this has no code that reads it, its token goes, and its address answers like any unknown one.
- **The operator and admins** see counts and the readable part of each record, never an address; they can pause and resume a mailbox, never disconnect or read it; an admin does so through the actor-bound admin facade of accounts.
- **A closing account** can read, search and download its mail and disconnect mailboxes; **an account that has not agreed to changed terms** can download its mail and disconnect mailboxes. The server enforces it route by route.

## Records and layout

All in the one DynamoDB table (`permaily-mail`), the mail bucket and the anchors bucket, as F1 and accounts are. "Signed" is `envelope.Sign` with a key derived from the root secret for mailboxes (`Derive("permaily mailboxes sign v1")`), bound to where the value is stored; "sealed" is `envelope.Seal` or `SealText` under the mailbox's data key, bound to the mailbox (and message) by `envelope.AAD`.

| What | Where | Treatment | Written how |
|---|---|---|---|
| A mailbox | Record `mailbox-<id>` (`pk=system`, `sk=record#mailbox-<id>@<version>-<hash>`), its commits in the anchors bucket | Signed, readable: `v`, `id`, `account`, `state` (`active`, `disconnecting`, `cancelled`), `rcpt` (the address's HMAC, hex), `created_at`, `receiving_since`, `verified_at`, `paused_at`, `state_changes` (times of the last ten pauses and resumes), `disconnected_at`, `disconnected_by` (`person`, `account_removed`). Sealed inside it as `sealed` (bound to `mailbox-private` and the id): the address, and the Gmail account Google confirmed. The data key, wrapped under the root secret, is the record's `WrappedKey` | `store.Anchored` with `OnlyNewest`: made with `CreateNew`, changed with `GetRecordAt` and `PutRecordAt`, removed with `RemoveRecord` ("State machines") |
| An account's mailboxes | Record `mailboxes-<account id>` | Signed: `{v, account, state: open or closed, mailboxes: [{id, added_at}], made: [times of the last ten creations]}`, identifiers only | Anchored, `OnlyNewest`: made with `CreateNew`, changed at the version read, removed with the account |
| A mailbox's settings | Entry `pk=mailbox#<id>`, `sk=settings` | Signed with where it is stored: `{v, retention_days, label}`, the label sealed (`SealText`, bound to `mailbox-label` and the id) | `PutEntry` at version 0, then at the version read; absent means 90 days and no label |
| The pointer from an address | Entry `pk=rcpt#<hex HMAC-SHA-256 of the local part>`, `sk=mailbox` | Signed with where it is stored: `{mailbox, write}` (`write` random per write, as accounts' pointers) | `PutEntry` at version 0; deleted as it was read (`DeleteEntryAt`); never moved or rewritten |
| The index of mailboxes, for counts | Entry `pk=mailboxes`, `sk=<mailbox id>` | Signed: `{account}`. It proves nothing and is only counted | Written after the record is made; deleted when the record is removed |
| A message (F1's format) | Item `pk=mailbox#<id>`, `sk=msg#<message id>`; raw in S3 `raw/<mailbox id>/<message id>`, tagged with its retention | Meta (`inbox.StoredMeta`) sealed (`message-meta`); raw sealed (`message-raw`); plain identifiers, sizes, times | Written by a delivery under its claim's identifier: a retry writes the same keys. DynamoDB's TTL and the S3 lifecycle rule of its tag delete it |
| What search reads of a message | Entry `pk=mailbox#<id>`, `sk=txt#<message id>` | Sealed (`message-search`): `inbox.SearchEntry`, the senders, the subject, the first 16 KiB of the text, when it arrived | `PutEntry` at version 0 (`ErrExists` from an earlier try of the same delivery is success); the message's expiry |
| A delivery's claim, then its mark | Entry `pk=mailbox#<id>`, `sk=seen#<hex HMAC, under a key derived from the mailbox key, of the envelope sender and the SHA-256 of the raw message>` | Sealed (`message-seen`): `inbox.SeenEntry`, `{stored, message, at}` | Version 0 to claim, at the version read to take a stale claim over or to mark it stored; seven days (TTL) |
| A forwarding confirmation | Entry `pk=mailbox#<id>`, `sk=confirm#<message id>` | Sealed (`message-confirm`): `inbox.ConfirmEntry`, code, requester, when, verified, for another address, when it stops being shown | `PutEntry` at version 0; thirty days, read from the sealed `ends` |
| The mailboxes being disconnected | Entry `pk=mailboxes-disconnecting`, `sk=<disconnected_at, RFC 3339>#<mailbox id>` | Signed: `{mailbox}`. It proves nothing: the record does | Version 0, by whoever finds a record `disconnecting` without one; deleted as it was read when nothing of the mailbox is left |

- **Identifiers.** A mailbox is `mbx_` and 26 base32 characters (16 random bytes), made as accounts make theirs, and never reused. Message identifiers stay F1's: 32 hex characters, time first. `scrub` learns `mbx_`, and no message identifier goes in a log line or a logged error.
- **Why the record is readable and signed, with a sealed part.** The operator needs the state, the owner and the dates to give support and to count; none of it is the person's mail. The address is what lets anyone put mail into the person's inbox (and, from step 5, in front of their agents); the Gmail account is a second address of theirs; the label is typed by the person and may be anything. Those are sealed. ADR 0017's table listed the forwarding address as readable; this changes it.
- **Why a record of its own for the list**, not a field of the account's record: a mailbox's life has nothing to do with who gets into the account, and the account's record changes only through `change`, with the checks of sign-in. Kept apart, the mailboxes service never writes `accountDoc` and accounts never learn a mailbox's format.

**Rejected.** A pointer named by the address in plain text: the table would list every address. A scan of mailboxes at `RCPT TO`: the cost grows with the number of mailboxes. The address readable, as ADR 0017 listed it. The list as entries: an earlier copy put back would hide a mailbox from its person while it went on receiving. The label and retention in the anchored record: every rename would be a commit that can never be deleted ("What is anchored, and what is not").

## The address

- **Made of** twenty characters of `abcdefghijkmnpqrstuvwxyz23456789` (32 letters and digits, without `l`, `o`, `0` and `1`), drawn with `crypto/rand`, at `inbox.permaily.com`: 100 bits. `domain.NewMailboxAddress` makes one.
- **Collisions** are found by the pointer's write at version 0 (`ErrExists`): a new address is drawn, three times at most. With a million mailboxes the chance of one is about one in 10^18.
- **Guessing.** With a million mailboxes a made-up address finds one with a chance of one in 10^24, so `RCPT TO` is not limited per sender. Every refusal is the same `550 5.1.1 No such mailbox`: an address never made, one disconnected, one paused, one whose record cannot be read, one of another domain. An address that exists costs two reads more than one that does not, which tells only someone who guessed it already.
- **What is compared.** The local part, lowercased, at most 64 bytes; one with a `+tag` is refused (Gmail forwards to the exact address it was given). The domain must be the inbox host, compared without case.
- **The HMAC key** is `Derive("permaily mailbox addresses v1")` from the root secret: the same in every enclave of the deployment, unknown outside.
- **What the pointer says.** Only which mailbox. Whose it is and whether it accepts mail are read from the record, whose `rcpt` must be the HMAC the pointer is named by: a pointer copied under another name, or put back after a disconnect, leads to a record that refuses.

## Keys

- **One data key per mailbox**: 32 random bytes from `keys.Root.NewDataKey`, wrapped under the root secret with the context `{purpose: mailbox, mailbox: <id>}`. No KMS call: wrapping and unwrapping are AES-GCM under a key the enclave holds.
- **Not derived.** A key derived from the root secret and the identifier could not be taken away short of losing the root secret.
- **Not cached.** Every request that opens mail reads the record anyway (owner and state), and unwrapping costs microseconds, so the key is unwrapped from the record just read, put in the request's `inbox.Box`, and dropped with the request.

### What disconnecting does to the key, and what it does not

- **What it does.** Disconnecting writes the next version of the record without `WrappedKey`, at the version read; `OnlyNewest` then deletes the version before, which held it. When the mail is gone the record is removed (`RemoveRecord`): a commit says it is gone, and every stored version left is deleted. From the first write on, every read of the record by any enclave of the deployment goes through the newest commit, which has no key and later says the record is gone, so the approved code never unwraps that key again. An earlier copy of the record put back in the table is refused (ADR 0016). The wrapped key cannot be moved into another record: the wrap is bound to the mailbox identifier, and records are signed and committed.
- **What it does not.** It is a refusal enforced by the code of public releases, not cryptographic destruction. The wrapped key was in the table under the root secret: a saved copy of the table from before the disconnect, together with the root secret, decrypts a saved copy of the mail. The root secret exists only inside enclaves of public releases and in records locked by their KMS keys (ADR 0013), so that takes breaking an enclave or KMS; and every release whose KMS key still exists can open the root secret, so an old release with a flaw stays part of the boundary until its key is deleted (docs/operations.md, "Delete the key of an old release"). A request that unwrapped the key before the disconnect keeps it until it ends (at most a minute, "Disconnecting"). A KMS key per mailbox would make destruction real, at the cost of a key per mailbox and seven days to delete one; not done.
- **What stays.** The anchored commits of the record and of the account's list: the record's name (which holds the mailbox identifier), versions, times, hashes and authentication codes, for the lock period: ten years in production, then S3 may delete them. Never a key, an address or a value of a record.
- The app, the notice by mail, docs/data.md, docs/threat-model.md and ADR 0018 say exactly this and nothing stronger.

## State machines

Each machine says its states, the writes that move between them and how each is written: **create-only** (`CreateNew` for records, `PutEntry` at version 0 for entries), **at the version read** (`PutRecordAt` after `GetRecordAt`; `PutEntry` at the version read; `DeleteEntryAt` as read), or **removal** (`RemoveRecord`). Every write that fails because another enclave wrote first (`ErrChanged`, `ErrExists`, `ErrConflict`) reads again and decides again, four times at most, then fails.

### A mailbox's record

| From | To | When | Write |
|---|---|---|---|
| none | `active` | Making a mailbox, its identifier reserved in an open list | `CreateNew` |
| none | `cancelled` | A reader of the list found the identifier reserved more than ten minutes ago with no record, or the account is being removed | `CreateNew`: a tombstone `{account, state: cancelled, cancelled_at}`, no key |
| `active` | `active` | `verified_at` and the sealed Gmail account (once), `receiving_since` (once), `paused_at` set or cleared (ten in thirty days) | At the version read |
| `active` | `disconnecting` | The person disconnects, or the account is removed | At the version read: `disconnected_at`, `disconnected_by`, no key, no sealed part |
| `disconnecting`, `cancelled` | gone | Nothing of the mailbox is left (the sweep) | `RemoveRecord` |
| any | forked or lost | Whoever writes to the bucket or the table broke it | None: every read fails closed (`store.ErrForked`, `store.ErrLost`); shown as `unreadable` |

Because a record is made only with `CreateNew`, which fails wherever any commit of this deployment was ever made for its name, a removal included, nothing turns a cancelled or removed mailbox back into one that accepts mail. The fence of a reservation is the record itself: whoever reclaims writes the tombstone, and the creator's own `CreateNew` then fails.

### An account's list

| From | To | When | Write |
|---|---|---|---|
| none | `open` | The first mailbox of the account | `CreateNew` |
| `open` | `open` | An identifier added (a reservation, with its time in `made`), or taken out (its record is `disconnecting`, `cancelled` or gone) | At the version read |
| `open` | `closed` | The account is being removed | At the version read |
| `closed` | `closed` | Identifiers taken out by cleanup | At the version read |
| `closed` | gone | Every mailbox it names is `disconnecting`, `cancelled` or gone | `RemoveRecord` |

Nothing is added to a closed list. Creation reads the list at its version and writes it at that version, so a close that happens in between fails the creation's write, and the reread sees `closed`.

### An identifier in the list (a reservation)

| State | What it is | What any reader of the list does |
|---|---|---|
| reserved | In the list; no commit for `mailbox-<id>` | Counts it while younger than ten minutes; older, writes the tombstone (`CreateNew`; `ErrExists` means the record was made: read it) and takes the identifier out |
| active | The record is `active` | Counts it |
| unreadable | The record is forked or lost | Counts it; shows it as `unreadable`; only disconnecting it frees the slot |
| leaving | The record is `disconnecting` or `cancelled`, or gone | Does not count it; puts the disconnect entry back for a `disconnecting` record that has none (create-only); takes the identifier out of the list at the version read |

"Any reader of the list" is listing the mailboxes, making one, disconnecting one, `Count`, `Records`, `RemoveAccount` and the sweep. The queue of disconnects is an optimisation: a slot is never counted for a mailbox that is leaving, whether or not its cleanup ran, and the next reader finishes it.

### Invariants, and why they hold

1. **Every `active` record's identifier is in an open list of its account.** A record is made only after its identifier is in the list (making, step 2), and an identifier leaves the list only when its record is `disconnecting`, `cancelled` or gone, or after the tombstone was written, which stops the record ever being `active`.
2. **An account never has more than five mailboxes that count**, with any number of enclaves: the count is made from the list and the records it names, and the new identifier is written at the version the count was made at.
3. **Nothing is added to an account being removed**: the list is closed at its version before its mailboxes are enumerated, and a reservation older than the close is fenced by a tombstone.
4. **From the commit of `disconnecting`, no new request opens the mailbox and no new mail is accepted for it**, on any enclave: every request and every delivery reads the record, and nothing caches it.
5. **A slot is never kept by a mailbox that is leaving**: it does not count, whatever its cleanup did.

### Making a mailbox

`POST mailboxes {label}`, by an account that is open and agreed (`Standing`).

1. Read `mailboxes-<account>` at its version (none: `CreateNew` it `open`, empty). `closed`: `account_closing`. Read the record of each identifier and do what the reservation table says. Ten creations in the last thirty days (`made`): `too_many_requests`. Five that count: `too_many_mailboxes`.
2. Add the new identifier with `added_at`, and the time to `made` (the last ten kept), and write the list at the version read.
3. Draw the address and write its pointer at version 0 (`ErrExists`: draw again).
4. `CreateNew` the record: `active`, the account, `rcpt`, a new data key, the sealed address. `ErrExists` means a reader cancelled the reservation (step 2 took longer than ten minutes): delete the pointer as written, answer an internal error; the identifier leaves the list with its tombstone.
5. Write the settings entry at version 0 (retention 90, the sealed label) and the index entry. A failure here leaves a mailbox with the default settings, shown in the list; the answer is an error and the person can rename it.
6. Send the notice "a mailbox was added". Answer 201.

A failure after 2 holds a slot for ten minutes; after 3 leaves a pointer to a record that does not exist, which refuses mail like no pointer and is never handed to anyone. Nothing half made receives mail or shows an address.

### Label and retention

`PATCH mailboxes/{id}`: the record is read (it must be `active` and the caller's), then the settings entry is read and written at its version, four tries. The label is one printable line of at most 64 characters (`domain.MailboxLabel`), may be empty, and is sealed. A new retention applies to mail received afterwards: each message's expiry is fixed when it is stored. Neither needs a fresh proof; both need an open, agreed account.

### Gmail's forwarding confirmation

What Gmail sends, from what the repository holds and from Google's message: `From: forwarding-noreply@google.com`, `To:` the forwarding address it was asked to use, `Subject: (#<code>) Gmail Forwarding Confirmation - Receive Mail from <requester>`, and a body that says the requester "has requested to automatically forward mail to your email address" (naming it, in Google's message), with the code and a link. Google signs it with DKIM for `google.com`. The repository's fixture (`internal/inbox/inbox_test.go`, `gmailConfirmation`) is written by hand: its body does not name the destination, and it carries no signature. Pull request 1 adds a real one, captured in a staging rehearsal with the DNS record of its key, so that the checks below are tested on what Google sends.

- **Detected** at ingest as F1 does (sender `forwarding-noreply@google.com`, the code in the subject or the body), stored as a message of the inbox like any other, marked `confirmation`, and kept as a sealed `confirm#` entry for thirty days so the set-up shows the code without opening the inbox. A flood of fake confirmations writes no commit.
- **Verified** (`ConfirmEntry.Verified`) only when all of this holds: a DKIM signature with `d=google.com` passes; its `h=` covers `From`, `To` and `Subject`; it has no `l=` (the whole body is covered); `To` holds exactly one address and it is this mailbox's; and when the body names the address mail will be forwarded to, that is this mailbox's too. `msgauth` returns, for each signature, the header fields it covers and whether it limits the body (`inbox.AuthResult` gains `Headers []string` and `BodyLimited bool`, never stored). The requester and the code are read from the signed subject.
- **For another address** (`ForAnotherAddress`): Google signed it, and the address it names is another one. Someone sent here a genuine confirmation meant for an address they control. It is shown without its code ("Gmail sent this for another address; it says nothing about this mailbox") and changes nothing.
- **What "verified" means, everywhere it is said**: Google confirms that this Gmail account asked to forward to this mailbox. Not that forwarding is on, and not that later mail comes from it.
- **The verified Gmail account of the mailbox** (`Mailbox.source`) is the first verified confirmation that arrives while the mailbox is waiting (no `receiving_since`): written once into the record at the version read, with `verified_at`, and the notice "Gmail asked to forward" sent. Later verified confirmations, from the same or another Gmail account, are listed in `confirmations` with their requester and change nothing. Someone who learns the address can ask Gmail to forward their own account to it, and win the first arrival: the app then shows their Gmail address, with its code, as the one Google confirmed. That statement is true. Forwarding starts only when someone types the code into Gmail's settings of the requesting account; Permaily never follows the link, and the person sees which Gmail account asked before they copy anything. The set-up says so: "Copy this code into Gmail only if <requester> is your account."
- **For a mailbox that was disconnected or paused** the confirmation is refused at `RCPT TO` like any mail, and Gmail tells its account the address does not accept mail.

### Mail starts arriving

The first message that is not a confirmation sets `receiving_since` in the record (at the version read; when another enclave set it first, the earlier time stays). The state shown is `waiting` without it, `receiving` with it, `paused` while `paused_at` is set, and `unreadable` when the record cannot be read.

### Disconnecting

`DELETE mailboxes/{id}`, with a fresh proof (ADR 0022), in every standing: an account closing or that has not agreed to changed terms can leave.

1. Read the list and find the identifier (not there: `not_found`). Read the record at its version. Not the caller's account: `not_found`. Already `disconnecting`, `cancelled` or gone: finish what the reservation table says, answer 204. Forked or lost: go to "An unreadable mailbox".
2. Write the disconnect entry at version 0 (`ErrExists`: there already).
3. Write the record at its version: `disconnecting`, `disconnected_at`, `disconnected_by: person`, no key, no sealed part. `ErrChanged`: read again; another request disconnected it, the answer is the same (204). This is the moment of invariant 4.
4. Take the identifier out of the list at its version.
5. Delete the pointer as it was read, if it names this mailbox.
6. Send the notice "a mailbox was disconnected". Answer 204.

**What it promises, exactly.** From the commit of step 3: no new request opens the mailbox and no new mail is accepted, on any enclave. A request that read the record before it finishes within its own deadline and is not cut short: a read of the inbox at most one minute from its start (`mailboxes.ReadDeadline`, the HTTP server's write timeout, after which nothing is sent), a delivery at most one minute from the end of `DATA` (`mailboxes.IngestDeadline`). What a late delivery leaves is ciphertext under a key no record holds any more: the sweep's last pass waits it out (below), and anything later still, on storage that misbehaves, expires with DynamoDB's TTL and the S3 lifecycle rule (at most 365 days, the longest retention). The design does not claim the mail is physically gone at the answer.

**The sweep** (`mailboxes.Service.Sweep`) is run by every enclave every ten minutes and once right after a disconnect. It goes through the disconnect entries, the oldest first, and for each reads the record:

- `disconnecting`: make sure the identifier is out of the list and the pointer is gone; delete the mailbox's messages, a page at a time (the raw object, then the item), then every other item of its partition (`txt#`, `seen#`, `confirm#`, `settings`) and its index entry; when a pass finds nothing left and `disconnected_at` is at least three minutes old (`mailboxes.SweepGrace`: the ingest deadline, the clock difference two enclaves may have, and a minute), make one more pass, remove the record (`RemoveRecord`) and delete the entry as it was read.
- gone: delete the entry.
- `active` an hour after the entry was written: the disconnect failed before step 3; delete the entry.
- forked or lost: as "An unreadable mailbox".

Every deletion can be done twice, by two enclaves at once; `ErrNotFound` counts as done. A raw object written by a delivery after the last pass has no item and is deleted by the lifecycle rule of its tag.

**A failure in between** leaves, after 2, a mailbox exactly as it was, and the person sees an error and asks again; after 3, a mailbox that accepts nothing and that nobody can open, whose slot, pointer and mail the next reader of the list and the sweep free and delete, even when the disconnect entry was deleted by hand (the next reader puts it back). Never a mailbox that receives mail nobody can see, and never mail that can be opened after the answer said the mailbox was disconnected.

### An unreadable mailbox

A record that is forked (`store.ErrForked`) or whose newest value is gone (`store.ErrLost`) cannot be read by any enclave: it refuses mail (`550 5.1.1`), its mail cannot be opened, and it is listed as `unreadable` with its identifier and when it was added. It counts against the five. The person can disconnect it: since the record cannot say whose it is, the account's list, which is anchored too, is the proof; the disconnect entry is written, the identifier taken out of the list, and the sweep deletes the partition and the raw objects of its messages (it needs no key to delete) but never removes the record, which stays refused. The pointer cannot be found without the address and is left; it leads to the refused record. The operator sees it in `/status` (a regression marked `fork`) and in the account's records. Nothing makes it readable again: a fork is evidence that someone wrote to the bucket of commits, and it stays there for the lock period.

### Accounts removed, closing and blocked

- **Removed.** `accounts.removeLocked` calls `Mailboxes.RemoveAccount(ctx, id)` before it removes the account's record: the list is closed at its version; every identifier in it is handled as the reservation table says, with `active` ones disconnected as above (steps 2 and 3, `disconnected_by: account_removed`, no notice) and reserved ones fenced by a tombstone; then the list is removed. The sweep deletes the mail afterwards. Every step can be done twice; a failure fails the removal, which the account's loop tries again (ADR 0017).
- **Closing** (the seven days): mailboxes go on receiving. The person can read, search and download their mail and disconnect mailboxes; they cannot make a mailbox, change one or delete messages (`account_closing`).
- **Terms not agreed** (changed terms): mailboxes go on receiving. The person can list their mailboxes and messages, download messages and attachments, and disconnect mailboxes; nothing else (`terms_needed`). Leaving never requires agreeing.
- **Blocked** by the operator (decided by the owner): mailboxes go on receiving and storing. Nobody reads them while the account is blocked: every request reads the account's record (`Standing`), and a blocked account's sessions are signed out.

### Paused by the operator

For abuse (an address flooded, or mail that harms the service); decided by the owner. `paused_at` is written in the record at the version read, and the time added to `state_changes`; past ten changes in thirty days the operator gets `too_many_requests`. A paused mailbox refuses mail at `RCPT TO` with the same `550 5.1.1`, keeps everything it holds; the person reads, deletes and disconnects as before, sees "Paused by Permaily", and is told by mail. Resuming clears it. The operator cannot disconnect a mailbox or delete mail: that destroys what is the person's, and pausing stops the harm.

### Interleavings the tests cover

Two enclaves, A and B, share one store, as `enclaves_test.go` in `services/accounts` sets them up. Each case is a test of the pull request named; "S" cases are the store's (pull request 0), "M" the mailboxes service's (pull request 1).

- **S1.** A and B read a record at one version. A writes the next version. A delete marker hides A's commit; B writes the same version, and S3 takes it; the marker is removed. Every later read and write of the record, through A, through B and through a third enclave started afterwards, fails with `ErrForked`; `RemoveRecord` and `CreateNew` too. A and B record a regression marked `fork`.
- **S2.** S1 on an `Anchored` with `ForkIsLost` (the TLS identity): a read is lost; A and B both make a new identity; one commit wins at the version above the fork, and both then read it.
- **S3.** A delete marker over the newest commit, and nothing written: the record reads as before.
- **S4.** `CreateNew` through A and B at once: one succeeds, the other is `ErrExists`. `CreateNew` after `RemoveRecord`, and over a record written before commits existed: `ErrExists`.
- **S5.** A partition with 300 expired entries above one that is not: `ScanEntriesDown` with `maxRead` 100 answers `Read` 100, no entry and a `Next`, three times, then the entry. The same for `ScanMessagesDown`.
- **M1.** An account with four mailboxes makes one through A and one through B at once: exactly one succeeds, the other is `too_many_mailboxes`.
- **M2.** A reserves an identifier and stops before its record. Eleven minutes later B lists the mailboxes: B writes the tombstone and frees the slot, and makes another mailbox. A resumes: its `CreateNew` fails, its pointer is deleted, it answers an error. The account has five mailboxes, all listed, and only listed ones accept mail.
- **M3.** M2, but A's `CreateNew` lands just before B's tombstone: B's is `ErrExists`, B reads the record and keeps the identifier.
- **M4.** A reserves and stops; the account is removed through B (the list closed, the reservation fenced); A resumes: its `CreateNew` fails.
- **M5.** A reads the list to make a mailbox; B closes it to remove the account; A's write is `ErrChanged`, and A's reread answers `account_closing`.
- **M6.** A has accepted a recipient at `RCPT TO`; B disconnects the mailbox; A's `DATA` reads the record again: not stored, `451`; the retry is refused at `RCPT TO` with `550`.
- **M7.** A has read the record for delivery and is writing; B disconnects and its sweep runs: the sweep's last pass waits `SweepGrace` after `disconnected_at`, and deletes what A wrote. A write made after that (a store that held it) is a raw object without its item, under a key no record holds.
- **M8.** A and B disconnect one mailbox at once: one write of `disconnecting` wins; both answer 204; one notice.
- **M9.** A disconnects while B pauses: whichever writes second reads again; a pause of a `disconnecting` mailbox is `not_found`, a disconnect of a paused one goes on.
- **M10.** A disconnect stops after step 3; the disconnect entry is deleted by hand. B lists the mailboxes: the mailbox is not listed, the slot is free, the entry is back, and the sweep deletes the mail.
- **M11.** A and B sweep one mailbox at once: every item deleted once, the record removed once, the entry deleted once.
- **M12.** One message delivered through A and B at once: one claim wins; the other answers `451`; the sender's retry finds the mark and answers `250`; one copy.
- **M13.** A claims a message, stores part of it and stops. Within two minutes B's retry answers `451`; after, B takes the claim over with the same message identifier, writes the same keys, marks it; one copy, complete.
- **M14.** A message for X and Y; Y is paused between `RCPT TO` and the end of `DATA`: X stored, the answer `451`; on the retry X is found stored and Y refused at `RCPT TO`; X holds one copy.
- **M15.** Two confirmations signed by Google arrive through A and B at once for a waiting mailbox: one `verified_at`, one notice.
- **M16.** The record put back by hand to its `active` version after a disconnect: refused, as ADR 0016's tests show; the mailbox stays disconnected.
- **M17.** The pointer put back after a disconnect, or copied under the HMAC of another address: `550`.
- **M18.** A pauses; mail through B: `550`. B resumes; mail through A: accepted.
- **M19.** A and B change the settings at once: neither undoes the other.
- **M20.** A and B remove one account at once: every mailbox disconnected once, the list removed once.
- **M21.** A mailbox's record forked: `RCPT TO` refuses it, the list shows it `unreadable` and counts it, disconnecting frees the slot and the sweep deletes its partition.
- **M22.** A confirmation arrives through A while the set-up reads through B: B shows it.

## Delivery

Both enclaves listen on port 25 behind one address, and a connection lands on either.

### Admission, before anything is buffered

The enclave has 2 GiB (`enclave_memory_mib`), and the Go runtime is held under 1,536 MiB (`GOMEMLIMIT`, `nix/eif.nix`). What can grow with what senders do is bounded per enclave, by counted semaphores that wait a bounded time:

| Budget | Value | When it is taken | Over it |
|---|---|---|---|
| SMTP sessions at once | 64 | When a connection is accepted | `421 4.3.2 Too busy, try again later`, and the connection closes |
| Bytes of `DATA` admitted at once | 128 MiB | At `MAIL FROM`: the declared `SIZE`, or 25 MiB without one | Waits at most 10 seconds, then `452 4.3.1 Insufficient system storage, try again later` |
| Bytes of mail open for reading at once (a message, an attachment, a download, the HTML frame) | 128 MiB | Before a raw message is read from the bucket: its size | Waits at most 10 seconds, then 429 `too_many_requests` |
| Searches at once | 4 | Before a search page | Waits at most 10 seconds, then 429 |

A message held for delivery takes about three times its size at its peak (the buffer, its sealed copy, what parsing and DKIM keep), and an opened one about the same (the sealed object, the raw message, the HTML document of at most 9 MiB): 128 MiB each, so at most about 384 MiB for each side, plus four search pages of about 2 MiB, under 800 MiB in all, with the rest of 1,536 MiB for the runtime, TLS and everything else. A `DATA` larger than its `SIZE`, or than 25 MiB, is cut at that size and answered `552 5.3.4`; a `DATA` that takes more than ten minutes is answered `451 4.4.2` and frees its bytes. These are per enclave by nature: two enclaves have twice the memory. How many sessions come from one remote address is limited by the host's proxy (8), since the enclave sees connections over vsock without the sender's address; that limit is about fairness, which the host can break anyway by refusing connections, and the enclave's own budgets hold whatever it does.

### What a message may hold

Parsing (`inbox`) stops reading what is past these, and the message is still stored whole, with `StoredMeta.Partial`: the header block 256 KiB; one header field 16 KiB decoded; 100 addresses in one of `From`, `To`, `Cc`, `Reply-To`; the subject 2 KiB decoded; MIME nesting 16 deep; 500 parts; 100 attachments listed (the rest are in the raw message); an attachment's name 255 bytes before it is cleaned. Mail is never refused for its content: a sender told `250` must find it stored.

### The sequence, and what each failure leaves

1. **`RCPT TO`** (at most 10 recipients): normalise the address; refuse another domain or a `+tag`; compute the HMAC; read the pointer and check its signature; read the record; check that its `rcpt` is the pointer's name, that it is `active` and `paused_at` is unset; check this enclave's counts for the mailbox (600 messages an hour, 2 GiB a day). Two recipients of one mailbox are one.
2. **The end of `DATA`**: from here a context of one minute (`mailboxes.IngestDeadline`). Verify DKIM (at most 15 seconds, inside the minute) and parse once. Then for each accepted mailbox, one after the other:
   1. Read the record again. No longer `active`, or paused: this mailbox is **not stored**; go on with the next.
   2. Unwrap the key; read the settings for the retention.
   3. Read `seen#<digest>`. Marked stored: this mailbox is **done**. Claimed less than two minutes ago (`mailboxes.ClaimStale`): another delivery of the same message is storing it; **not stored**. Claimed longer ago: take the claim over at the version read, keeping its message identifier. None: claim it at version 0 with a new identifier. `ErrExists` or `ErrConflict`: read once more and decide again; still contended: **not stored**.
   4. Store, under the claim's identifier: the raw object (S3, which replaces what an earlier try wrote at that key), the message item (replaced the same way), the `txt#` entry and, for a confirmation, the `confirm#` entry (version 0; `ErrExists` is an earlier try's, the same message).
   5. For a verified confirmation of a waiting mailbox with no source, `verified_at` and the requester; for the first other mail, `receiving_since`: each at the version read, decided again on `ErrChanged`.
   6. Mark `seen#<digest>` stored at the version of the claim. `ErrConflict`: read it; whoever took it over writes the same identifier, and step 4 is complete either way, so this mailbox is **done**.
   Any error in 1 to 6 makes this mailbox **not stored** and the next one is tried.
3. **The answer**: every accepted mailbox done: `250 2.0.0`; otherwise `451 4.3.0 Temporary failure, try again later`. The admitted bytes are given back.

| Interrupted after | What it leaves | What the sender's retry does |
|---|---|---|
| 2.3, the claim | A claim, nothing stored | Within two minutes, `451` again; after, takes it over, same identifier |
| part of 2.4 | A raw object, maybe the item, maybe the search entry | Takes the claim over and writes the same keys again: one message, complete |
| 2.4, before the mark | The message, complete, unmarked | The same: written again over itself, then marked |
| 2.6, before the answer | The message, marked | Finds the mark: `250`, nothing written |
| Never retried (the sender gave up) | A claim that ends after seven days; at most a message without its search entry (found by the list, not by a search), or a raw object without its item (shown nowhere), each expiring with its retention | — |

So a crash never loses a message and never stores it twice: the store is idempotent because the claim decides the keys of the message's items. A message is stored twice only when a sender changes its bytes between tries (another `Received` line it adds, a new signature), when the `seen#` entry is deleted by whoever writes to the table, or when it is sent again after seven days.

### The answers, all of them

| Situation | Where | Answer |
|---|---|---|
| No TLS | `MAIL FROM` | `530 5.7.0` |
| `SIZE` over 25 MiB | `MAIL FROM` | `552 5.3.4` |
| Sessions over 64 | Connection | `421 4.3.2` |
| Bytes of `DATA` not admitted within 10 seconds | `MAIL FROM` | `452 4.3.1` |
| An address never made, disconnected, paused, cancelled, unreadable, of another domain, or with a `+tag` | `RCPT TO` | `550 5.1.1 No such mailbox` |
| The mailbox over 600 messages this hour or 2 GiB today, in this enclave | `RCPT TO` | `451 4.7.1` |
| An error reading the pointer or the record | `RCPT TO` | `451 4.3.0` |
| An eleventh recipient | `RCPT TO` | `452 4.5.3` |
| No recipient accepted | `DATA` | `554 5.5.1` |
| More than its `SIZE` or 25 MiB | During `DATA` | `552 5.3.4` |
| `DATA` over ten minutes | During `DATA` | `451 4.4.2` |
| Every accepted mailbox holds it, now or from an earlier try | End of `DATA` | `250 2.0.0` |
| Some stored, one stopped accepting meanwhile | End of `DATA` | `451 4.3.0`; the retry finds the others stored and that one refused at `RCPT TO`, so the sender bounces only that one |
| One being stored by another delivery of the same message | End of `DATA` | `451 4.3.0` |
| A storage error, or the minute passed | End of `DATA` | `451 4.3.0` |
| The connection lost | Anywhere | Nothing is marked; the sender tries again |

Retention is DynamoDB's TTL and the S3 lifecycle rule of the object's tag, as in F1: nothing in the enclave sweeps expired mail. Reads leave out what has expired.

What it costs: a refused address is one DynamoDB read. An accepted one is two anchored reads of the record (each an S3 listing of its commits, its newest commit and one DynamoDB read), one or two reads and writes of the claim, two item writes, one or two entry writes and one S3 write.

## Limits

| Limit | Value | Kept | With two enclaves |
|---|---|---|---|
| Mailboxes an account has (decided by the owner) | 5 | The account's list and the records it names | Exactly 5 |
| Mailboxes an account makes | 10 in any thirty days | `made` in the account's list | Exactly 10 |
| Pauses and resumes of one mailbox | 10 in any thirty days | `state_changes` in its record | Exactly 10 |
| Size of a message | 25 MiB (SMTP `SIZE`, as F1) | Compiled in | The same |
| Recipients of a message | 10 (as F1) | Compiled in | The same |
| Messages an hour into a mailbox | 600 | Each enclave's memory | 1,200 |
| Bytes a day into a mailbox | 2 GiB | Each enclave's memory | 4 GiB |
| Settings changes of a mailbox | 10 a minute | Each enclave's memory | 20 |
| Messages or bytes stored | None (ADR 0019: none per person in the first version; retention bounds it and the spending alarm watches it) | | |
| Messages deleted in one request | 100 | Compiled in | The same |
| Items a list page reads | 500 | Compiled in | The same |
| Entries a search page reads | 2,000 | Compiled in | The same |
| Search pages an account asks | 30 a minute | Each enclave's memory | 60 |
| Memory and sessions | "Admission" | Each enclave's memory | Each its own |

The limits in memory follow ADR 0017: a count in the storage could be set back by whoever writes to it, and none is dangerous at twice its value. The three kept in anchored records cannot be set back, and are there because each change they count is a commit that nobody can delete.

## What is anchored, and what is not

**Anchored** (`store.Anchored`, an earlier copy refused, a fork refused): the mailbox's record, for what going back would matter: its owner, its state (a disconnected or paused mailbox accepting mail again, a cancelled reservation coming alive), its key (a removed key coming back), its `rcpt`, `verified_at` and the Gmail account (undoing which account Google confirmed); and the account's list (going back would hide a mailbox from its person while it received, give back a slot, or reopen a closed list).

**How many commits that is.** S3 keeps every commit for the lock period, and `Head` refuses a record with more than 10,000 objects (`maxCommitObjects`), so ordinary use must stay far below. A mailbox's record gets at most: one when made, one for `verified_at`, one for `receiving_since`, ten pauses and resumes in thirty days, one at disconnect, one at removal: at the maximum rate, 10,000 commits last about 1,000 periods of thirty days, about 82 years. An account's list gets at most: one when made, ten reservations and ten departures in thirty days, one close and one removal: about 500 periods, 41 years. Both outlast the ten-year lock, after which ADR 0016's "The lock has an end" applies to them as to every record. Moving a record's head to a new name when its commits grow ("head rollover") is future work of the store.

**Not anchored**, and what putting back an earlier copy, or deleting one, gives whoever writes to the table:

| Record | Put back or deleted | What it gives |
|---|---|---|
| The settings (label, retention) | An earlier label, an earlier retention | The old label shown. An older retention applied to mail received afterwards: shorter, mail expires sooner; longer, it stays longer. Whoever writes to the table already controls when mail is deleted (the TTL, the lifecycle rules, deleting items), so it gives nothing they lack |
| The pointer | Put back after a disconnect, or copied under another address's name | Nothing: the record refuses it |
| A message, its `txt#`, its raw object | Deleted, or put back after the person deleted it | A message lost or shown again, until the audit log of ADR 0019 (accepted by ADR 0018). Nothing moves between mailboxes or accounts: each is sealed under its mailbox's key, bound to its mailbox and message |
| A `confirm#` entry | Put back after it ended | Nothing: the sealed `ends` hides it |
| A `seen#` entry | Deleted | A message sent again is stored twice |
| A `seen#` entry | An old mark put back | A message sent again with exactly the same bytes is not stored again: dropping mail, which whoever runs the host can do anyway by dropping connections |
| A disconnect entry | Deleted | Nothing lasting: the next reader of the list puts it back while the identifier is in the list; after that, ciphertext under a key no record holds expires with its retention |
| The index of mailboxes | Anything | Wrong counts in the admin panel |

## Changes to the store

Built first, in pull request 0, with their tests (S1 to S5) and ADR 0016 updated. The types and the interface below are already in `internal/store/scan.go`.

**The fork rule.** A commit object is written only if nothing is at its key (`IfNoneMatch: "*"` since the store of commits was built, in #51), so two commits of this deployment at one version can only be there if whoever writes to the bucket hid the first behind a delete marker while an enclave wrote the second (`TestAnchoredOverS3CommitsWithADeleteMarkerInBetween`). No legitimate race leaves two: two enclaves that write the same version get one `ErrExists`. So the rule that chose the commit with the smaller digest goes:

- `S3Commits.Head` reads every object at the highest version that has a commit of this deployment (within `maxCommitReads`), and when two or more are commits, the same digest or not, returns `store.ErrForked`. `MemoryCommits` gains `Fork(name, version, digest)` for tests.
- `Anchored` passes it on from every method that reads the head: `GetRecord`, `GetRecordAt`, `PutRecord`, `RemoveRecord` and `CreateNew` fail with `ErrForked`, which is not an `ErrNotFound`. `PutRecordAt` with a mark from before the fork writes at the forked version, gets `ErrExists`, answers `ErrChanged`, and the reread fails with `ErrForked`. Nothing writes past a fork, so a fork stays the newest version, and every enclave, after every restart, finds it: both commits stay in the bucket, which nobody can delete from, for the lock period.
- A fork found is recorded as a `Regression` with `Fork: true` (once per record and enclave), and `/status` reports it as an `AnchorRegression` with `fork: true` (`pkg/attestation`), on which `permaily-verify` fails as on any regression. The names of mailbox records, which hold identifiers, are reported as `mailbox`, as accounts' are as `account`.
- Records stored before this rule have no fork unless someone made one: the rule needs no migration. An older release that runs again (a rollback) chooses by digest as before.

**What it does to the TLS identity.** Failing closed there would leave the service without its certificate key until a new deployment. So `Anchored` gains `ForkIsLost bool`, set only on the TLS identity's: a forked record then reads as lost (`ErrLost`, wrapping `ErrForked`), and `PutRecord` with create makes it again as the version above the fork. That is what ADR 0024 already does with a lost identity: a new key made inside the enclave, nothing old comes back, the CAA records and the certificate history follow "Replace the TLS key". Whoever can fork could deny service anyway; with this they cost the deployment one new key and one certificate, and are seen in `/status`. Accounts and mailboxes do not set it: a forked account or mailbox is an error, never "none".

**`CreateNew(ctx, r) error`.** Makes a record only if no commit of this deployment was ever made for its name, a removal included, and nothing is stored under its plain name from before commits existed: `ErrExists` otherwise (`ErrForked` counts as a commit). `PutRecord(create)` stays as it is for the TLS identity and accounts, which may make a removed or lost name again; mailboxes and lists use only `CreateNew`, so a name once made is never made again. Added to `mailboxes.Records`, which `*store.Anchored` then satisfies.

**`store.Scanner`**, implemented by `*Memory` and `*AWS`:

```go
ScanEntriesDown(ctx context.Context, partition, prefix, before string, maxRead int) (*EntryScan, error)
ScanMessagesDown(ctx context.Context, mailboxID, before string, maxRead int) (*MessageScan, error)
```

Each reads at most `maxRead` items (1 to `store.MaxScanRead`, 100) of the partition whose key starts with the prefix and is strictly below `before` (`""` for from the greatest), greatest key first, with consistent reads, and returns the ones not expired, with `Read` (items read, expired included) and `Next` (the key of the last item read when it stopped at `maxRead`, `""` when nothing is left). The AWS one is a DynamoDB `Query` with `ScanIndexForward: false` and `Limit: maxRead`, which counts items evaluated, not returned; it never reads a second page. `ListEntries` and `ListMessages` stay as they are for accounts and F1's callers.

ADR 0016 and ADR 0024 change with it (in this pull request): the fork rule replaces the smallest digest and "noticed, not prevented"; `CreateNew` and `ForkIsLost` are described there.

## Access while an account is closing or has not agreed

Decided by the orchestrator of the review on 2026-11-12; the owner is told. The server enforces it, through `mailboxes.Accounts.Standing` and `mailboxes.Access` (`internal/services/mailboxes/mailboxes.go`); the web app shows only what the server allows.

| Route | Agreed, open | Closing | Terms not agreed |
|---|---|---|---|
| `GET mailboxes`, `GET mailboxes/{id}` | Yes | Yes | Yes |
| `POST mailboxes` | Yes | 409 `account_closing` | 400 `terms_needed` |
| `PATCH mailboxes/{id}` | Yes | 409 `account_closing` | 400 `terms_needed` |
| `DELETE mailboxes/{id}` (fresh proof) | Yes | Yes | Yes |
| `GET .../messages` without `q` | Yes | Yes | Yes, to find what to download |
| `GET .../messages?q=` | Yes | Yes | 400 `terms_needed` |
| `GET .../messages/{message}`, `.../html`, `.../links/{n}` | Yes | Yes | 400 `terms_needed` |
| `GET .../raw`, `GET .../attachments/{index}` | Yes | Yes | Yes |
| `DELETE .../messages/{message}`, `POST .../messages/delete` | Yes | 409 `account_closing` | 400 `terms_needed` |

An account both closing and not agreed may do what both columns allow; a refusal is `account_closing` when the closing column refuses, otherwise `terms_needed`. A blocked account, or one that is gone, is 401 everywhere. The admin panel's and the operator's routes are not the person's and do not depend on the person's standing.

## The inbox

### What a message returns

- **Text, always.** The text/plain part (the first, decoded); with none, text the enclave takes out of the text/html part with `golang.org/x/net/html`'s tokenizer (already in the module graph through `go-message`): text nodes, a line break for each block element, nothing of `script`, `style` or `head`, entities decoded, and each link's target written after its text in brackets. With neither, nothing (`text_from: none`). At most 256 KiB; `text_truncated` past it. The text view and search use it; `inbox.MessageText(raw []byte, max int) (text, from string, truncated bool)` makes it, at delivery for the search entry and at read for `Get`.
- **And HTML, formatted**, as "Showing a message's HTML" says.
- **Attachments** are listed by index, name, type and size, and downloaded only as files: `GET .../attachments/{index}` answers `Content-Type: application/octet-stream` whatever the message said, `Content-Disposition: attachment` with the name cleaned (`domain.AttachmentFilename`: no path, no control characters, at most 120 characters, `attachment-<n>` when nothing is left; `filename*` in UTF-8 and an ASCII `filename`), `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src 'none'`, `Cross-Origin-Resource-Policy: same-origin` and `Cache-Control: no-store`. The raw message the same way, as `message/rfc822`.
- **Who sent it**: the DKIM results are returned (`auth`), and the app says "Signed by <domain>" or nothing; it never says a sender is who they claim to be.

### Listing and paging

Newest first; `limit` 1 to 100 (50 without one); `after` is the `next` of the page before, a message identifier (32 hex characters, otherwise `invalid_cursor`). A page reads at most 500 message items (`ScanMessagesDown` in steps of 100) and stops at `limit` messages or 500 read. `next` is the identifier of the last item read, set whenever the scan stopped before the partition's end; absent on the last page. So a page may hold fewer than `limit` messages, or none, and still have `next`: the app offers "Older" whenever `next` is there. Expired items are read and left out; they count toward the 500.

### Searching

- **What**: the senders' names and addresses, the subject and the text (its first 16 KiB). The query is split on spaces; each part must appear in one of them, without regard to case. No operators, no phrases.
- **How**: `ScanEntriesDown` over the `txt#` entries from below `after`, a hundred at a time, opening each and matching, until `limit` matches or 2,000 entries read. What counts toward the 2,000: every item read under `txt#`, an expired one and one that does not open under the key included. A match whose message item is gone is left out (it counted). `next` is the message identifier of the last entry read, whether it matched or not, set whenever the scan stopped before the end; `searched_to` is when the last entry read arrived (`SearchEntry.received_at`, or the time in the identifier of one that does not open). A page with no match has `messages: []`, `searched_to` and, unless nothing older is left, `next`: the app says "Searched mail back to <searched_to>" and offers "Search older mail". With no migration (the test mailbox is retired) every message has its `txt#` entry; one whose delivery stopped before writing it is found by the list, not by a search.
- **Cost, at most, per page**: 2,000 entries of about 18 KiB, so about 36 MiB read from DynamoDB in twenty queries, about 4,600 read units (a thousandth of a dollar on demand), about a second, and 2 MiB in memory at a time. At the forty messages a day the forwarding screens expect, ninety days are 3,600 messages: two pages search a whole mailbox.
- **Rejected: a sealed index** (an HMAC per word). It would answer in one read, but it shows whoever reads the table how often each hidden word appears and when, and it has to be kept right as messages are deleted or expire. To be revisited when a mailbox holds more than about 20,000 messages.

### Deleting

One (`DELETE .../messages/{id}`, 204, `not_found` when it is not there) or up to 100 (`POST .../messages/delete`, answering how many were there). Each deletes the raw object, then the item, then its `txt#` and `confirm#` entries. No fresh proof: a session that can read a message can delete it, as in any mail app.

## The test mailbox: retired

Production has the test mailbox `test`, `u-9b393600758a2985dae3@inbox.permaily.com`, read with a token by the owner (docs/operations.md, "Read the test mailbox"), and the address TLS-RPT reports go to (`deploy/tofu/mail.tf`). The verifier only checks that port 25 answers STARTTLS with the attested key: no address is part of it. It is not moved into an account: moving it would keep a key that the operator's token and every release up to v0.11.0 can read, and a release that can still read F1's items could bring it back.

**What the release that builds this does.** It has no code that reads F1's mailbox item, its key or its mail. Its address has no pointer, so it answers `550 5.1.1` like any unknown address. Gone with it: `internal/inbox/inbox.go` and `messages.go` (F1's service), `internal/httpapi/mailbox.go` and its tests, the flags `-mailbox-id`, `-mailbox-address` and `-mailbox-token-sha256` and their lines in `nix/eif.nix`, the keys `mailbox_id`, `mailbox_address` and `mailbox_token_sha256` in `deploy/enclave.json`, the rule for the token's hash in `.gitleaks.toml`, and `TokenAuth` if nothing else uses it. Storage and SMTP are on whenever the image names a table (`dynamodb_table`), and mailboxes whenever accounts are on (`mail_from`).

**`/v1/mailbox` goes without a 410.** What calls it: the owner's `curl` in docs/operations.md, `cmd/permaily-enclave/main_dev_test.go` and `internal/httpapi/mailbox_test.go`; no script, workflow or verifier (searched on 2026-11-12). A path under it answers 404, as any unknown path of the API host.

**TLS-RPT.** `deploy/tofu/mail.tf` reads the address from a variable, `tls_rpt_address`, empty by default, and writes the `_smtp._tls` record only when it is set. The release's pull request removes the record (the variable is empty). After the release, the super admin makes a mailbox in the app ("TLS reports"), and the operator sets `tls_rpt_address` to its address and applies (docs/operations.md). Until then senders find no TLS-RPT record and send no reports; nothing else depends on it.

**F1's items** stay where they are until the release is verified and a rollback to v0.11.0 is no longer wanted. Then the operator deletes (docs/operations.md): in the table, every item with `pk=mailbox#test` (the mailbox, `sk=mailbox`, and its messages, `sk=msg#…`); in the mail bucket, every object under `raw/test/`. A rollback to v0.11.0 before that reads the test mailbox as it was (and receives at its address again); after it, v0.11.0 would make a new, empty test mailbox. Deleting the KMS key of v0.11.0 and of the releases before (docs/operations.md, "Delete the key of an old release") ends their ability to open the root secret.

**What it does not undo.** Mail the test mailbox ever received was readable with the operator's token, and by every release up to v0.11.0. Nothing makes it unreadable in retrospect except deleting it and retiring those releases' KMS keys. Said in docs/threat-model.md.

**What tests, dev mode and staging use instead.** Dev mode makes mailboxes through the API like everyone: `TestMailEndToEnd` (`main_dev_test.go`) signs up an account (the dev outbox holds the code), makes two mailboxes through `/api/v1/mailboxes`, sends mail to both over SMTP with STARTTLS, reads it through the API with the session, disconnects one, and checks the log holds no address, subject, identifier or code. The F1 dev command in internal/AGENTS.md goes. Staging rehearsals sign in to the staging app with an account of the owner's, make a mailbox (two, when a test needs a second readable address), send to it and read it in the app or through the API with the session (docs/operations.md, "Test with staging images").

## What the operator and admins see and do

- **Counts and records, never addresses.** `AccountRecord` gains `mailboxes` (how many the account has), `Stats` gains `mailboxes: {total}` (from the index), and an account's mailboxes are listed as `MailboxRecord`s: identifier, account, state, created, retention, receiving since, paused at, verified or not. No address, label or Gmail account.
- **Finding a mailbox by its address**, for an abuse report that names one: the address is hashed as `RCPT TO` hashes it, the pointer followed and the record must bear it out; a list of none or one. It tells the operator which account owns an address they know already.
- **Pause and resume**, counted on `/status` as `mailbox_paused` and `mailbox_resumed` (added to `attestation.AdminActions` and printed by `permaily-verify`), logged by kind alone, and, through the panel, written in the admin log with the account and the mailbox.
- **Not**: reading, disconnecting, deleting mail, changing a label or a retention, moving a mailbox.
- **`/status` shows no count of mailboxes**: it is public, and one enclave's.
- **`scripts/accounts.sh`** prints the count with each account, and gains `mailboxes <account id>`, `mailbox find <address>`, `mailbox pause <id>` and `mailbox resume <id>`.
- **Who acts, and what it checks.** The mailboxes service exposes the operator's half to accounts alone (`accounts.Mailboxes`): capabilities without an actor, never given to an adapter. Accounts wraps it twice, as it wraps its own (`operator.go`, `admins.go`), and both are `httpapi.MailboxOperator`:
  - `*accounts.Service`, for the operator with the admin token: nothing limits whom it acts on; each change counted (`acted`) and logged by kind.
  - `*accounts.Admin`, for an admin: a change needs a fresh proof (`changing`); the owner of the mailbox is read from its record (`Mailboxes.Record`), then its account, and `guard` applies as for accounts: a mailbox of an admin's account is acted on by the super admin alone, and nobody else acts on the super admin's; each change counted, logged by kind, and written with `logged` (kinds `mailbox_paused`, `mailbox_resumed`, with the account and the mailbox). Looking needs no proof and writes no log, as for accounts.
  `TestTheOperatorsRoutesCannotChangeAnAddressOrAWayIn` names the four methods.

## Notices by mail

To the account's contact address, through `notify` (ADR 0017: after the change is stored, never failing it, five seconds at most), in the blocks of ADR 0025 (`internal/domain/mail`). No address, label, code, link or identifier in any of them; the facts are times.

| Kind | Subject | Blocks |
|---|---|---|
| `mailbox_added` | A mailbox was added to your Permaily account | Lead "A mailbox was added to your Permaily account." Paragraph "Mail forwarded to it is kept in your account, and only you can read it, in the app." Facts: When. Note "If this wasn't you, sign in at app.permaily.com, disconnect it, and write to support@permaily.com." |
| `forwarding_asked` (once a mailbox: the first verified confirmation) | Gmail asked to forward to your Permaily mailbox | Lead "Google confirmed that a Gmail account asked to forward its mail to one of your Permaily mailboxes." Paragraph "Open Accounts in the app to see which Gmail account asked. Enter the code in Gmail only if that account is yours." Facts: When. Note "If you didn't ask, do nothing: no mail is forwarded until the code is entered in Gmail." |
| `mailbox_disconnected` | A mailbox was disconnected from your Permaily account | Lead "A mailbox was disconnected from your Permaily account. Its address no longer accepts mail, and its mail is being deleted." Paragraph "Turn forwarding to it off in Gmail, or Gmail will tell you that forwarding fails." Facts: When. Note: the support line |
| `mailbox_paused` | Permaily paused one of your mailboxes | Lead "Permaily paused one of your mailboxes." Paragraph "It refuses new mail until Permaily resumes it. The mail it holds stays, and you can read, delete or disconnect it." Facts: When. Note "Write to support@permaily.com to ask why." |
| `mailbox_resumed` | Permaily resumed one of your mailboxes | Lead "Permaily resumed one of your mailboxes. It accepts mail again." Facts: When. Note: the support line |

None on an account's removal (its deletion was told already), for a label, a retention or deleted messages.

## Confirming again

Disconnecting a mailbox needs the fresh proof of ADR 0022 (403 `confirmation_needed`): it deletes mail for good. Pause and resume through the admin panel need it, as every admin change. Nothing else: making a mailbox gives nobody anything (mail only comes in), a label and a retention are the person's own and can be changed back, and deleting a message is what any mail app lets a session do. The web app runs disconnecting through `useConfirmed()`.

## The web app

Screens, routes, states and words are in docs/web.md ("Mailboxes and the inbox"). In short: Accounts lists the real mailboxes; connecting makes one and opens its forwarding set-up, which shows the address to copy, the Gmail steps, and Gmail's confirmation code as soon as it arrives (asking `GET mailboxes/{id}` every five seconds while that step shows, for fifteen minutes at most); a mailbox shows "Google confirmed: <Gmail account>" or "Not confirmed by Google"; disconnecting asks, in the exact words of docs/web.md, and confirms it's the person. A new Inbox section lists, searches, opens and deletes the messages of one mailbox at a time, and says that nothing from before `receiving_since` is there. `RequireSession` lets a closing account, and one that has not agreed, reach Accounts and the Inbox in a limited form that offers only what the table above allows. What stays sample: which connections use a mailbox (step 5) and connecting with a Google account (F3). The sample's Gmail filter step goes, since ADR 0018 forwards all mail (decided by the owner).

## The API

The web app's own API: under `/api/v1/` on the app's host, JSON, the session cookie, same origin for every request that changes something (ADR 0017). Errors are `{"error": {"code", "message"}}`. Every route answers 401 `unauthorized` without a session and 404 `not_found` for a mailbox or a message that is not the account's; what each route allows by the account's standing is the table of "Access while an account is closing or has not agreed". Types: `internal/contracts/mailboxes.go`; the same shapes in `web/src/lib/api.ts`. `too_many_mailboxes` and `mailbox_unreadable` are 409, as `account_closing` is.

| Request | Needs | Answers | Errors besides the above |
|---|---|---|---|
| `GET mailboxes` | a session | 200 `MailboxList`: `{mailboxes: [Mailbox], limit}`, the oldest first, without the ones being disconnected | |
| `POST mailboxes` `{label}` | a session | 201 `Mailbox` | 400 `invalid_label`, `invalid_body`; 409 `too_many_mailboxes`; 429 `too_many_requests` (ten in thirty days) |
| `GET mailboxes/{id}` | a session | 200 `Mailbox` | |
| `PATCH mailboxes/{id}` `{label?, retention_days?}` | a session | 200 `Mailbox` | 400 `invalid_label`, `invalid_retention`, `invalid_body`; 409 `mailbox_unreadable`; 429 |
| `DELETE mailboxes/{id}` | a session and a fresh proof | 204 | 403 `confirmation_needed` |
| `GET mailboxes/{id}/messages?limit=&after=&q=` | a session | 200 `MessagePage`; with `q`, a page of what a search found, with `searched_to` | 400 `invalid_limit` (1 to 100), `invalid_cursor`, `invalid_query` (1 to 200 characters); 409 `mailbox_unreadable`; 429 `too_many_requests` |
| `GET mailboxes/{id}/messages/{message}` | a session | 200 `Message`; its HTML and its links have routes of their own ("Showing a message's HTML", "Routes") | |
| `GET mailboxes/{id}/messages/{message}/raw` | a session | 200 `message/rfc822`, as a file `<message>.eml` | 429 |
| `GET mailboxes/{id}/messages/{message}/attachments/{index}` | a session | 200 `application/octet-stream`, as a file with the attachment's cleaned name | 404 for an index that is not one; 429 |
| `DELETE mailboxes/{id}/messages/{message}` | a session | 204 | |
| `POST mailboxes/{id}/messages/delete` `{ids}` | a session | 200 `MessagesDeleted`: `{deleted}`, how many were there | 400 `too_many_messages` (more than 100), `invalid_body` |

A `Mailbox` is `{id, address, label, state: waiting|receiving|paused|unreadable, created_at, retention_days, retention_options, receiving_since?, paused_at?, source?: {provider: gmail, address, verified_at}, confirmations: [{code, requester?, received_at, verified, for_another_address?, message?}]}`; an `unreadable` one has only its identifier and `created_at`. A `MessageSummary` is `{id, received_at, expires_at, size, from: [{name?, address}], subject, snippet, attachments, confirmation}`; a `MessagePage` `{messages, next?, searched_to?}`; a `Message` `{id, received_at, expires_at, size, from, to, cc, reply_to, subject, date, text, text_from: plain|html|none, text_truncated, attachments: [{index, name, type, size}], auth: [{method, domain, result}], confirmation, unparsed, html?}`. Lists are always lists, never null; absent fields are left out. `TestMailboxShapes` pins the JSON.

The admin panel, under `/api/v1/admin/` (ADR 0022: an admin whose account is "passkey only", 404 for anyone else, a fresh proof for changes):

| Request | Answers |
|---|---|
| `GET admin/accounts/{id}/mailboxes` | `MailboxRecordList` |
| `GET admin/mailboxes?address=<address>` | `MailboxRecordList` of none or one |
| `POST admin/mailboxes/{id}/pause`, `POST admin/mailboxes/{id}/resume` | The `MailboxRecord` after it; 403 `confirmation_needed`, `super_admin_only`; 429 past ten changes in thirty days |

`AccountRecord` gains `mailboxes` (a number) and `Stats` gains `mailboxes` (`MailboxCounts`, `{total}`), added by pull request 4.

The operator's tool, with the admin token on the API host: `GET /v1/admin/accounts/{id}/mailboxes`, `GET /v1/admin/mailboxes?address=<address>`, `POST /v1/admin/mailboxes/{id}/pause` and `/resume`, answering as above. `/v1/mailbox` is gone.

## Options considered

- **A list inside the account's record**, or entries: rejected above.
- **Moving the test mailbox into the owner's account**: a release that reads F1's items could bring back a disconnected test mailbox, and a rollback would keep a reader that ignores disconnection and holds the token. Retired instead.
- **A cache of mailbox records for ingest** (a minute): one enclave would accept mail for a minute after the other disconnected or paused the mailbox.
- **Marking a message seen before storing it**: a failure between the two loses mail. The claim, then the mark after, instead.
- **Choosing one of two commits at a version by digest** (ADR 0016 until now): whoever writes to the bucket decides which change counts. Refused instead.
- **Refusing the mail of a blocked account**: Gmail would tell the person forwarding fails, and nothing is gained while nobody can read it.
- **Letting the operator disconnect**: it destroys the person's mail without their word; pausing stops the harm.
- **Deleting the mail inside the request that disconnects**: up to tens of thousands of objects in one request; removing the key from the record stops access at once, and the deletion can wait for the sweep.
- **A KMS key per mailbox**: real destruction of a key, at a key per mailbox and a seven-day wait; not now ("What disconnecting does to the key").
- **Text only, HTML deferred**: rejected by the owner on 2026-11-12 ("Showing a message's HTML").
- **One inbox merging every mailbox**: each page would read every mailbox's partition and merge by time. One mailbox at a time first; a merged view needs no change of format later.

## Implementation pull requests

Six. Pull request 0 first, alone. Then 1, 2, 3 and 4 are built at the same time against the interfaces and fakes committed with this design; 5 once 2 and 3 have merged. Merge order: 0; 1; then 2 and 4 in either order; 3 at any time after 0; 5 last. A release takes all six: the owner decided the inbox shows HTML from the start. Whoever owns a file changes it; the others do not, except the lines a seam names.

**Why the store goes first, on its own.** Pull requests 1 and 2 both call what it adds (`CreateNew`, `ErrForked`, `Scanner`), so it must merge before either can merge; it changes how every anchored record already in production is read (the TLS identity, accounts) and what `/status` reports, which docs/verification.md covers, so it deserves a review of its own by someone who reads only it; and it is small enough to merge in days, before the others are done. It can even ship in a release of its own.

**What truly runs in parallel.** After 0 merges: 1, 2, 3 and 4. Pull request 2 needs from 1 only `inbox.MessageText` for `Get`'s text: it builds everything else against `inboxtest.Seed` and `mailboxestest.Fake`, and writes `Get` once 1 has merged. Pull request 4 needs nothing of 1 but the interface, faked by `mailboxestest.Fake`. Pull request 3 needs only the TypeScript types and played `fetch`. 5 waits for 2 and 3.

### The seams, as committed

| Seam | Where | Implemented by | Used by | Fake |
|---|---|---|---|---|
| `store.Scanner`, `store.EntryScan`, `store.MessageScan`, `store.ErrForked` | `internal/store/scan.go` | `*store.Memory`, `*store.AWS` (0) | 1, 2 | `store.Memory` itself |
| `mailboxes.Records` (`CreateNew` among them) | `internal/services/mailboxes/mailboxes.go` | `*store.Anchored` (0) | 1 | `store.Anchored` over `store.Memory` |
| `mailboxes.Accounts` (`Standing`) | same | `*accounts.Service` (1, in `accounts/standing.go`) | 1 | `mailboxestest.Accounts` |
| `mailboxes.Notifier` | same | `*accounts.Service` (4, in `accounts/notices.go`) | 1 | `mailboxestest.Notices` |
| `mailboxes.Caller`, `Standing`, `Access`, `NoticeKind`, limits, `ErrNoSuchMailbox`, `ErrSlowDown`, `ErrNotAllStored` | same | (types) | all | |
| `httpapi.MailboxesService` | `internal/httpapi/mailboxes.go` | `*mailboxes.Service` (1) | 2 | `mailboxestest.Fake` |
| `inbox.Box`, `inbox.Reader`, the stored format (`StoredMeta`, `SearchEntry`, `ConfirmEntry`, `SeenEntry`, purposes, keys, parse limits) | `internal/inbox/box.go` | `Reader` by 2; the format written by 1 | 1, 2, 5 | `inboxtest.Seed` writes the format |
| `accounts.Mailboxes` | `internal/services/accounts/mailboxes.go` | `*mailboxes.Service` (1) | accounts (1: `RemoveAccount`; 4: the rest) | `mailboxestest.Fake` |
| `httpapi.MailboxOperator` | `internal/httpapi/mailboxes_operator.go` | `*accounts.Service` and `*accounts.Admin` (4) | 4's routes | (4 tests it with `mailboxestest.Fake` under accounts) |
| `smtpd.Recipients` | `internal/smtpd/recipients.go` | `*mailboxes.Service` (1) | `smtpd` (1) | (1 owns both sides) |

**The composition** is `cmd/permaily-enclave` (`main.go`, `platform_enclave.go`, `platform_dev.go`), owned by pull request 1: it makes `*accounts.Service` first, then the `*mailboxes.Service` from the store, the anchored records (an `Anchored` of its own, `OnlyNewest`, reported in `/status` as `mailbox`), the keys and `*accounts.Service` as its `Accounts`; gives the service to accounts (the two refer to each other, so accounts takes it once, before serving, with `SetMailboxes`, which pull request 1 adds next to `Config.Mailboxes`) and to `smtpd`; and asserts at compile time `var _ httpapi.MailboxesService`, `accounts.Mailboxes` and `smtpd.Recipients` of `(*mailboxes.Service)(nil)`. Pull request 2 adds the lines that mount its handler; pull request 4 gives `*accounts.Service` to the mailboxes service as its `Notifier` and asserts `httpapi.MailboxOperator` of `(*accounts.Service)(nil)` and `(*accounts.Admin)(nil)`.

`depguard` holds: the new service imports `contracts`, `domain`, `envelope`, `keys`, `store` and `inbox`, never `net/http`, an adapter or the AWS SDK; `inboxtest` and `mailboxestest` are fakes, allowed only in tests (`no-fakes-in-production` lists them).

### 0. Store

- **Owns**: `internal/store` (`anchor.go`, `s3commits.go`, `memory.go`, `aws.go`, `scan.go` and their tests); `pkg/attestation/status.go` (`AnchorRegression.Fork`) and `cmd/permaily-verify` (printing it); `cmd/permaily-enclave/platform_enclave.go`'s `anchorStatus` (the fork) and `ForkIsLost` on the TLS identity's `Anchored`; docs/verification.md, ADR 0016's and 0024's paragraphs if this pull request's wording needs fixing.
- **Must not touch**: anything of mailboxes, accounts or the web app.
- **Tests**: S1 to S5; the fork against the fake bucket and, when `PERMAILY_TEST_ANCHOR_BUCKET` is set, a real one; `/status` and `permaily-verify` with a fork; the store's coverage stays at least 96 % with the emulators.
- **Done when**: `make check` passes; every existing test of `Anchored` and `S3Commits` passes unchanged but `TestAnchoredOverS3CommitsWithADeleteMarkerInBetween`, which now expects `ErrForked` on both enclaves.

### 1. Server core: records, lifecycle, delivery, the test mailbox retired

- **Owns**: `internal/services/mailboxes/` (the service: records, the list, the address, making, settings, disconnecting, the sweep, pausing, `RemoveAccount`, `Count`, `Total`, `Records`, `ByAddress`, `Record`, `Open`, `Accept`, `Deliver`; `enclaves_test.go`); `internal/domain/mailbox.go` (the address, the label, `AttachmentFilename`); in `internal/inbox`: `deliver.go` (writing the format), `parse.go` (`StoredMeta`, `SearchEntry`, the parse limits, the confirmation and its checks), `text.go` (`MessageText`), and deleting `inbox.go` and `messages.go`; `internal/msgauth` (the signed header fields and `l=`); `internal/smtpd` (`Recipients`, admission); in `internal/services/accounts`: `standing.go`, the `Config.Mailboxes` field, `SetMailboxes` and the call in `removeLocked`; `internal/httpapi/mailbox.go` and its test (deleted); `cmd/permaily-enclave` (the composition above, flags, `main_dev_test.go`); `deploy/enclave.json`, `nix/eif.nix`, `.gitleaks.toml`, `deploy/tofu/mail.tf` and its variable; `scrub`; docs/data.md's rows, docs/operations.md, internal/AGENTS.md; `scripts/coverage-thresholds.txt` (a line for the new package).
- **Must not touch**: `internal/inbox/read.go`, `search.go`; `internal/httpapi/mailboxes.go` beyond the interface; the admin and operator routes; `notices.go`; the web app.
- **Depends on**: 0.
- **Tests**: M1 to M22; the order of making and of a disconnect with a store that fails at each step; every refusal at `RCPT TO` the same; every row of "The answers, all of them"; each limit of "Admission" at its value and one past it (a hundred sessions, messages near 25 MiB at once); the parse limits; the confirmation's checks with the real message from staging (signed, for this mailbox; replayed to another; `l=`; `To` not signed; two addresses in `To`); `TestMailEndToEnd` on the API; the log holds no address, subject, identifier or code.
- **Done when**: `make check`; coverage of the new package at least 95 %; a staging rehearsal: a Gmail account forwarding to a new mailbox through Gmail's set-up, its confirmation verified, mail read, the mailbox disconnected and its mail gone after the sweep.

### 2. Server messages: the inbox, and all of `/api/v1/mailboxes`

- **Owns**: `internal/inbox/read.go` and `search.go` (the `Reader`); `internal/httpapi/mailboxes.go` (every route of the person's table, with the session and same-origin checks taken out of `AccountsAPI` into a helper both use, and `too_many_mailboxes` and `mailbox_unreadable` added to the 409s of `failed`); `go.mod` (`golang.org/x/net` from indirect to direct) and the `vendorHash` in `nix/go.nix`.
- **Must not touch**: `internal/services/`; `deliver.go`, `parse.go`, `text.go`; `cmd/`, but for mounting the handler (`app.Handle("/api/v1/mailboxes", …)` and with a trailing `/`); the web app.
- **Depends on**: 0; 1 for `MessageText` and to merge.
- **Tests**: every route's answers and errors against `mailboxestest.Fake`, including every cell of the access table; reading against `inboxtest.Seed`, and before merging against messages written by 1's `Deliver`; the bounds of a list (500 read, `next` on a short page) and of a search (2,000 read with expired and unopenable entries counted, `next` and `searched_to` on a page with no match); the headers of attachment and raw downloads; cleaned filenames; a message of another account is 404; the read budget answers 429.
- **Done when**: `make check`; `internal/httpapi` stays at 95 % and `internal/inbox` at 86 % or more.

### 3. Web: Accounts wired, the Inbox, and the gate

- **Owns**: `web/src/pages/accounts/*`, `web/src/pages/inbox/*` (new), `web/src/components/SessionGate.tsx` (the limited form for closing and unagreed accounts), `web/src/pages/auth/AccountClosing.tsx` and `Terms.tsx` (a link to the mailboxes), the calls in `web/src/lib/api.ts` (the types are there already), `web/src/App.tsx`, `web/src/sections.ts`, `web/tests/pages/accounts/*`, `inbox/*` and the gate's tests, coverage thresholds for them, docs/web.md.
- **Must not touch**: `web/src/pages/admin/*`, the server.
- **Depends on**: the contract only.
- **Tests**: `fetch` played: the list, connecting, the set-up with the code arriving while it asks, verified, not verified, for another address, disconnecting with `confirmation_needed` and its exact words, an `unreadable` mailbox; the inbox's list, a short page with `next`, a search page with nothing found and `next`, one message, deleting one and many, the empty states; the gate: a closing account reaches Accounts and the Inbox with only what it may do, one that has not agreed reaches the list, downloads and disconnecting and nothing else, and every other route still shows their screens. `expectAccessible()` on each; no address, search or code in storage or the address bar (`expectNotKept`).
- **Done when**: `bun run check` in `web/` and the root `bun run lint` pass; thresholds do not go down.

### 4. Operator, admin panel and notices

- **Owns**: `internal/services/accounts/notices.go` (the five notices, as the `Notifier`), `mailbox_admin.go` (new: the operator's and the admin's wrappers, implementing `httpapi.MailboxOperator`), `operator.go` and `admins.go` additions (the kinds, counts in `AccountRecord` and `Stats`), `internal/httpapi/accounts.go` (the admin and operator routes above), `pkg/attestation/status.go` (the two kinds), `cmd/permaily-verify` (printing them), `scripts/accounts.sh`, `web/src/pages/admin/*`, the `mailboxes` fields of `AccountRecord` and `Stats` in `internal/contracts`.
- **Must not touch**: `internal/services/mailboxes`; the person's screens of the web app.
- **Depends on**: 0 to merge (nothing of it); 1 to compose.
- **Tests**: each notice's blocks, in `sampleMails`, sent once and never failing what caused it; the operator's routes and the admin panel's with `mailboxestest.Fake`: the fresh proof, the guard (an admin on an admin's mailbox, anyone on the super admin's), the count, the log entry, `too_many_requests`; the method list of the operator updated; the counts on `/status`.
- **Done when**: `make check`; `internal/services/accounts` stays at 99 %.

### 5. Showing HTML

As "Its pull request: the fifth", at the end of this record; it calls `Reader.Get` (2) and adds to the frame's route the read budget of "Admission".

## What it rests on, and what is left

- **The root secret stays inside enclaves of public releases.** A saved copy of the table from before a disconnect, together with the root secret, reads that copy's mail ("What disconnecting does to the key").
- **Google's word for the Gmail account**: Google confirms that a Gmail account asked to forward to this mailbox, and nothing more. The checks rest on Google signing `To` and the whole body; a real message captured in staging must show it (pull request 1).
- **Whether an address exists** is learnt only by guessing 100 bits.
- **A deleted message put back** in the table and the bucket shows again in the inbox, until the audit log of ADR 0019.
- **Search scans**: a mailbox of more than about 20,000 messages takes several pages to search through.
- **Decided by the owner on 2026-11-12**: messages are shown as HTML from the start; the inbox shows one mailbox at a time; five mailboxes an account; a blocked account's mailboxes keep receiving; the operator may pause and resume a mailbox and never disconnect or read it; the Gmail filter step is dropped from the screens. The owner's choice that the test mailbox goes to `hello@permaily.com` is met by the super admin making an ordinary mailbox after the release, which TLS-RPT then names.
- **Decided by the orchestrator of the review on 2026-11-12, the owner told**: no migration of the test mailbox; disconnecting promises immediate refusal of new access, and running requests end within their deadline; what a closing account and one that has not agreed may do.
- **To design while building**: the exact words of the screens and notices beyond those written here, with the designer.

## Showing a message's HTML

Decided by the owner on 2026-11-12: the inbox shows messages formatted from the start. Mail is hostile input from anyone on the internet, and it is shown inside the app that holds the person's session, their passkeys' settings and, from step 5, the consent screen for agents; the product also promises that nobody learns that or when a person read a message. This section says how the HTML is shown so that it cannot hurt, which mechanism guarantees each property, and which are only a second line.

### What holds, and why

| Property | First line (guarantees it) | Second line (holds if the first fails) |
|---|---|---|
| Nothing in the message runs: no script, event handler, `javascript:` URL, plugin, form that submits, `<meta>` refresh, `<base>`, nested frame, SVG or MathML trick | The browser: the iframe's `sandbox` attribute without `allow-scripts`, `allow-forms`, `allow-same-origin` or `allow-top-navigation`, and the same `sandbox` again in the response's `Content-Security-Policy` with `default-src 'none'` (no script, object, frame or worker source), `form-action 'none'`, `base-uri 'none'`. Without `allow-scripts` the sandbox also sets the "sandboxed automatic features" flag, which stops a `<meta>` refresh from navigating by itself (HTML, "shared declarative refresh steps") | The sanitiser: an allowlist that keeps no `script`, `style` outside its CSS filter, event attribute, `meta`, `base`, `form` control, frame, `object`, `embed`, `svg` or `math`, and no URL the message wrote in any attribute of the output |
| Nothing remote is fetched because a message was opened | The response's CSP: `default-src 'none'; img-src data:; style-src 'unsafe-inline'` and no other source. That blocks every image, font (`@font-face`), stylesheet (`<link>`, `@import`), `url()` in any property, media, poster, frame and object; prefetch hints are allowed only to the sources a policy lists (CSP 3), and it lists none | The sanitiser removes every remote reference (and counts it for the person); it never writes an attacker's URL into the output, so no `href` or `src` in the frame names a remote host, and nothing is there to DNS-prefetch or preconnect to. `X-DNS-Prefetch-Control: off` as a third. `<link rel=dns-prefetch>` and `preconnect` are not reliably under CSP in every browser: for those the sanitiser, which drops every `<link>`, is the only line besides that header |
| The message cannot reach the app: its DOM, cookies, storage, or the API with the person's session | The sandbox without `allow-same-origin` gives the frame's document an opaque origin, so to the browser it is another site: it cannot touch the parent's DOM, `document.cookie` or storage. With no script, it cannot call anything at all | `HttpOnly` and `SameSite=Strict` on the session and device cookies; a request from the frame's opaque origin is cross-site, so it carries neither; changes need the app's own origin (ADR 0017) |
| The message cannot navigate or cover the app's window | The sandbox without `allow-top-navigation*`: the frame cannot navigate the top window. A frame draws only inside its box, so `position: fixed` and full-page overlays are fixed to the frame's own viewport | The sanitiser drops `position` other than `static` and `relative`, `z-index`, and anything that could draw over the frame's own text |
| The frame does not navigate itself to a remote page | The sanitiser: every kept link opens a page of the app in a new tab (`target="_blank"`), never the frame; `<meta>` and `<form>` are gone. A sandboxed frame may still navigate itself (HTML: the sandboxed navigation flag only stops it navigating others), so this property rests on the sanitiser | The sandbox stays on whatever the frame navigates to: a page that got there would run no script either. The automatic-features flag stops a meta refresh; `form-action 'none'` and the missing `allow-forms` stop a form |
| Nobody learns where the person was or that they opened it when they follow a link | `rel="noopener noreferrer"` on the link, `Referrer-Policy: no-referrer` on the frame and on the app, and a link page of the app between the message and the site | `<meta name="referrer" content="no-referrer">` written by the sanitiser in the frame's head |
| The frame cannot be loaded anywhere else | `frame-ancestors https://app.permaily.com` (the app's origin written out, not `'self'`, so nothing depends on how a browser treats `'self'` in a sandboxed response); `Sec-Fetch-Dest: iframe` and `Sec-Fetch-Site: same-origin` required when the browser sends them (every current browser does) | `Cross-Origin-Resource-Policy: same-origin`, `X-Content-Type-Options: nosniff`; and if opened in a tab anyway, the CSP `sandbox` applies to a top-level document too, so it runs nothing there either |

The two lines are independent: the frame and its CSP hold whatever HTML reaches them, and the sanitiser's output would run nothing and fetch nothing even in a page without them. The frame is first for running and fetching because the browser enforces it after its own parse of the document, where a sanitiser can only predict that parse; the sanitiser is first for the frame navigating itself and for links, which no sandbox flag or CSP directive controls (CSP's `navigate-to` was dropped from the specification).

### The frame: a URL with its own headers, not `srcdoc`

The viewer is an `<iframe>` whose `src` is `/api/v1/mailboxes/{id}/messages/{message}/html`, with `sandbox="allow-popups allow-popups-to-escape-sandbox"`, `referrerpolicy="no-referrer"`, `title` and nothing else.

- **Why not `srcdoc`.** A `srcdoc` document takes a copy of its parent's policy container, so the app's own CSP (`default-src 'self'`) would apply inside it: inline styles and `data:` images would be refused, and the mail shown unstyled and without its images, unless the app's CSP were loosened for the whole app (`style-src 'unsafe-inline'; img-src data:`). A `<meta>` CSP inside the document can only add restrictions. `srcdoc` would also put the hostile HTML into a string in the app's own JavaScript, one mistaken `innerHTML` away from the app's DOM. A URL gives the frame a CSP of its own, the narrowest that shows mail, leaves the app's untouched, and keeps the HTML out of the app's JavaScript entirely.
- **The app's CSP.** Today `default-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'` (`internal/router`). `frame-src` falls back to `child-src` and then `default-src`, so a same-origin frame is allowed already; the fifth pull request writes `frame-src 'self'` out, so a later change to `default-src` cannot widen or break it unnoticed, and a test pins it. Nothing else of the app's CSP changes.
- **The frame's response headers**, on every answer of the route, errors included: `Content-Type: text/html; charset=utf-8`; `Content-Security-Policy: sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors https://app.permaily.com` (the app's host from the image's configuration; dev mode's own); `X-Content-Type-Options: nosniff`; `Cross-Origin-Resource-Policy: same-origin`; `Referrer-Policy: no-referrer`; `Cache-Control: no-store`; `X-DNS-Prefetch-Control: off`. They replace the router's for this route (the router's `frame-ancestors 'none'` would forbid the frame).
- **How the session authorises it.** As every route: the `__Host-` session cookie. The frame's request is made by the app's own document for a frame whose every ancestor is the app, so it is same-site and carries `SameSite=Strict` cookies; the sandbox applies to the document that comes back, not to the request that fetches it. The route opens the mailbox like `GET message` (`Open` with `AccessReadMessage`: the owner, the record, the account's standing), and takes the raw message's size from the bytes of mail open for reading ("Admission"). A request from inside the frame is cross-site (opaque origin) and carries no session: the frame cannot ask the API for anything.
- **Opened directly in a tab** (a person copies the frame's address): the browser sends `Sec-Fetch-Dest: document` and the route answers 404 with the frame's headers. A browser that sends no `Sec-Fetch-*` gets the document, and the CSP `sandbox` applies at the top level as it does in a frame: an opaque origin, no script, links that open the link page. MDN's warning that sandboxing is useless if the content can be opened outside the frame is about the `sandbox` attribute alone; the header covers it.
- **Back, forward and the cache.** The viewer makes a new `<iframe>` element for each message (React `key` on the message identifier) and never changes the `src` of one: the first navigation of a new frame replaces its initial `about:blank` and adds no history entry, so Back leaves the message instead of stepping through frames. `Cache-Control: no-store` keeps the document out of the HTTP cache. A page restored from the back-forward cache (after signing out, say) is the app's question as a whole; the message view listens for `pageshow` with `persisted` and asks the server again.
- **Chrome, Firefox and Safari** all support the `sandbox` attribute and its `allow-popups*` tokens, the CSP `sandbox` directive (Baseline since 2016), `frame-ancestors`, `Sec-Fetch-Dest` (Baseline since March 2023, Safari 16.4) and `Referrer-Policy`. What differs: DNS prefetching (Chromium honours `X-DNS-Prefetch-Control`, the others mostly do not prefetch from HTTPS pages at all), and how each treats `<link rel=preconnect>` under CSP; both are covered by the sanitiser writing no remote host. Rejected: the `credentialless` attribute (Chromium only, and not needed: the frame needs no credentials of its own).
- **A separate origin** for message content (a host such as `content.permaily.com`, as MDN recommends and large webmail does) would add a third line that does not depend on the sandbox. Not now: it needs a new name in the enclave's certificate and its CAA and verification steps (docs/verification.md), and since the `__Host-` cookie is not sent to another host, a signed, short-lived ticket in the frame's URL. The sandbox attribute and the CSP `sandbox` are two independent mechanisms of every current browser; a published bypass of both, or a need for scripts in the frame, reopens this.

### The sanitiser

A new package, `internal/mailhtml`, pure (no storage, no network), on `golang.org/x/net/html`, which is already in the module graph (`go.mod` moves it from indirect to direct with pull request 2; `golang.org/x/net/html/charset` and `golang.org/x/net/idna` come from the same module). No third-party sanitiser: `github.com/microcosm-cc/bluemonday` (BSD-3-Clause) would add itself, `github.com/aymerick/douceur` (MIT) and `github.com/gorilla/css` (BSD-3-Clause) to the image and to third-party-notices.md, and it would still leave to us the parts that matter here: images from `cid:` parts, links rewritten to numbered link pages, counting what was removed, the limits, CSS by value rather than by regular expression, and the check that the output parses back to itself. Its CSS support filters `style` attributes by a regular expression or a function per property and does not make `<style>` blocks safe, which newsletters need; ours tokenises both.

**Input.** `internal/inbox/htmlpart.go` (pull request 5) walks the raw message with `go-message` and takes the text/html part a mail client would show: the last text/html of a `multipart/alternative`, else the first inline text/html; and the parts of its `multipart/related` by `Content-ID`. The transfer encoding is undone by `go-message`, the charset by its `charset` package from the MIME header; with no charset there, `html/charset.DetermineEncoding` (a byte order mark, then a `<meta>` in the first 1,024 bytes, then UTF-8). A charset that cannot be decoded makes the HTML `unreadable`. The result is UTF-8; invalid sequences become U+FFFD. The output always says UTF-8 (header and its own `<meta charset>` first in `<head>`), so a wrong label in the message changes nothing in the browser.

**Parsing, once.** `html.ParseWithOptions(r, html.ParseOptionEnableScripting(false))`: the HTML standard's tree construction, with scripting off as it is in the frame, so `<noscript>` parses the same in both. Never regular expressions on markup. The sanitiser walks that tree and builds a new one from nothing, copying only what the allowlist names; it never moves a node of the input into the output. It renders the new tree with `html.Render`, parses that output again with the same options, renders it again, and requires the two renderings to be byte for byte the same; when they differ, the HTML is `unreadable` and the person sees the text. The package's own documentation warns that a tree built by a program can change meaning when it is rendered and parsed again: this check is what turns that warning into a refusal, and it is what stops mutation XSS (a document the browser parses differently from the sanitiser).

**Elements.**

- Kept, with the attributes below: `a abbr address article aside b bdi bdo big blockquote br caption center cite code col colgroup dd del details dfn div dl dt em figcaption figure font footer h1 h2 h3 h4 h5 h6 header hr i img ins kbd li main mark nav ol p pre q rp rt ruby s samp section small span strike strong sub summary sup table tbody td tfoot th thead time tr tt u ul var wbr`; `style` only through the CSS filter, moved into the head.
- Dropped with everything inside: `script noscript template style` (outside the filter) `title head` (the message's; ours is written) `iframe frame frameset object embed applet param svg math canvas audio video source track picture map area link meta base input select option optgroup textarea button datalist output progress meter dialog portal slot xmp plaintext noembed noframes listing`, comments, processing instructions, `<!DOCTYPE>`, and any element in the SVG or MathML namespace.
- Unwrapped (the element goes, its children stay): `html`, `body`, `form`, `label`, `fieldset`, `legend`, `marquee`, `blink`, and every element not named in either list (`o:p`, Outlook's `v:` elements, custom tags).

**Attributes** (all others go, every `on*`, `style` except through the filter, `srcset`, `sizes`, `loading`, `poster`, `background`, `dynsrc`, `lowsrc`, `longdesc`, `usemap`, `ping`, `formaction`, `xmlns`, `cite`, `name`):

- On any kept element: `dir` (`ltr`, `rtl`, `auto`), `lang` (a language tag, letters, digits and `-`, at most 35), `title`, `align`, `valign`, `class` and `id` (each a list of `[A-Za-z0-9_-]` tokens, for the message's own CSS), `style` (filtered).
- `table`, `tr`, `td`, `th`: `width`, `height`, `border`, `cellpadding`, `cellspacing`, `bgcolor`, `colspan`, `rowspan`, `nowrap`, `scope`, `headers`; `col`, `colgroup`: `span`, `width`; `ol`: `start`, `type`, `reversed`; `li`: `value`; `font`: `color`, `face`, `size`; `time`: `datetime`; `img`: `src` (below), `alt`, `width`, `height`, `border`, `hspace`, `vspace`; `a`: `href` (below), and the `target`, `rel` and `title` the sanitiser writes. Numbers are numbers or percentages, colours a `#` and hex digits or a name of letters.
- The message's `<body>` gives its `bgcolor` and `text` to ours, and its `<html>` its `lang` and `dir`.

**URLs.** Parsed with `net/url` after the WHATWG trimming (leading and trailing C0 controls and spaces removed, tabs and newlines inside removed), and only these:

- **`a href`, `http` and `https` with a host**: the link is numbered in document order (at most 1,000; past that, the rest are text) and written as `href="/inbox/<mailbox>/<message>/links/<n>"`, `target="_blank"`, `rel="noopener noreferrer"`, and `title` set to the real address, so hovering shows where it leads. The address the message wrote is never in the `href`. It is kept, re-serialised by `net/url`, for the link route, which answers it only when parsing it again gives the same scheme and host.
- **`mailto:` and `tel:`**: the link goes and its text stays; when the text does not contain the address, the address is written after it in brackets, as text. Permaily neither writes mail nor calls, and a link that fills in another app's form is a trick of its own.
- **Anything else** (`javascript:`, `vbscript:`, `data:`, `file:`, `blob:`, `cid:`, `intent:`, any other scheme, relative and protocol-relative addresses, fragments such as `#top`): the link goes and its text stays. A fragment would navigate the frame and add a history entry.
- **`img src`**: `cid:<id>` is replaced by a `data:` URI built by the sanitiser from that part of `multipart/related`; `data:image/png|jpeg|gif|webp;base64,` is decoded and rebuilt the same way. Either is shown only when its bytes start as that type does (PNG `\x89PNG\r\n\x1a\n`, JPEG `FF D8 FF`, GIF `GIF87a`/`GIF89a`, WebP `RIFF....WEBP`), whatever type the message declared, and within the limits below; it is written with the type found. SVG is never shown as an image, inline or from a part (an SVG image runs no script in an `<img>`, but it is a document format with a long history of parser bugs): it is counted in `inline_images_omitted` and stays in the original download. `http`, `https` and protocol-relative sources are removed and counted in `remote_images`; anything else is removed.

**Remote images: what the person sees, and why there is no "Load images".** A remote image is replaced by a small bordered box with its `alt` text (or nothing, for an image whose `width` or `height` is 0, 1 or 2 or that is hidden, which is how open-tracking pixels are made: it is counted, not boxed). Above the frame the app says "3 remote images were not loaded". There is no button to load them, now or as a setting: loading them in the browser tells the sender that and when the message was opened, from which address and with which browser; loading them through the enclave would still tell that and when, from the enclave's address, and would make the enclave fetch any address on the internet, which its egress allowlist forbids (docs/threat-model.md). Fetching them at delivery would tell the sender the address is alive and when mail arrives. Any of these needs an ADR of its own.

**CSS.** Real mail needs it: tables laid out by inline `style` on almost every cell, colours, fonts, and in newsletters a `<style>` block with `@media` rules for phones. Dropping it all would make most of the mail people get unreadable; keeping it and relying on the CSP alone would keep `position: fixed` overlays and the CSP as a single line for every `url()`. So it is kept through an allowlist, and the CSP stays first for fetching:

- A tokeniser written in the package from CSS Syntax Level 3 (about 400 lines; fuzzed), so escapes such as `\75 rl(` are read as the browser reads them. The output is written by the sanitiser from the tokens it kept, never the input's text; `<` in it is written as `\3c `, so a `<style>` cannot be closed from inside.
- **Properties kept**: `color`, `background-color`, `background` (only colours and keywords), `font`, `font-family`, `font-size`, `font-style`, `font-weight`, `font-variant`, `line-height`, `letter-spacing`, `word-spacing`, `text-align`, `text-decoration` and its `-line`, `-color` and `-style`, `text-indent`, `text-transform`, `text-overflow`, `white-space`, `word-break`, `word-wrap`, `overflow-wrap`, `vertical-align`, `direction`, `margin` and `padding` and their sides, `border` and its sides, widths, styles, colours and radii, `border-collapse`, `border-spacing`, `width`, `height` and their `min-` and `max-`, `display`, `float`, `clear`, `table-layout`, `caption-side`, `empty-cells`, `list-style-type`, `list-style-position`, `overflow`, `overflow-x`, `overflow-y`, `visibility`, `box-sizing`, `opacity`, `position` (`static` and `relative` only), `top`, `right`, `bottom`, `left`. Everything else goes, among them `content`, `cursor`, `z-index`, custom properties and `var()`, `behavior`, `-moz-binding`, `expression`, every `*-image`, `mask*`, `filter`, `animation*`, `transition*`, `color-scheme` and `mso-*`.
- **Values**: identifiers, numbers, percentages, dimensions in `px em rem % pt pc cm mm in ex ch vw vh`, `#` colours, strings (in `font-family` only), commas, `/` and `!important`; functions `rgb() rgba() hsl() hsla()` only. A declaration with any other token (a `url()`, any other function, `@`, `\` left after unescaping) goes; a `url()` of `http`, `https` or `//` is counted in `remote_images` (in image properties) or `remote_other`.
- **Rules of a `<style>`**: style rules whose selectors are made of identifiers, `#` and `.` names, `*`, combinators, attribute selectors and the pseudo-classes `:hover :first-child :last-child :nth-child() :nth-of-type() :not() :link :visited`; `@media` with a prelude of media types, features and `and`, `not`, `only`, commas, its rules filtered the same way. Every other at-rule goes, `@import` and `@font-face` among them (counted in `remote_other` when they name an address), with `@charset`, `@namespace`, `@keyframes`, `@supports`, `@page`, `@layer`, `@container` and `@property`.
- **What the sanitiser adds first**, before the message's own CSS: `:root { color-scheme: light; background: #fff; color: #111 } body { margin: 16px; font: 16px/1.5 system-ui, sans-serif; overflow-wrap: anywhere } img { max-width: 100%; height: auto } table { max-width: 100% }` and the style of the remote image's box. System fonts only: the frame can load none.

### Limits

| Limit | Value | Over it |
|---|---|---|
| The HTML part, decoded | 2 MiB | `too_large` |
| Elements in it | 50,000 | `too_large` |
| How deep they nest | 256 | `too_large` |
| CSS of the `<style>` blocks together; of one `style` attribute | 256 KiB; 8 KiB | That CSS goes; the rest is shown |
| Links numbered | 1,000 | The rest are text |
| Inline images shown | 50; 2 MiB each, decoded; 5 MiB together | Those images are not shown (a box, counted in `inline_images_omitted`); the rest is |
| The document sent to the frame | about 9 MiB at most (2 MiB and the images in base64) | Cannot be passed by the limits above |

When the HTML is `too_large` or `unreadable`, the app shows the text with a line that says why, and "Download original" for all of it. Each limit has a test at its value and one past it.

### Text stays available

The message view has two views, "Formatted" and "Text" (two buttons with `aria-pressed`), Formatted first when `html.status` is `shown`. The choice is kept in the app's memory for the session, never in storage. A message with no HTML part shows text and no switch. Search works on the text, as before (`txt#` holds text, never HTML). The text view is the one to use where the frame cannot help: a screen reader that handles frames badly, a dark page, a message whose formatting hides things.

### Dark theme, height, keyboard, zoom

- **Colours.** Mail is written for a white page, so the frame is always a white sheet (`color-scheme: light` and a white background in the frame's document), with a one-pixel border in `--line` and the radius of cards, in both themes. Nothing is inverted: inverting colours breaks logos, images and mail that has its own dark design. The text view follows the app's theme.
- **Height without scripts.** A frame with no script in it cannot report its height, and its opaque origin hides its document from the app, rightly. The message view is a column filling the viewport: the compact head (sender, subject, date, "Details" for recipients and signatures, the switch, the notes) and below it the frame, which takes the rest of the height (`flex: 1`, at least 24 rem) and scrolls inside itself. Nothing the message says sets a size the app uses.
- **Keyboard and screen readers.** The frame's `title` is "Message: <subject>" ("Message: No subject"). The page's focus goes to the subject, the `h1`, as on every page (`RequireSession`); Tab goes through the head's controls, then into the frame's links in order, then out. Images keep their `alt`; the box of a remote image says "Remote image not loaded" and its `alt`. The frame's document gets the message's `lang` and `dir` when they are valid.
- **Zoom.** The browser's zoom and text size apply inside the frame. Animated GIFs cannot be stopped without a script in the frame: they follow the browser; CSS animations are dropped.

### Routes

| Request | Needs | Answers | Errors |
|---|---|---|---|
| `GET mailboxes/{id}/messages/{message}/html` | a session; `Sec-Fetch-Dest: iframe` and `Sec-Fetch-Site: same-origin` when sent | 200 `text/html; charset=utf-8`, the cleaned document, with the frame's headers | 404 when the message is not the account's, has no HTML that is `shown`, or the request is not a frame's; 401, 429 and 500 as every route. Every answer, errors included, has the frame's headers and a short fixed HTML body ("This message can't be shown here."), never JSON, since it is shown in the frame |
| `GET mailboxes/{id}/messages/{message}/links/{n}` | a session | 200 `MessageLink`: `{url, host, shown?}`: the address, its host in ASCII (`idna`, so a look-alike host shows as `xn--…`), and the host the link's text showed when it named another one | 404 for an `n` that is not a link of the message |

`Message.html` and both routes sanitise on read: every request parses the raw message again (milliseconds for 2 MiB), and the same image gives the same output on every enclave. Nothing is stored or cached: no sanitised HTML, no image, no link. Nothing changes for an operator or a second enclave; there is no new record, entry or key.

### The link page

`/inbox/:mailbox/:message/links/:n`, inside `RequireSession`. The frame's link opens it in a new tab (`allow-popups` lets the frame open it; `allow-popups-to-escape-sandbox` lets it run as the app, not under the frame's sandbox). That tab was opened from an opaque origin, so its first request carries no cookie, which the page does not need; its calls to the API are made by the app's own top-level document and carry the session. It asks `GET .../links/{n}` and shows: "This link leads to" and the host, large; the whole address in monospace, breaking anywhere; when `shown` is set, "The message showed <shown>, but this link leads to <host>."; then "Open <host>", a plain link with `rel="noopener noreferrer"` to the address the server answered, and "Back to the message". The app thus leaves for another site in a second place, still only for an address the server answered, never for one read from the address bar (web/AGENTS.md changes with it).

What the two sandbox flags open, besides that: a link with `target="_blank"` opens a new tab. Nothing else: `window.open` needs a script, and forms are not allowed. `allow-popups-to-escape-sandbox` matters only if a link somehow named a remote site, which the sanitiser never writes; such a link would open like a link in any mail client, without an opener and without a referrer.

### Tests the implementers write

- **The corpus**, `internal/mailhtml/testdata/hostile/`, one message per file with the case named, run through the sanitiser in Go. Each output must contain no element, attribute, URL scheme, CSS property, function or at-rule outside the allowlist (checked by parsing the output, not by searching its text), must parse back to itself, and must be within the limits. The cases: `<script>` in every form (upper case, inside SVG, inside a table, after a broken comment, `type=module`, `speculationrules`); every `on*` attribute, on kept and dropped elements; `javascript:`, `JaVaScRiPt:`, `java&#x09;script:`, `\x00javascript:`, `vbscript:`, `data:text/html` and `data:image/svg+xml` in `href`, `src`, `action`, `formaction`, `background`, `poster`, `xlink:href`; CSS `url()` (quoted, unquoted, escaped as `\75 rl(`, in `background`, `list-style`, `border-image`, `cursor`, `content`), `@import`, `@font-face`, `expression()`, `behavior`, `-moz-binding`, `position: fixed` overlays, `</style>` inside CSS; remote `img`, `srcset`, `picture`/`source`, `video poster`, `input type=image`, `link rel=stylesheet|preload|prefetch|dns-prefetch|preconnect|icon`, `meta http-equiv=refresh|set-cookie|content-security-policy`, `base href` and `base target`; forms and every control; `iframe`, `frame`, `object`, `embed`, `applet`, `portal`, `srcdoc` inside them; SVG with `<script>`, `<foreignObject>`, `<use href>`, `<animate>`, `<set>`; MathML `<maction>`, `<annotation-xml encoding=text/html>`; the mutation-XSS classics (`<noscript><p title="</noscript><img src=x onerror=...>">`, `<svg><style><img onerror>`, `<math><mtext><table><mglyph><style><img onerror>`, `<form><math><mtext></form><form><mglyph><style></math><img onerror>`, `<!--><img onerror>-->`, `<xmp>`, `<textarea>`, `<title>`, `<template>`, `<![CDATA[` in foreign content, unclosed attributes and backticks); oversized input at each limit and one past it; deep nesting; 50,001 elements; a wrong and an unknown `charset`, UTF-7, a BOM that disagrees with the header, invalid UTF-8, a `<meta charset>` that disagrees; `cid:` to a missing part, to a part that is not an image, to SVG, to an image whose bytes are not its declared type, to one over the size; links whose text is another address (`https://bank.example` leading elsewhere), an IDN look-alike host, a `mailto:` whose text is another address, 1,001 links. Plus a fuzz target (`FuzzSanitise`) that holds the same properties on any input, run by `make fuzz`, and golden outputs of real messages (a receipt, a newsletter with `@media`, a Gmail confirmation) so a change of output is seen in review.
- **In a real browser**: a Playwright suite under `web/e2e/` (the site's set-up, `site/playwright.config.ts`, is the model; the web app has none yet) against a Go test server (`internal/mailhtml/frametest`, built for tests only) that serves, over TLS at an app host name, a page with the app's own CSP and `__Host-` cookies and the viewer's exact `<iframe>`, and the real route's handler on the corpus. A second host stands for the internet and records every request it gets. For every hostile message, the suite asserts that the remote host received nothing (images, fonts, CSS, prefetch, DNS through a resolver it controls), that no script ran (a marker the hostile scripts would set in the frame or the parent stays unset, and no dialog opened), that the parent's DOM, `document.cookie` and storage are unchanged, that the frame's URL is the one it was given after load and after a wait, and that a click on a link opens one new tab on the link page, without an opener. It also asserts the frame's headers and that opening the route in a tab answers 404. Run in Chromium in CI, required. Firefox and WebKit run the same suite in CI too; Playwright's WebKit is not Safari (Safari's own privacy features and Apple's network stack are not covered), and neither is a mobile browser or a browser extension, which can read any page. Before the release: the corpus by hand in Safari on macOS and iOS (docs/accessibility.md's manual pass gains the frame).
- **Web unit tests** (Vitest, `fetch` played): the switch and its default; each note (remote images, images omitted, `too_large`, `unreadable`); the frame's attributes exactly (`sandbox`, `referrerpolicy`, `title`, a new element per message); the link page with and without `shown`, and that its "Open" link is the server's address; `expectAccessible()` on both.
- **Server tests**: the route's headers on 200 and on every error; 404 without `Sec-Fetch-Dest: iframe` when `Sec-Fetch-*` is sent; `Message.html` counts equal to what the document holds; the link route's answers; the app's CSP pinned with `frame-src 'self'`.

### Its pull request: the fifth

Recommended: a pull request of its own, built once 2 and 3 have merged, so the inbox works as text first and the riskiest part gets a review of its own, by someone who reads only it; a release takes all six.

- **Owns**: `internal/mailhtml/` (sanitiser, CSS tokeniser, corpus, fuzz target, golden outputs, `frametest`); `internal/inbox/htmlpart.go` (the HTML part, its charset and its `cid:` parts) and the one call in `read.go` that fills `Message.html`; `internal/httpapi/mailboxes_html.go` (the two routes, registered in `mailboxes.go`'s mux by two lines); `internal/router` (`frame-src 'self'`); `web/src/pages/inbox/MessageFrame.tsx`, the switch and the notes in the message view, `web/src/pages/inbox/Link.tsx` and its route in `App.tsx`; `web/e2e/` and its CI job; `web/AGENTS.md` (the second place the app leaves for another site), docs/web.md, docs/threat-model.md ("Mail shown in the app": built), docs/testing.md, docs/accessibility.md.
- **Must not touch**: what pull requests 0 to 4 own beyond the lines named.
- **The seam with pull request 2**: `Reader.Get` leaves `Message.html` absent until this one fills it; the web app shows the text and no switch when it is absent, so 3 works unchanged before 5 and needs no flag. With 5: `mailhtml.Render(in mailhtml.Input) (mailhtml.Output, error)`, where `Input` is `{HTML []byte (UTF-8), Inline map[string]InlinePart, Mailbox, Message string}` and `Output` is `{Document []byte, Report contracts.MessageHTML, Links []string}`; `Reader.Get` uses the report, the HTML route the document, the link route `Links[n]`.
- **Rejected: building it inside 2 and 3.** The two would each carry half of the riskiest change of step 2, reviewed among everything else; and while it was unfinished, the inbox could not be tried at all.

### What this rests on (looked up on 2026-11-12)

- HTML, the `iframe` element and its `sandbox` attribute, and the sandboxing flags: the sandboxed navigation flag stops a frame navigating others but not itself; the sandboxed origin flag gives an opaque origin; the sandboxed automatic features flag, set without `allow-scripts`, keeps a `<meta>` refresh from navigating by itself (https://html.spec.whatwg.org/multipage/browsers.html#sandboxing, https://html.spec.whatwg.org/multipage/semantics.html#shared-declarative-refresh-steps).
- HTML, policy containers: an `about:srcdoc` document takes a clone of its parent's (https://html.spec.whatwg.org/multipage/browsers.html#policy-containers).
- CSP Level 3: `frame-src` falls back to `child-src` and then `default-src`; `frame-ancestors` does not fall back; resource hints are allowed only to the union of a policy's sources; the `sandbox` directive is not honoured in `<meta>` (https://w3c.github.io/webappsec-csp/, https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/sandbox).
- MDN on `<iframe>`: `allow-popups`, `allow-popups-to-escape-sandbox`, the warning about content opened outside its frame, `credentialless` (https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe).
- MDN on `Sec-Fetch-Dest`: `iframe` for a frame, `document` for a top-level navigation, Baseline since March 2023 (https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Sec-Fetch-Dest).
- `golang.org/x/net/html`: "Security Considerations", `ParseOptionEnableScripting`, and that a rendered tree is not always parsed back to the same tree (https://pkg.go.dev/golang.org/x/net/html).
- Requests from a sandboxed frame's opaque origin are cross-site and carry no `SameSite=Strict` or `Lax` cookies (https://github.com/sbingler/schemeful-same-site, https://github.com/w3ctag/design-reviews/issues/1004).
- `bluemonday`'s module file: `github.com/aymerick/douceur`, `github.com/gorilla/css`, `golang.org/x/net` (https://github.com/microcosm-cc/bluemonday/blob/main/go.mod).
- Not settled by reading, to be shown by the browser tests: that each browser sends the `SameSite=Strict` session cookie on the frame's own request (the request is made by the app's document; the specifications say it is same-site), and how each treats `<link rel=preconnect>` under `default-src 'none'`.
