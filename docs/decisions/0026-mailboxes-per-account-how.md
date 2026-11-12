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

### What pull requests 1 to 4 keep true for the frame

The frame of the fifth pull request ("Showing a message's HTML") rests on things the origin already does, which the first four must not undo (they hold today: `internal/router/router.go`'s `setSecurityHeaders`, `internal/httpapi/httpapi.go`'s `writeJSON`, F1's `internal/httpapi/mailbox.go`'s downloads). Each of 1 to 4 carries a test that keeps them for the routes it adds:

- `X-Content-Type-Options: nosniff` on every response of the origin, errors included.
- Errors are fixed text, or JSON whose message is fixed; nothing of a message (a subject, a name, a filename, an address) in an error body.
- The API answers JSON only, with `Content-Type: application/json`.
- Attachments and the raw message only as downloads: `application/octet-stream` or `message/rfc822`, `Content-Disposition: attachment`, the sandbox CSP, `nosniff`.
- The owner is checked before anything of a message is read or parsed: another account's mailbox or message is `not_found` before the bucket is asked.
- No route answers attacker-controlled HTML as a document, other than the frame's route of the fifth.
- No GET changes state, and a later GET that must (as the provider callback does, behind its own state check) carries its own check: a page can trigger a GET blindly even though it cannot read the answer.

The tests: 1, `TestMailEndToEnd` checks `nosniff` and a JSON error with a fixed message on every response it gets, with a hostile subject and filename in its mail; 2, `TestMailboxRoutesNeverAnswerADocument` tries every route of the person's table, answered and refused, against `inboxtest.Seed` with a hostile subject, sender, filename and HTML part, and asserts the type, the disposition, `nosniff`, a fixed error and that `mailboxestest.Fake` and the reader saw no call for another account's mailbox; 3, the inbox's tests render a subject, a sender, a filename and a text of `<img src=x onerror=alert(1)>` and find them as text, never as elements, and the app has no `dangerouslySetInnerHTML` (a check in `web/tests`); 4, the admin and operator routes' tests assert JSON, `nosniff` and no message content in any answer.

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

Decided by the owner on 2026-11-12: the inbox shows messages formatted from the start. Mail is hostile input from anyone on the internet, and it is shown inside the app that holds the person's session, their passkeys' settings and, from step 5, the consent screen for agents. This section says what showing it guarantees and what it does not, which mechanism holds each guarantee and which is only a second line, and every limit with its number. It was revised after an adversarial review on 2026-11-12 (no script-execution escape and no cross-account disclosure found; eight findings carried in below).

### What it guarantees, and what it does not

**A message can never**: run anything (script, event handler, `javascript:` URL, plugin, form, `<meta>` refresh, nested frame); read or change the app's DOM, cookies, storage, clipboard or address; call the API with the person's session; navigate the app's window; fetch anything from anywhere because it was opened or read (images, fonts, stylesheets, prefetch hints); send the person anywhere without the link page between, where the real destination is shown and nothing is contacted until the person presses "Open"; put a link's box over other content or make a link invisible; change the trusted chrome around the frame.

**A message can still**: draw a picture of anything inside its frame, a fake Permaily screen or a fake sign-in included, as any mail client's content can; make its text say one address while its link leads to another (the link page shows the real one, and says so when they differ); be long, ugly or hard to read (the "Text" view is always there); show an image the sender attached, with whatever metadata (EXIF) is inside it, which stays inside the `data:` URI in the frame and goes nowhere.

**Following a link** is the person's act. It goes to the address the link page shows, and that site learns that the link was followed and when, from the person's browser and network address; a link can be unique to a reader (`https://sender.example/read/recipient-7c91`), so the site may know who followed it, whatever the referrer policy. No referrer and no opener go with it.

**Opening or reading a message** makes no request that the sender or anyone outside Permaily can see.

**Outside it**: what browser services the person turns on see what the browser shows them (translation in Safari and Chrome sends page text to Apple or Google; reader mode; extensions, which can read any page). The frame carries `translate="no"` and `<meta name="google" content="notranslate">` as a courtesy, not a guarantee. A browser bug that breaks both the sandbox attribute and the CSP `sandbox` (a separate origin for message content is the third line, below, and when it is reopened).

### What holds each guarantee

| Property | First line (guarantees it) | Second line (holds if the first fails) |
|---|---|---|
| Nothing in the message runs | The browser: the iframe's `sandbox` attribute without `allow-scripts`, `allow-forms`, `allow-same-origin` or `allow-top-navigation*`, and the same `sandbox` again in the response's `Content-Security-Policy` with `default-src 'none'`, `form-action 'none'`, `base-uri 'none'`. Without `allow-scripts` the sandbox also sets the sandboxed automatic features flag, which keeps a `<meta>` refresh from navigating by itself | The sanitiser: an allowlist that keeps no `script`, event attribute, `meta`, `base`, form control, frame, `object`, `embed`, `svg` or `math`, and no URL the message wrote in any attribute of the output |
| Nothing remote is fetched because a message was opened | The response's CSP: `default-src 'none'; img-src data:; style-src 'unsafe-inline'`, no other source: every image, font, stylesheet, `url()`, media and frame is blocked, and resource hints are allowed only to sources a policy lists (CSP 3), which is none | The sanitiser writes no address of the message into any attribute or CSS, drops every `<link>`, and counts what it removed. `X-DNS-Prefetch-Control: off` as a third. `<link rel=dns-prefetch>` and `preconnect` are not reliably under CSP in every browser: for those the sanitiser and that header are the only lines |
| The message cannot reach the app: its DOM, cookies, storage, or the API with the session | The sandbox without `allow-same-origin` gives the frame's document an opaque origin: to the browser it is another site, which cannot touch the parent's DOM, `document.cookie` or storage, and has no script to try | Cookies: the session cookie is `HttpOnly` and `SameSite=Strict`, so no request from the frame's opaque origin carries it; the device and provider cookies are `HttpOnly` and `SameSite=Lax` (`internal/httpapi/accounts.go`), sent on a top-level navigation such as the link page's tab, and neither authorises reading mail. Changes need the app's own origin (ADR 0017) |
| The message cannot navigate or cover the app's window | The sandbox without `allow-top-navigation*`. A frame draws only inside its box | The sanitiser keeps nothing that positions a box |
| The frame does not navigate itself to a remote page | The sanitiser: every kept link opens the app's link page in a new tab (`target="_blank"`), never the frame; `<meta>`, `<form>` and fragments are gone. No sandbox flag or CSP directive stops a frame navigating itself (CSP's `navigate-to` was dropped) | The sandbox stays on whatever the frame navigates to |
| A link cannot hide, move over other content, or pass for something else than a link | The sanitiser: no CSS that positions, layers, hides or makes transparent; each link's own `style` written by the sanitiser with `!important` ("CSS"), which no rule of a stylesheet can beat | The frame's stylesheet, after the sender's ("CSS") |
| Nothing is contacted before the person presses "Open" | The link page: the destination is text, never a link; the only way to it is a `<button>` whose handler navigates | The frame's link names only the app's own link page, so a link preview (touch-and-hold in Safari on iPhone) loads Permaily's page, not the destination |
| No referrer and no opener when a link is followed | `rel="noopener noreferrer"` on the frame's link; `window.open(url, '_blank', 'noopener,noreferrer')` on the link page; `Referrer-Policy: no-referrer` on the app and the frame | `<meta name="referrer" content="no-referrer">` in the frame's head |
| Other sites cannot frame or load it as a document | `frame-ancestors https://app.permaily.com` (written out, not `'self'`). When the browser sends Fetch Metadata, the route answers only `Sec-Fetch-Dest: iframe` together with `Sec-Fetch-Site: same-origin` | The CSP `sandbox` applies to a top-level document too: opened in a tab by a browser that sends no Fetch Metadata (Safari before 16.4, a proxy that strips them), the document gets an opaque origin and runs nothing. That is what holds then; "it cannot be loaded elsewhere" is not claimed |

The two lines are independent, and each is tested without the other ("Tests"): the frame and its CSP hold whatever HTML reaches them, and the sanitiser's output runs nothing, fetches nothing and keeps links in their boxes in a page with neither. The frame is first for running and fetching because the browser enforces it after its own parse of the document, where a sanitiser can only predict that parse; the sanitiser is first for navigation and for how links look, which no sandbox flag or CSP directive controls.

`allow-popups` has a second effect: it lifts the sandboxed custom protocols navigation flag (HTML, "parse a sandboxing directive"), so a link to `zoommtg:` or `tel:` would be followed. What holds that is the sanitiser, which keeps `http` and `https` links only and writes them as links to the app.

### The frame

The viewer is an `<iframe>` whose `src` is `/api/v1/mailboxes/{id}/messages/{message}/html`, with `sandbox="allow-popups allow-popups-to-escape-sandbox"`, `referrerpolicy="no-referrer"`, a `title` for assistive technology ("Message: <subject>", "Message: No subject") and nothing else. A new `<iframe>` element per message (React `key` on the message identifier), its `src` never changed: the first navigation of a new frame replaces its initial `about:blank` and adds no history entry.

- **Both popup flags are needed.** `allow-popups` lets the frame's link open a tab. `allow-popups-to-escape-sandbox` unsets the "sandbox propagates to auxiliary browsing contexts" flag (HTML); without it the link page's tab would inherit the frame's sandbox, run no script, and the link page, an app page, would not work.
- **Why a URL and not `srcdoc`.** A `srcdoc` document takes a copy of its parent's policy container, so the app's own CSP would apply inside it and mail would show without its styles and images, unless the whole app's CSP were loosened. `srcdoc` would also put the hostile HTML into a string in the app's JavaScript. A URL gives the frame a CSP of its own and keeps the HTML out of the app's JavaScript entirely.
- **The app's CSP.** `default-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'` today (`internal/router`). The fifth pull request writes `frame-src 'self'` out, so a later change to `default-src` cannot widen or break it unnoticed, and a test pins it.
- **The frame's response headers**, on every answer of the route, errors included, replacing the router's: `Content-Type: text/html; charset=utf-8`; `Content-Security-Policy: sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors https://app.permaily.com` (the app's host from the image's configuration; dev mode's own); `X-Content-Type-Options: nosniff`; `Cross-Origin-Resource-Policy: same-origin`; `Referrer-Policy: no-referrer`; `Cache-Control: no-store`; `X-DNS-Prefetch-Control: off`; `Strict-Transport-Security` as the router's.
- **The document** is written by the sanitiser from nothing: `<!DOCTYPE html><html lang dir translate="no" style="…">` (the message's `lang` and `dir` when valid), `<head>` with `<meta charset="utf-8">` first, `<meta name="referrer" content="no-referrer">`, `<meta name="google" content="notranslate">`, the sender's CSS, then the frame's stylesheet; then `<body>` with the message's content.
- **How the session authorises it.** The frame's request is made by the app's own document for a frame whose every ancestor is the app, so by the specifications it is same-site and carries the `SameSite=Strict` session cookie; the sandbox applies to the document that comes back, not to the request that fetches it. Whether each browser sends it is release evidence ("The release gate"); when it does not, the route answers 401 with the frame's headers and the fixed body, and the viewer says "This message can't be shown formatted here" with the text view. The route opens the mailbox like `GET message` (`Open` with `AccessReadMessage`: the owner, the record, the account's standing) before reading anything, and takes the raw message's size from the bytes of mail open for reading ("Admission").
- **A request without a frame's Fetch Metadata.** When the request carries any `Sec-Fetch-*` header, the route needs `Sec-Fetch-Dest: iframe` and `Sec-Fetch-Site: same-origin`, and answers 404 otherwise: opening the frame's URL in a tab, from history or from session restore gets "This message can't be shown here." A request with none is served, and the CSP `sandbox` holds as above. Fetch Metadata is Baseline since March 2023 (Safari 16.4).
- **Back, forward and the back/forward cache.** `Cache-Control: no-store` keeps the frame's document out of the HTTP cache. It does not keep a page out of the back/forward cache everywhere (web.dev: browsers have historically declined, and Chrome is changing it), and the app's own page is `no-cache`, so a restored app page brings its frame back. The message view listens for `pageshow`; when `persisted` is true it removes the frame and the text at once, shows "Checking…", asks `GET me` and `GET message` again, and shows the message again only when both answer; otherwise what the session gate shows, or "That isn't there any more."
- **A separate origin** for message content (a host such as `content.permaily.com`) would be a third line, independent of the sandbox. Not now: it needs a new name in the enclave's certificate, its CAA and verification steps (docs/verification.md), and a signed, short-lived ticket in the frame's URL since the `__Host-` cookie is not sent to another host. Reopened by a published bypass of both the sandbox attribute and the CSP `sandbox`, by a need for scripts in the frame, or by a release-gate check that fails in a browser people use.

### The trusted chrome around the frame

The frame never fills the message view. Around it, drawn by the app and never by the message, always visible while a message is open in the formatted view (the frame scrolls inside itself, so the chrome does not scroll away):

- **The label**, attached to the frame's top edge, in the app's own type and colours: "Email content. Permaily never asks you to sign in or confirm anything inside a message." It is visible text; the frame's `title` is not this label.
- **The sender as the enclave parsed it**, in the head above: each `from` entry's name and address from `Message.from` ("From Ann <ann@example.org>"), and "Signed by <domain>" for each DKIM pass, as the head already has. Nothing the message's HTML says changes either.
- **The frame's border**: one pixel in `--line` with the radius of cards, in both themes, so where the message ends is always drawn by the app.

The message can draw anything inside the border, including a copy of this label; it cannot draw outside it.

### The sanitiser

A new package, `internal/mailhtml`, pure (no storage, no network), on `golang.org/x/net/html`, already in the module graph (`go.mod` moves it from indirect to direct with pull request 2; `golang.org/x/net/html/charset` and `golang.org/x/net/idna` come from the same module, v0.62.0 today). No third-party sanitiser: `github.com/microcosm-cc/bluemonday` would add itself, `github.com/aymerick/douceur` and `github.com/gorilla/css` to the image and to third-party-notices.md, and would still leave to us `cid:` images, links rewritten to the link page, counting what was removed, the budgets, and CSS by value.

**Input.** `internal/inbox/htmlpart.go` (pull request 5) walks the raw message with `go-message` and takes the text/html part a mail client would show: the last text/html of a `multipart/alternative`, else the first inline text/html; and the parts of its `multipart/related` by `Content-ID`. `go-message` undoes the transfer encoding. The charset is decided in this order, written in the package rather than taken from `charset.DetermineEncoding`, which looks at the first 1,024 bytes only, puts a byte order mark before the header, and falls back to Windows-1252 whenever those 1,024 bytes are not UTF-8 with a byte above 0x7F (read in x/net v0.62.0's source):

1. the MIME header's `charset`, when it is a label of the WHATWG Encoding Standard (`charset.Lookup`);
2. a byte order mark (UTF-8, UTF-16LE, UTF-16BE), which is then removed;
3. a `<meta charset>` or `<meta http-equiv=content-type>` within the first 1,024 bytes (the HTML standard's prescan);
4. UTF-8, when the whole part is valid UTF-8;
5. Windows-1252.

The HTML standard puts the byte order mark first; mail puts the MIME header first, as every mail client does, and since the frame receives only the sanitiser's own UTF-8 the order changes how text looks, never what runs. A label that names the WHATWG "replacement" encoding, or no WHATWG encoding (UTF-7 among them), makes the HTML `unreadable`. Invalid sequences become U+FFFD. The size limit applies before and after transcoding (a Windows-1252 byte can become three).

**Budgets, enforced while the work happens.** Every render (for `Message.html`, for the frame, for a link) runs under one `context.Context` with the deadline below, after taking a render slot ("Renders at once") and before the raw message is read from the bucket, and stops at the first limit it meets:

1. **Before any tree.** A pass of `html.NewTokenizer` over the decoded part counts start tags, attributes per element and in all, attribute bytes, and an upper bound of the tree the parser will build: each start tag and text token adds one, plus the formatting elements (`a b big code em font i nobr s small strike strong tt u`) opened and not yet closed, which the parser's "reconstruct the active formatting elements" step can clone at that token. Past any limit the HTML is `too_large` and no tree is built. This is what stops input whose tree is much larger than its tokens (the amplification case of "Tests").
2. **The parse.** `html.ParseWithOptions(r, html.ParseOptionEnableScripting(false))`, the HTML standard's tree construction with scripting off as it is in the frame, so `<noscript>` parses the same in both. `r` returns at most 4 KiB a read and fails once the deadline has passed, so the parse stops within one read of it. The parser itself refuses a stack of open elements deeper than 512 (`insertOpenElement`), which the sanitiser reports as `too_large`.
3. **The walk.** The sanitiser builds a new tree from nothing, copying only what the allowlist names; it never moves a node of the input into the output. It counts output elements and depth as it goes and checks the context every 1,024 nodes.
4. **The output.** Written by `html.Render` into a writer that counts bytes and fails at the cap; `Message.html` renders into a counting writer that keeps nothing, so its status is the frame's. Then the output is parsed again with the same options and rendered again, and the two renderings must be byte for byte the same, else the HTML is `unreadable`. That check catches a tree that x/net/html would itself parse differently; it does not show what a browser builds, and it is not what stops mutation XSS. The sandbox and the CSP are first for running anything; the browser tests inspect the DOM each browser actually builds from the output ("Tests").

**Elements.**

- Kept, with the attributes below: `a abbr address article aside b bdi bdo big blockquote br caption center cite code col colgroup dd del details dfn div dl dt em figcaption figure font footer h1 h2 h3 h4 h5 h6 header hr i img ins kbd li main mark nav ol p pre q rp rt ruby s samp section small span strike strong sub summary sup table tbody td tfoot th thead time tr tt u ul var wbr`; `style` only through the CSS filter, moved into the head.
- Dropped with everything inside: `script noscript template style` (outside the filter) `title head` (the message's; ours is written) `iframe frame frameset object embed applet param svg math canvas audio video source track picture map area link meta base input select option optgroup textarea button datalist output progress meter dialog portal slot xmp plaintext noembed noframes listing`, comments, processing instructions, `<!DOCTYPE>`, and any element in the SVG or MathML namespace.
- Unwrapped (the element goes, its children stay): `html`, `body`, `form`, `label`, `fieldset`, `legend`, `marquee`, `blink`, and every element not named in either list (`o:p`, Outlook's `v:` elements, custom tags).

**Attributes** (all others go, every `on*`, `srcset`, `sizes`, `loading`, `poster`, `background`, `dynsrc`, `lowsrc`, `longdesc`, `usemap`, `ping`, `formaction`, `xmlns`, `cite`, `name`, `target`, `rel` among them):

- On any kept element: `dir` (`ltr`, `rtl`, `auto`), `lang` (letters, digits and `-`, at most 35), `title`, `align`, `valign`, `class` and `id` (each a list of `[A-Za-z0-9_-]` tokens, for the message's own CSS), `style` (filtered).
- `table`, `tr`, `td`, `th`: `width`, `height`, `border`, `cellpadding`, `cellspacing`, `bgcolor`, `colspan`, `rowspan`, `nowrap`, `scope`, `headers`; `col`, `colgroup`: `span`, `width`; `ol`: `start`, `type`, `reversed`; `li`: `value`; `font`: `color`, `face`, `size`; `time`: `datetime`; `img`: `src` (below), `alt`, `width`, `height`, `border`, `hspace`, `vspace`; `a`: `href` (below), and the `target`, `rel`, `title` and `style` the sanitiser writes. Numbers are non-negative numbers or percentages, colours a `#` and hex digits or a name of letters.
- The message's `<body>` gives its `bgcolor` and `text` to ours, and its `<html>` its `lang` and `dir`.

**Links.** Each `a href` is read as a URL after the WHATWG trimming (leading and trailing C0 controls and spaces removed, tabs and newlines inside removed) and kept only when it matches this grammar exactly, checked by the package's own parser before `net/url` sees it:

- `scheme "://" host [":" port] [path] ["?" query] ["#" fragment]`, at most 4,096 bytes;
- `scheme` is `http` or `https`, in any case, written in lower case;
- no userinfo: no `@` before the path; no `\` anywhere (a browser reads it as `/` in these schemes, Go does not); no `%` in the host (a browser percent-decodes a host, Go refuses it);
- `host` is a DNS name: the host as written goes through `idna.Lookup.ToASCII` (x/net/idna's `Lookup` profile: UTS 46 non-transitional mapping, STD3 rules, hyphen and joiner checks, the Bidi rule), stricter than the WHATWG's "domain to ASCII", which uses the same mapping with `beStrict` false; the result, lower case, must be two to 127 labels of `[a-z0-9-]`, each 1 to 63 characters, not starting or ending with `-`, 253 characters at most, no trailing dot, and its last label must be letters only (`[a-z]{2,63}`) or `xn--` and letters, digits and hyphens. That last rule is what refuses every IP address in every spelling: the WHATWG URL parser treats a host whose last label is all digits, or `0x` and hex digits, as IPv4 (its "ends in a number checker"), so `2130706433`, `0x7f000001`, `0177.0.0.1` and `127.1` all mean `127.0.0.1` to a browser while Go keeps their spelling (`net/url` follows RFC 3986); an IPv6 literal (`[…]`) has no DNS name at all;
- `port`, when present, is 1 to 65535 without a leading zero, and is dropped when it is the scheme's default (80, 443);
- path, query and fragment are written again with every byte outside the URL code points of the WHATWG standard, and every non-ASCII character, percent-encoded as UTF-8, so the stored URL is ASCII and a browser parses it to itself.

A link that does not match becomes its text. A `mailto:` or `tel:` link becomes its text too, with the address written after it in brackets when the text does not contain it: Permaily neither writes mail nor calls, and the sandbox's `allow-popups` would follow other protocols ("What holds each guarantee"). Fragments (`#top`) become text, since they would navigate the frame.

A kept link is numbered in document order of the output (at most 1,000; past that the rest are text) and written as `href="/inbox/<mailbox>/<message>/links/<n>.<tag>"`, `target="_blank"`, `rel="noopener noreferrer"`, `title` set to the canonical URL (so hovering shows it; a `title` is not navigable), and the forced link style below. The address the message wrote is never in an `href`.

**The link's token.** `<tag>` is HMAC-SHA-256, under a key derived with HKDF-SHA-256 from the mailbox's data key with the info `permaily link v1`, over the message identifier, the number `n` and the canonical URL, truncated to 12 bytes and written in unpadded base64url (16 characters). The link route recomputes the walk, finds link `n`, recomputes the tag and compares in constant time: a tag that does not match, or an `n` past the links there now, answers 409 `link_changed`, and the link page says "This link changed since the message was opened. Close this tab, open the message again, then the link." So a link opened from a page rendered by one release never leads to another destination under a later sanitiser. Nothing is stored. Why an HMAC and not a plain truncated SHA-256 of the URL: the link page's address stays in the browser's history and whatever syncs it; a plain hash there would let anyone holding it confirm a guessed destination, which the mailbox's key prevents. The key is already unwrapped for the request that reads the message, and the derivation (purpose `link`) is added to the purposes of `internal/inbox/box.go`.

**Images.**

- `img src` of `cid:<id>` is replaced by a `data:` URI the sanitiser builds from that part of `multipart/related`; `data:image/png|jpeg|gif|webp;base64,` is decoded and rebuilt the same way. Nothing is decoded into pixels and nothing is re-encoded inside the enclave in this version: decoding hostile images there would move a decoder's risk into the enclave, and re-encoding needs a decoder. Metadata such as EXIF stays inside the data URI, which only the frame shows and nothing fetches.
- **Structure, read before anything else**, by a walker in the package that reads headers and chunk or block lengths and no pixel data: PNG (signature, `IHDR` first, then every chunk to `IEND`; an `acTL`, `fcTL` or `fdAT` chunk is APNG), JPEG (markers to the first `SOFn`), GIF (header, logical screen, then every block; more than one image descriptor, or a frame outside the logical screen, is animated or refused), WebP (RIFF chunks: `VP8 ` or `VP8L` alone, or `VP8X` with its animation flag clear and no `ANIM` or `ANMF`). `image.DecodeConfig` alone is not enough: for PNG it reads the 33 bytes up to `IHDR` and reports an 8192 x 8192 image from them, and it says nothing of animation; the standard library has no WebP decoder, and no new dependency is added for one.
- **Shown** only when the bytes are PNG, JPEG, GIF or WebP whatever type the message declared (written with the type found), static, and within the limits. Otherwise a box with its `alt` text, counted in `inline_images_omitted` and in `inline_images_omitted_by` as `too_large`, `animated` or `unsupported` (SVG, any other type, a `cid:` naming no part, a structure that does not read). SVG is never shown as an image.
- `http`, `https` and protocol-relative sources are removed and counted in `remote_images`; anything else is removed.

**Remote images: what the person sees, and why there is no "Load images".** A remote image is replaced by a small bordered box with its `alt` text (or nothing, for an image whose `width` or `height` is 0, 1 or 2 or that is hidden, which is how open-tracking pixels are made: counted, not boxed). Above the frame the app says "3 remote images were not loaded". There is no button to load them, now or as a setting: loading them in the browser tells the sender that and when the message was opened, from which address and with which browser; loading them through the enclave would still tell that and when, and would make the enclave fetch any address, which its egress allowlist forbids (docs/threat-model.md). Any of these needs an ADR of its own.

### CSS

Real mail needs it: tables laid out by inline `style`, colours, fonts, and in newsletters a `<style>` block with `@media` rules for phones. It is kept through an allowlist, and the CSP stays first for fetching.

- **Parsing.** A tokeniser written in the package from CSS Syntax Level 3 (about 400 lines; fuzzed), so escapes such as `\75 rl(` are read as the browser reads them. The output is written from the tokens kept, never the input's text; `<` in it is written as `\3c `, so a `<style>` cannot be closed from inside.
- **The principle**, for any property not listed here: a declaration is kept only when it changes how a box looks or flows in normal order; never where a box is drawn relative to the boxes around it, whether it is drawn, whether it takes the pointer, or what is drawn over what. Nothing may make a link's box invisible or put it over other content.
- **Properties kept**: `color`, `background-color`, `background` (colours and keywords only), `font`, `font-family`, `font-size` (bounded), `font-style`, `font-weight`, `font-variant`, `line-height` (bounded), `letter-spacing`, `word-spacing`, `text-align`, `text-decoration` and its `-line`, `-color` and `-style`, `text-indent` (bounded), `text-transform`, `text-overflow`, `white-space`, `word-break`, `word-wrap`, `overflow-wrap`, `vertical-align`, `direction`, `margin` and `padding` and their sides, `border` and its sides, widths, styles, colours and radii, `border-collapse`, `border-spacing`, `width`, `min-width`, `max-width`, `min-height`, `height` and `max-height` (a length only in a `style` attribute of `td`, `th`, `tr`, `table` and `img`, where a height is a minimum or the box has no children; `auto` anywhere), `display` (`none`, `block`, `inline`, `inline-block`, `list-item` and the `table*` values; not `flex`, `grid` or `contents`), `float` (`left`, `right`, `none`), `clear`, `table-layout`, `caption-side`, `empty-cells`, `list-style-type`, `list-style-position`, `overflow`, `overflow-x`, `overflow-y`, `box-sizing`.
- **Removed**, besides everything not listed: `position` (every value), `top`, `right`, `bottom`, `left`, `inset*`, `transform`, `translate`, `rotate`, `scale`, `z-index`, `opacity`, `visibility`, `clip`, `clip-path`, `mask*`, `pointer-events`, `mix-blend-mode`, `filter`, `backdrop-filter`, `content`, `cursor`, `outline*`, `box-shadow`, `text-shadow`, `order`, `grid*`, `flex*`, custom properties and `var()`, `behavior`, `-moz-binding`, `expression`, every `*-image`, `animation*`, `transition*`, `color-scheme` and `mso-*`.
- **`float` stays.** Mail built for every client lays out with tables, since Outlook on Windows ignores most of CSS, but real newsletters and receipts float an image beside its text or a logo to one side, and two-column blocks fall back to floats where tables are not used. Without `position`, negative values and fixed heights, a float cannot put a box over another's content: the line boxes beside a float are shortened around it (CSS 2.1, 9.5), so its text and links move aside instead of being covered. Dropping it would make that mail read in one column with images above their text, which is readable; keeping it costs nothing of the principle.
- **Values**: identifiers, numbers, percentages, dimensions in `px em rem % pt pc cm mm in ex ch vw vh`, `#` colours, strings (in `font-family` only), commas and `/`; functions `rgb() rgba() hsl() hsla()` only. No negative number anywhere (that removes negative margins and the `text-indent: -9999px` trick); `font-size` from 6 to 96 px, 5 to 72 pt, 0.5 to 6 em or rem, 50 to 600 % or a keyword; `line-height` `normal`, or 1 to 3 unitless, em or 100 to 300 %, or 10 to 150 px; `text-indent` at most 160 px or 10 em. A declaration outside these, or with any other token (a `url()`, another function, `@`, `\` left after unescaping), goes; a `url()` of `http`, `https` or `//` is counted in `remote_images` (in image properties) or `remote_other`.
- **`!important`** is removed from `style` attributes and kept in `<style>` rules, where newsletters need it for their `@media` rules to beat inline styles.
- **Rules of a `<style>`**: style rules whose selectors are made of type, `#` and `.` names, `*`, combinators, attribute selectors and the pseudo-classes `:first-child :last-child :nth-child() :nth-of-type() :not()` (of simple selectors) `:link :visited :hover`; `@media` with a prelude of media types, features and `and`, `not`, `only`, commas, its rules filtered the same way. Every other at-rule goes, `@import` and `@font-face` among them (counted in `remote_other` when they name an address), with `@charset`, `@namespace`, `@keyframes`, `@supports`, `@page`, `@layer`, `@container` and `@property`.
- **What each link carries.** The sanitiser removes from the link's own declarations every property below and writes them after, with `!important`, in the link's `style` attribute: `display: inline-block; max-width: 100%; overflow: hidden; color: #1a4fd6; background-color: #fff; text-decoration: underline; font-size: max(1em, 12px); line-height: normal; letter-spacing: normal; text-indent: 0`. A declaration attached to the element with `!important` wins over every rule of a stylesheet, `!important` ones included (CSS Cascading and Inheritance Level 5, "Cascade Sorting Order", element-attached styles), so nothing the sender wrote changes how a link looks: underlined, the renderer's blue on white, in its own box that holds what it draws and fits its container. Bulletproof buttons (a styled `<a>` in a coloured cell) show as an underlined link on white in that cell: readable and honest.
- **The frame's stylesheet, after the sender's**: `html { color-scheme: light; background: #fff; color: #111 } body { margin: 16px; font: 16px/1.5 system-ui, sans-serif; overflow-wrap: anywhere } img { max-width: 100% !important; height: auto } table { max-width: 100% !important } a img { border: 0 }` and the box of an image not shown. The same canvas properties are also written in our `<html>` element's `style` with `!important`, so the sheet is white whatever the sender's rules say; the body's own colour stays the sender's (`bgcolor`, `text`, CSS). System fonts only: the frame can load none.

### Limits

Each limit has a test at its value and one past it. The numbers marked "starting value" are not measured yet: the fifth pull request runs the adversarial benchmarks of "Tests" on the pinned `golang.org/x/net/html`, in an enclave of the production size (one vCPU, 2,048 MiB), and writes the measured numbers back into this table before it merges.

| Limit | Value | Over it | Why this number |
|---|---|---|---|
| The HTML part, before and after transcoding | 2 MiB | `too_large` | Larger than any newsletter seen (they are 20 to 200 KiB); bounds every pass below |
| Start tags (tokeniser pass) | 50,000 | `too_large` | A long newsletter has 2,000 to 10,000 |
| Attributes on one element; in all | 64; 200,000 | `too_large` | Real elements carry under ten; bounds the attribute work |
| One attribute's value | 8 KiB, but `img src` up to the image limit in base64 | That attribute goes | A `style` or `href` is never longer in real mail |
| Upper bound of the tree (tokeniser pass) | 200,000 nodes | `too_large` | Four times the start tags allowed: room for implied elements and real misnesting, not for amplification |
| Output elements; depth | 50,000; 256 | `too_large` | Below the parser's own cap of 512 open elements |
| Output markup, without images, counted as written | 4 MiB | `too_large` | Twice the input: room for escaping in real mail; 2 MiB of `&` (10 MiB of `&amp;`) is refused |
| The document sent to the frame | 4 MiB of markup and at most 6.7 MiB of images in base64: under 11 MiB | Cannot be passed by the limits above | Pinned by `TestMessageHTMLLimits` |
| CSS of the `<style>` blocks together; of one `style` attribute | 256 KiB; 8 KiB | That CSS goes; the rest is shown | Newsletters' blocks are 5 to 60 KiB |
| CSS rules; declarations in all; one selector | 5,000; 20,000; 1 KiB and 32 compound selectors | That CSS goes | Bounds what the browser matches as well as our work |
| Links numbered; one link's URL | 1,000; 4,096 bytes | The rest are text | Tracking URLs reach 2 KiB; a newsletter has under 200 links |
| Inline images: count; bytes each, decoded; bytes together | 50; 2 MiB; 5 MiB | That image is not shown (`too_large`) | Real mail embeds logos and a few photos |
| One image's width or height | 8,192 px | Not shown (`too_large`) | Above any image mail needs (600 to 1,200 px wide, twice for high-density screens) |
| One image's pixels | 16,777,216 (16 MP, 64 MiB decoded at four bytes a pixel) | Not shown (`too_large`) | An 8192 x 8192 image is 256 MiB decoded; a 260 KiB PNG can declare it |
| Pixels of a message's images together | 33,554,432 (32 MP, 128 MiB decoded) | Images past it not shown (`too_large`) | A long newsletter's images at high density are under 25 MP; 128 MiB is what a phone's browser tab can hold for one message beside the app. Starting value: the release gate opens the worst case on an iPhone |
| Animation | none in this version | Not shown (`animated`) | Cannot be stopped without a script in the frame |
| Renders at once, per enclave | 2 (starting value) | Wait in the queue | One vCPU per enclave; two keeps one person's render from blocking another's |
| Renders waiting, per enclave; how long one waits | 16; 5 seconds | 503 `busy` (`Message.html`: `unavailable`) | A queue that cannot grow without bound |
| Renders per account, running and waiting | 1 running, 2 waiting | 429 `too_many_requests` | So one person cannot hold every slot |
| Time for one render | 2 seconds (starting value) | Stopped; `unavailable` | Real mail renders in milliseconds (to be measured); the deadline is what stops the work, not a hope |
| Memory for one render | 64 MiB at peak (starting value) | (by the limits above) | The benchmarks measure it; two at once fit beside the 128 MiB of mail open for reading of "Admission" |

When the HTML is `too_large` or `unreadable`, the app shows the text with a line that says why, and "Download original". When it is `unavailable`, the text with "The formatted view isn't available right now. Try again in a moment." and "Try again".

`Message.html` and both routes render on read: every request parses the raw message again, and the same image gives the same output on every enclave. Nothing is stored or cached: no sanitised HTML, no image, no link. `GET message` renders into a counting writer to know the status and the counts; the frame's route renders the document; the link route runs the token pass, the parse and the walk with images, CSS and output skipped (`mailhtml.Links`), sharing the walk's code with `Render`, so its numbering is the frame's by construction. Each takes a render slot. Nothing changes for an operator or a second enclave; there is no new record, entry or key.

### Text, honestly labelled

The message view has two views, "Formatted" and "Text" (two buttons with `aria-pressed`), Formatted first when `html.status` is `shown`. The choice is kept in the app's memory for the session, never in storage. A message with no HTML part shows text and no switch. The sender controls the text part independently of the HTML, so the Text view says which it shows:

- A message with both (`text_from: plain` and `html`): "This is the plain-text version the sender included. It may differ from the formatted one."
- A message with only HTML (`text_from: html`): "This is the text of the formatted message, without its formatting."
- A message with only text: no note.

Search reads the first 16 KiB of the same text the Text view shows (`txt#` holds text, never HTML), so in a message with both, a word that is only in the HTML is not found. The Text view is the one to use where the frame cannot help: a screen reader that handles frames badly, a dark page, a message whose formatting hides things.

### Dark theme, height, keyboard, zoom

- **Colours.** Mail is written for a white page, so the frame is always a white sheet in both themes, inside the app's border. Nothing is inverted: inverting colours breaks logos, images and mail that has its own dark design. The Text view follows the app's theme.
- **Height without scripts.** A frame with no script cannot report its height, and its opaque origin hides its document from the app, rightly. The message view is a column filling the viewport: the compact head (sender, subject, date, "Details" for recipients and signatures, the switch, the notes), the label, and the frame, which takes the rest of the height (`flex: 1`, at least 24 rem) and scrolls inside itself. Nothing the message says sets a size the app uses.
- **Keyboard and screen readers.** The page's focus goes to the subject, the `h1`, as on every page; Tab goes through the head's controls, then into the frame's links in order, then out. A link is announced by its text; its `title` is the canonical URL. Images keep their `alt`; the box of a remote image says "Remote image not loaded" and its `alt`; the box of an image not shown says why ("Image too large", "Animated image not shown", "Image not shown").
- **Zoom.** The browser's zoom and text size apply inside the frame. No animation reaches the frame: animated images are not shown and CSS animations are dropped.

### Routes

| Request | Needs | Answers | Errors |
|---|---|---|---|
| `GET mailboxes/{id}/messages/{message}/html` | a session; when any `Sec-Fetch-*` header is sent, `Sec-Fetch-Dest: iframe` and `Sec-Fetch-Site: same-origin` | 200 `text/html; charset=utf-8`, the document, with the frame's headers | 404 when the message is not the account's, has no HTML that is `shown`, or the request is not a frame's; 401; 429 (the account's share, "Admission"); 503 (busy); 500. Every answer has the frame's headers and a fixed HTML body ("This message can't be shown here."), never JSON and never text from the message |
| `GET mailboxes/{id}/messages/{message}/links/{link}` | a session | 200 `MessageLink`: `{link, url, host, shown?}` | 404 `not_found` for a `{link}` that is not `<n>.<tag>` or a message that is not there; 409 `link_changed`; 429; 503 `busy` |

`{link}` is `<n>.<tag>`: `n` in decimal without leading zeros, below 1,000, and the 16-character tag. `MessageLink.url` is the canonical URL; `host` is its host and, when not the default, `:port`, exactly as the WHATWG `URL.host` writes it; `shown` is the host the link's text named when it named another one.

### The link page

`/inbox/:mailbox/:message/links/:link`, inside `RequireSession`, opened in a new tab by the frame's link. That tab was opened from the frame's opaque origin, so its first request is cross-site: it carries the `Lax` device and provider cookies and not the `Strict` session, and needs neither (the app's page is static). Its calls to the API are made by the app's own top-level document, same-site, and carry the session (release evidence: "the new tab's session").

It asks `GET .../links/{link}`, then parses `url` with the browser's own `new URL()`, requires `protocol` to be `http:` or `https:` and `URL.host` to equal the server's `host` (a disagreement shows "This link can't be opened from Permaily." and no button), and uses that one `URL` object for everything it shows and for where it goes. It shows:

- "This link leads to" and the host (`URL.hostname`, ASCII, an `xn--` form as it is), large; the port after it when there is one; and the scheme ("https", or "http, not encrypted");
- the whole address (`URL.href`) in monospace, breaking anywhere, as text, never a link, inside a left-to-right isolate (`<bdi dir="ltr">`), with any code point outside printable ASCII written as `\u{…}` (the server writes ASCII only, so this is a second line);
- when `shown` is set: "The message showed <shown>, but this link leads to <host>.";
- "Opening it tells <host> that the link was followed, and when. The address itself may tell them who you are. Nothing is sent to <host> until you press Open.";
- a `<button>` "Open <host>", and "Back to the message", a link to the app's message view.

Pressing the button calls `window.open(url.href, '_blank', 'noopener,noreferrer')` from its click handler, then the page says "Opened in a new tab. You can close this one." The destination is in no `href`, no `src`, no prefetchable attribute and not in the address bar before the button is pressed. A middle-click, "Open in new tab", a long press or a link preview on the button does nothing: a button is not a link. The button is reached by Tab after the address and "Back to the message" comes after it; a screen reader announces "Open <host>, button"; Enter and Space press it; focus starts on the page's `h1`, "This link leads to <host>". On `link_changed`, `not_found`, `busy` or a failed `new URL()`, the page says so and offers no button.

This is the second place the app leaves for another site, always for an address the server answered and the browser parsed, never for one read from the app's address bar; `web/AGENTS.md` names it with the fifth pull request.

### Tests

The fifth pull request is done when every item below passes. Numbered so a review can check each.

**The hostile corpus**, `internal/mailhtml/testdata/hostile/`, one message per file with its case named:

1. `<script>` in every form: upper case, inside SVG, inside a table, after a broken comment, `type=module`, `speculationrules`.
2. Every `on*` attribute, on kept and dropped elements.
3. `javascript:`, `JaVaScRiPt:`, `java&#x09;script:`, `\x00javascript:`, `vbscript:`, `data:text/html`, `data:image/svg+xml` in `href`, `src`, `action`, `formaction`, `background`, `poster`, `xlink:href`.
4. CSS `url()` quoted, unquoted and escaped as `\75 rl(`, in `background`, `list-style`, `border-image`, `cursor`, `content`; `@import`; `@font-face`; `expression()`; `behavior`; `-moz-binding`; `position: fixed` overlays; `</style>` inside CSS.
5. Remote `img`, `srcset`, `picture`/`source`, `video poster`, `input type=image`; `link rel=stylesheet|preload|prefetch|dns-prefetch|preconnect|icon`; `meta http-equiv=refresh|set-cookie|content-security-policy`; `base href` and `base target`.
6. Forms and every control; `iframe`, `frame`, `object`, `embed`, `applet`, `portal`, and `srcdoc` inside them.
7. SVG with `<script>`, `<foreignObject>`, `<use href>`, `<animate>`, `<set>`; MathML `<maction>`, `<annotation-xml encoding=text/html>`.
8. The mutation-XSS classics: `<noscript><p title="</noscript><img src=x onerror=...>">`, `<svg><style><img onerror>`, `<math><mtext><table><mglyph><style><img onerror>`, `<form><math><mtext></form><form><mglyph><style></math><img onerror>`, `<!--><img onerror>-->`, `<xmp>`, `<textarea>`, `<title>`, `<template>`, `<![CDATA[` in foreign content, unclosed attributes and backticks.
9. The overlay, verbatim: `<div style="height:70vh">…</div><a href="https://phish.example" style="display:block;position:relative;top:-70vh;width:100%;height:70vh;background:#fff">…Permaily… Confirm it is you…</a>`, and the same with `opacity:0`; the same done with `margin-top:-70vh`, `transform`, `float` with a negative margin, `height:0` on the parent with the link overflowing, `font-size:0`, `line-height:0`, `text-indent:-9999px`, `color:transparent`, `visibility:hidden`, `pointer-events:none`, `clip-path`, a `<style>` rule `a { display:none !important }` and `#x a.y { color:#fff !important }`.
10. Numeric and other refused hosts, verbatim: `http://2130706433:8080/x`, `http://0x7f000001:8080/x`, `http://0177.0.0.1/`, `http://127.1/`, `http://127.0.0.1/`, `http://[::1]/`, `http://user:pw@example.com/`, `https://evil.example\@good.example/`, `http://exa%6Dple.com/`, `http://example.com./`, `http://example.0x10/`, `http://example.com:080/`, `http://localhost/`; each becomes text.
11. The decoding-cost images: a valid 260,987-byte PNG that declares 8192 x 8192 RGBA (made by the test: `IHDR` and one `IDAT` of deflated zeros); a PNG 8,193 px wide and one exactly 8,192 x 2,048; 50 images whose pixels together are the limit and one past it; an APNG (`acTL`); an animated GIF (two image descriptors, and a `NETSCAPE2.0` loop); a GIF frame outside its logical screen; an animated WebP (`VP8X` with the animation flag; and `ANMF` without it); a truncated chunk; each counted under its reason.
12. The output bound, verbatim: 2 MiB of `&` (refused at 4 MiB of output, while it is written).
13. The tree bound: 524,288 `<br>` in 2 MiB (refused by the tokeniser pass before any tree); 50,000 and 50,001 start tags; nesting at 256, 257 and 513; and the amplification case: `<div>`, then 400 `<b id=N>` with distinct `N`, then `</div><div>x` repeated to 2 MiB (refused by the tokeniser pass; without it the parser's reconstruction clones 400 elements a round).
14. Charsets: no `charset`, the first 1,024 bytes ASCII and UTF-8 after them (shown as UTF-8, where `DetermineEncoding` says Windows-1252); a header that disagrees with a BOM (the header wins); a `<meta charset>` after 1,024 bytes (ignored); a wrong and an unknown `charset`; UTF-7 and `iso-2022-kr` (`unreadable`); invalid UTF-8; a part that is 2 MiB in Windows-1252 and more in UTF-8 (`too_large`).
15. `cid:` to a missing part, to a part that is not an image, to SVG, to an image whose bytes are not its declared type, to one over each limit.
16. Links whose text is another address (`https://bank.example` leading elsewhere), an IDN look-alike host, a `mailto:` whose text is another address, 1,000 and 1,001 links, a URL of 4,096 and 4,097 bytes.
17. Link tokens: a link page's `<n>.<tag>` rendered by one sanitiser and checked by another that dropped an earlier link answers `link_changed`; a tag from another message or mailbox answers `link_changed`; a malformed `{link}` answers `not_found`.

**In Go**, for every corpus file:

18. The output holds no element, attribute, URL scheme, CSS property, value, function or at-rule outside the allowlist (checked by parsing the output, not by searching its text), parses back to itself, is within every limit, and every `a` carries the forced style.
19. Each budget stops the work while it runs: a test reader and a test clock show the parse stopped within one read of the deadline, the output writer stopped at its cap, and no tree built for items 12 and 13 (allocation counted with `testing.AllocsPerRun` or the benchmark's memory).
20. The charset order of item 14, step by step.
21. `FuzzSanitise` holds 18 on any input, run by `make fuzz`; `FuzzCSS` on the tokeniser; `FuzzImageStructure` on the walker.
22. Golden outputs of real messages (a receipt, a newsletter with `@media` and floats, a Gmail confirmation), so a change of output is seen in review.
23. Benchmarks on the pinned x/net/html of items 12, 13, a 2 MiB real-looking newsletter and the 50-image message: time and peak memory, written into the Limits table.

**The URL agreement table**, `internal/mailhtml/testdata/urls.json`: inputs (items 10 and 16, plus valid hosts with ports, IDNs, upper case, percent-encoded paths, queries and fragments), each with Go's verdict and, when kept, the canonical URL and its `host`. A Go test writes and checks it; a web test (Vitest, Node's WHATWG `URL`) and the browser suite (each engine's `new URL()`) read it and assert:

24. For every kept URL, `new URL(url)` gives the same `href`, `protocol`, `hostname`, `port` and `host` the server wrote.
25. For every refused numeric spelling, `new URL(input).hostname` is an IPv4 address, which is why it is refused.

**In real browsers**: a Playwright suite under `web/e2e/` (the site's `site/playwright.config.ts` is the model) against a Go test server (`internal/mailhtml/frametest`, tests only) that serves over TLS, at an app host name, a page with the app's CSP and `__Host-` cookies and the viewer's exact `<iframe>`, and the real route's handler; a second host stands for the internet and records every request it gets, with a resolver it controls. Run in Chromium in CI, required; Firefox and WebKit run the same suite in CI too. The corpus runs twice, so neither layer hides the other's failures:

26. **Run A, the frame alone**: the raw hostile HTML, unsanitised, served with the frame's headers into the viewer's frame. Asserted: no script ran (a marker the hostile scripts would set in the frame or the parent stays unset; no dialog opened); the parent's DOM, `document.cookie` and storage unchanged; the internet host received no request without a click (images, fonts, CSS, media, frames); the top window's URL unchanged; a meta refresh did not navigate. DNS lookups from `dns-prefetch` and `preconnect` are recorded per browser, not asserted (their first line is the sanitiser).
27. **Run B, the sanitiser alone**: the sanitised output served with no CSP, no sandbox and no frame, as a plain page of the test origin. Asserted: no script ran; the internet host received nothing at all, DNS included; the page's URL unchanged after load and after a wait; the DOM each browser built holds only allowlisted elements, in the HTML namespace, and allowlisted attributes; every `href` resolves to the app's link page and every `src` is `data:image/(png|jpeg|gif|webp)`; for every link, the computed style is the forced one, its box is inside the document's width, and `document.elementFromPoint` at the centre and the corners of each of its client rectangles returns the link or a descendant (nothing covers it, it covers nothing it does not contain).
28. With both lines: a click on a link opens one new tab on the link page, without an opener; the frame's URL is the one it was given; the frame's headers; opening the route in a tab answers 404.
29. The link page: the destination is in no `href` or `src` (the DOM is searched), nothing reaches the internet host before the button is pressed (page load, hover, focus, a wait), one request after it, without a referrer; the page with `shown`; `link_changed`.
30. The agreement table of items 24 and 25.
31. The back/forward cache: sign out in the message view, go back: the message is not shown until `me` and `message` answer again.

**Web unit tests** (Vitest, `fetch` played):

32. The switch and its default; each note (remote images, images omitted by reason, `too_large`, `unreadable`, `unavailable`); the Text view's two labels; the trusted label and the sender above the frame, in their exact words.
33. The frame's attributes exactly (`sandbox`, `referrerpolicy`, `title`, no other), a new element per message; `pageshow` with `persisted` hides the message until it is checked again.
34. The link page: with and without `shown`; the destination as text and in no `href`; the button calls `window.open` with the `URL` object's `href` and `noopener,noreferrer`; a `host` that disagrees with `new URL()` gives no button; `link_changed`, `not_found`, `busy`; `expectAccessible()` on both pages.

**Server tests**:

35. The frame route's headers on 200 and on every error; 404 for each Fetch Metadata combination but `iframe` with `same-origin`, and 200 with none; the owner checked before the raw message is read (the fake reader records no call for another account's message); error bodies fixed.
36. `Message.html` counts equal to what the document holds; `unavailable` when no slot comes or the deadline passes; the link route's answers; the render slots: two at once, the queue of sixteen and its five seconds, one running and two waiting per account, 429 and 503 past them.
37. The app's CSP pinned with `frame-src 'self'`.

### The release gate

A release that shows HTML ships only when every check of docs/acceptance.md, "F2 step 2: showing a message's HTML", has passed in the real browsers it names and is recorded there with the browser versions and the date. They are what reading the specifications could not settle: whether each browser sends the `Strict` session cookie on the frame's first navigation, and what the route does when it does not; that the frame's own requests carry no session; the link page's tab and its session; missing Fetch Metadata; the frame's URL opened directly, from history, from session restore and from another message's link; print, reader mode and view source; link previews on iOS; the image limits on an iPhone; the back/forward cache; and the two runs of the corpus by hand in Safari on macOS and iOS (docs/accessibility.md's manual pass gains the frame).

### Its pull request: the fifth

A pull request of its own, built once 2 and 3 have merged, so the inbox works as text first and the riskiest part gets a review of its own, by someone who reads only it; a release takes all six.

- **Owns**: `internal/mailhtml/` (sanitiser, CSS tokeniser, image walker, budgets, corpus, URL table, fuzz targets, golden outputs, benchmarks, `frametest`); `internal/inbox/htmlpart.go` (the HTML part, its charset and its `cid:` parts), the `link` purpose in `internal/inbox/box.go`, and the one call in `read.go` that fills `Message.html`; `internal/httpapi/mailboxes_html.go` (the two routes and the render slots, registered in `mailboxes.go`'s mux by two lines); `internal/router` (`frame-src 'self'`); `web/src/pages/inbox/MessageFrame.tsx`, the switch, the label and the notes in the message view, `web/src/pages/inbox/Link.tsx` and its route in `App.tsx`; `web/e2e/` and its CI job; `web/AGENTS.md` (the second place the app leaves for another site), docs/web.md, docs/threat-model.md ("Mail shown in the app": built), docs/testing.md, docs/accessibility.md, docs/acceptance.md (the gate's rows), and this section's Limits table with the measured numbers.
- **Must not touch**: what pull requests 0 to 4 own beyond the lines named.
- **The seam with pull request 2**: `Reader.Get` leaves `Message.html` absent until this one fills it; the web app shows the text and no switch when it is absent, so 3 works unchanged before 5 and needs no flag. With 5: `mailhtml.Render(ctx, in mailhtml.Input, w io.Writer) (contracts.MessageHTML, error)` and `mailhtml.Links(ctx, in mailhtml.Input) ([]mailhtml.Link, error)`, where `Input` is `{HTML []byte (UTF-8), Inline map[string]InlinePart, Mailbox, Message string, LinkKey []byte}` and a `Link` is `{Path, URL, Host, Shown string}`; `Reader.Get` renders into a counting writer, the HTML route into the response, the link route takes `Links[n]`.
- **Rejected: building it inside 2 and 3.** The two would each carry half of the riskiest change of step 2, reviewed among everything else; and while it was unfinished, the inbox could not be tried at all.

### What this rests on (looked up on 2026-11-12)

- HTML, sandboxing: the sandboxed navigation flag stops a frame navigating others but not itself; the sandboxed origin flag gives an opaque origin; the automatic features flag, set without `allow-scripts`, keeps a `<meta>` refresh from navigating; the custom protocols navigation flag is unset by `allow-popups` (and `allow-top-navigation*`); "sandbox propagates to auxiliary browsing contexts" is unset only by `allow-popups-to-escape-sandbox` (https://html.spec.whatwg.org/multipage/browsers.html#sandboxing, "parse a sandboxing directive"; https://html.spec.whatwg.org/multipage/semantics.html#shared-declarative-refresh-steps).
- HTML, policy containers: an `about:srcdoc` document takes a clone of its parent's (https://html.spec.whatwg.org/multipage/browsers.html#policy-containers).
- CSP Level 3: `frame-src` falls back to `child-src` and then `default-src`; `frame-ancestors` does not fall back; resource hints only to listed sources; `sandbox` is not honoured in `<meta>` (https://w3c.github.io/webappsec-csp/).
- WHATWG URL: the host parser percent-decodes a host before "domain to ASCII"; "domain to ASCII" runs UTS 46 with `CheckBidi` and `CheckJoiners` true, `Transitional_Processing` false, and `CheckHyphens`, `UseSTD3ASCIIRules` and `VerifyDnsLength` set to `beStrict` (false for a URL); the "ends in a number checker" sends a host whose last label is all digits, or parses as an IPv4 number (`0x` hex, leading-zero octal), to the IPv4 parser; `\` is a path separator in special schemes (https://url.spec.whatwg.org/#host-parsing, #concept-ipv4-parser).
- Go: `net/url.Parse` keeps `2130706433`, `0x7f000001` and `0177.0.0.1` as hosts, refuses `%6D` in a host, and refuses `\` in userinfo; `idna.Lookup` is non-transitional (`transitionalLookup = false`), with STD3 rules, hyphen and joiner checks and the Bidi rule, and refuses `ab--cd` and `a_b`; `image.DecodeConfig` reports 8192 x 8192 from a 33-byte PNG header; `charset.DetermineEncoding` reads 1,024 bytes, puts a BOM first, and falls back to Windows-1252; `html.Parse` refuses more than 512 open elements; `reconstructActiveFormattingElements` clones every active formatting element not on the stack (x/net v0.62.0, Go 1.26, run and read on 2026-11-12).
- CSS Cascading and Inheritance Level 5, "Cascade Sorting Order": element-attached declarations win over rule sets of the same origin and importance (https://www.w3.org/TR/css-cascade-5/#cascade-sort); CSS 2.1, 9.5: line boxes next to a float are shortened (https://www.w3.org/TR/CSS21/visuren.html#floats).
- MDN: `Sec-Fetch-Site` and `Sec-Fetch-Dest` Baseline since March 2023 (Safari 16.4) (https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Sec-Fetch-Site); `<iframe>` and its sandbox tokens (https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/iframe).
- web.dev, back/forward cache: pages with `Cache-Control: no-store` have historically not been cached, Chrome is changing that; `pageshow`'s `persisted` (https://web.dev/articles/bfcache).
- Apple, Safari on iPhone: touch and hold a link to preview it (https://support.apple.com/guide/iphone/browse-the-web-iph1fbef4daa/ios); the preview loads the page.
- Requests from a sandboxed frame's opaque origin are cross-site (https://github.com/sbingler/schemeful-same-site, https://github.com/w3ctag/design-reviews/issues/1004).
- Not settled by reading, and therefore in the release gate: each browser's cookies on the frame's request and on the link page's tab, `<link rel=preconnect>` under `default-src 'none'`, and the image limits on a phone.
