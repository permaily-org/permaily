# 0026 Mailboxes per account: how they are built

Status: Proposed. Designed on 2026-11-12 for step 2 of F2 (docs/plan.md); not built. The choices marked "for the owner" are assumed as written until the maintainer says otherwise.
Date: 2026-11-12

## Context

ADR 0018 decided what mailboxes are: forwarding only, several per account, nothing from before the forwarding started, retention as F1 has it, a full inbox in the app, disconnecting deletes the mail and destroys the key, and which account is behind a mailbox is verified for Gmail by Google's signed confirmation. This record says how, exactly enough that the server and the web app can be built at the same time by different people (see "Implementation pull requests").

It builds on: the F1 test mailbox (ADR 0009) and its message format, which stays; the records that refuse an earlier copy, `store.Anchored` (ADR 0016); the patterns of accounts (ADR 0017): signed records changed only at the version read, pointers that prove nothing, sealed entries, limits counted per enclave; two enclaves at once (ADR 0017, ADR 0024); confirming again (ADR 0022); public logs (ADR 0015); mail only over TLS (ADR 0011).

The rules this design keeps, from the maintainer: nothing that decides anything lives only in one enclave's memory; the operator and admins can see that mailboxes exist and how many, and cannot read mail or enter an account; logs never hold an address, a subject, a sender, content, a token, or an account, mailbox or message identifier; whoever writes to the table or the bucket cannot read mail, move a mailbox to another account, make a disconnected mailbox accept mail again, or put a mailbox back to an earlier state where that matters.

## Decision in short

- **A mailbox is an anchored record, readable and signed, with a sealed part.** `mailbox-<id>` holds who owns it, its state, its retention, when it started receiving, whether Google vouched for its Gmail account, and its data key wrapped under the root secret. The address, the label and the Gmail account are sealed under the mailbox's own key: the operator does not see them.
- **An account's mailboxes are listed in one anchored record**, `mailboxes-<account id>`. The limit (five, for the owner) is checked and written there at the version read, so it holds with two enclaves.
- **An address finds its mailbox through a pointer named by an HMAC of the address**, `rcpt#<hex>`: one read, no scan, and the table does not show the addresses. The pointer proves nothing; the record must bear it out and say the mailbox accepts mail.
- **One random data key per mailbox**, wrapped under the root secret as F1's is, never cached across requests. Disconnecting writes a version of the record without it, and removing the record deletes every stored version: no enclave of the deployment unwraps that key again, whatever copy of the table is put back.
- **Disconnecting** stops the address at once on both enclaves (one write of the record), then frees the slot and deletes the pointer; a sweep that either enclave runs deletes the mail.
- **Ingest** reads the pointer and the record at `RCPT TO` and again before storing; a message is stored once per mailbox (a mark of what was seen); retention is still done by DynamoDB and S3, not by a sweep.
- **The inbox** lists, opens, deletes one or a hundred, and searches by scanning sealed copies of sender, subject and text inside the enclave, 2,000 messages a page at most. A message is shown as text only; its HTML never reaches the browser; attachments are downloaded as files.
- **The test mailbox** becomes a mailbox of the account named in the image (`legacy_mailbox_owner`), with its address, its key and its mail, at the first start of the release; `/v1/mailbox` answers 410 and its token goes.
- **The operator and admins** see counts and the readable part of each record, never an address; they can pause and resume a mailbox, never disconnect or read it.
- **Notices by mail**: a mailbox added, forwarding confirmed by Google (once a mailbox), a mailbox disconnected, paused, resumed.
- **Confirming again**: disconnecting needs the fresh proof of ADR 0022; nothing else of a mailbox does.

## Records and layout

All in the one DynamoDB table (`permaily-mail`) and the mail bucket, as F1 and accounts are.

| What | Where | Treatment | Versions and writes |
|---|---|---|---|
| A mailbox | Record `mailbox-<mailbox id>` (`pk=system`, `sk=record#mailbox-<id>@<version>-<hash>`), its commits in the anchors bucket | Signed (`envelope.Sign`, the signing key of accounts). Readable: `v`, `id`, `account`, `state` (`active`, `disconnecting`), `rcpt` (the HMAC of the address, hex), `created_at`, `retention_days`, `receiving_since`, `verified_at`, `paused_at`, `disconnected_at`, `disconnected_by` (`person`, `account_removed`). Sealed inside it as `sealed` (`envelope.SealText` under the mailbox data key, bound to `mailbox-private` and the id): the address, the label, the Gmail account Google vouched for. The data key, wrapped under the root secret, is the record's `WrappedKey` | `store.Anchored` with `OnlyNewest`: made with `PutRecord(create)`, changed only with `GetRecordAt` and `PutRecordAt`, removed with `RemoveRecord` |
| An account's mailboxes | Record `mailboxes-<account id>` | Signed: `{v, account, mailboxes: [{id, added_at}]}`, identifiers only | Anchored, `OnlyNewest`; made with `PutRecord(create)`, changed at the version read, removed with the account |
| The pointer from an address | Entry `pk=rcpt#<hex HMAC-SHA-256 of the local part>`, `sk=mailbox` | Signed with where it is stored: `{mailbox, account, write}` | Written once, at version 0; deleted as it was read (`DeleteEntryAt`) when the mailbox is disconnected; never moved |
| A message (as in F1) | Entry `pk=mailbox#<id>`, `sk=msg#<message id>`; raw in S3 `raw/<mailbox id>/<message id>`, tagged with its retention | Sealed under the mailbox key (meta, raw); plain identifiers, sizes, times | Written once; DynamoDB's TTL and the S3 lifecycle rule of its tag delete it |
| What search reads of a message (new) | Entry `pk=mailbox#<id>`, `sk=txt#<message id>` | Sealed under the mailbox key: the senders' names and addresses, the subject, the first 16 KiB of the text | Written with the message, the same expiry |
| That a message was stored (new) | Entry `pk=mailbox#<id>`, `sk=seen#<hex HMAC of the Message-ID, the envelope sender and the size, under a key derived from the mailbox key>` | Plain, holds nothing (`{}`) | Written only where none is (version 0); seven days (TTL) |
| A forwarding confirmation (moved out of F1's sealed state) | Entry `pk=mailbox#<id>`, `sk=confirm#<message id>` | Sealed under the mailbox key: code, requester, when, verified, message | Written once; thirty days (TTL); the ten newest shown |
| The mailboxes being disconnected | Entry `pk=mailboxes-disconnecting`, `sk=<disconnected at, RFC 3339>#<mailbox id>` | Signed; it proves nothing, the record does | Written before the record says `disconnecting`; deleted as it was read when nothing of the mailbox is left |

- **Identifiers.** A mailbox is `mbx_` and 26 base32 characters (16 random bytes), made as accounts make theirs; the test mailbox keeps `test`, which its mail is sealed with. Message identifiers stay F1's: 32 hex characters, time first. `scrub` learns `mbx_`, and no message identifier goes in a log line or in an error that is logged.
- **Why the record is readable and signed, with a sealed part.** The operator needs the state, the owner and the dates to give support and to count; none of it is the person's mail. The address is what lets anyone put mail into the person's inbox (and, from step 5, in front of their agents); the label is typed by the person and may be anything; the Gmail account is a second address of theirs. Those three are sealed. ADR 0017's table listed the forwarding address as readable; this changes it.
- **Why a record of its own for the list**, and not a list inside the account's record: a mailbox's life (made, confirmed, receiving, paused, disconnected) has nothing to do with who gets into the account, and the account's record changes only through `change`, with the checks of sign-in. Kept apart, the mailboxes service never writes `accountDoc` and accounts never learn a mailbox's format.

**Rejected.** A pointer named by the address in plain text: the table would list every address. A scan of mailboxes at `RCPT TO`: the cost grows with the number of mailboxes, and a refused address costs as much as a hit. The address readable, as ADR 0017 listed it: the operator would hold every person's way in for mail. The list as entries: an earlier copy put back would hide a mailbox from its person while it went on receiving.

## The address

- **Made of** twenty characters of `abcdefghijkmnpqrstuvwxyz23456789` (32 letters and digits, without `l`, `o`, `0` and `1`), drawn with `crypto/rand`, at `inbox.permaily.com`: 100 bits. `domain.NewMailboxAddress` makes one. The test mailbox's `u-9b393600758a2985dae3` keeps working: it gets a pointer like any other.
- **Collisions** are found by the pointer's write at version 0 (`ErrExists`): a new address is drawn, three times at most. With a million mailboxes the chance of one is about one in 10^18.
- **Guessing.** With a million mailboxes a made-up address finds one with a chance of one in 10^24, so `RCPT TO` is not limited per sender. Every refusal is the same `550 5.1.1 No such mailbox`: an address never made, one disconnected, one paused, one of another domain. An address that exists costs two reads more than one that does not, which tells only someone who guessed it already.
- **What is compared.** The local part, lowercased, at most 64 bytes; one with a `+tag` is refused (Gmail forwards to the exact address it was given). The domain must be the inbox host, compared without case.
- **The HMAC key** is `Derive("permaily mailbox addresses v1")` from the root secret: the same in every enclave of the deployment, unknown outside.

## Keys

- **One data key per mailbox**: 32 random bytes from `keys.Root.NewDataKey`, wrapped under the root secret with the context `{purpose: mailbox, mailbox: <id>}`, exactly F1's, so the test mailbox's key moves as it is. No KMS call: wrapping and unwrapping are AES-GCM under a key the enclave holds.
- **Not derived.** A key derived from the root secret and the mailbox identifier could not be destroyed short of destroying the root secret.
- **Not cached.** Every request that opens mail reads the mailbox's record anyway (to check its owner and state), and unwrapping costs microseconds, so the key is unwrapped from the record just read, used and dropped. No enclave keeps a key the other one just destroyed.
- **What "the key is destroyed" means.** (1) Disconnecting writes the next version of the record without `WrappedKey`, at the version read; `OnlyNewest` then deletes the version before, which held it. (2) When the mail is gone the record is removed (`RemoveRecord`): a commit says it is gone, and every stored version left, also one whose deletion failed in (1), is deleted. From (1) on, no enclave of the deployment unwraps the key: every read goes through the newest commit, which has no key, and later says the record is gone. An earlier copy of the record put back in the table is refused (ADR 0016). The old wrapped key cannot be moved into another record either: the wrap is bound to the mailbox identifier, and records are signed and committed.
- **What it does not mean.** The wrapped key was in the table, under the root secret. Someone who kept a copy of the table from before and later obtains the root secret could unwrap it and read that copy's mail. The root secret exists only inside enclaves of public releases and in records locked by their KMS keys (ADR 0013), so that takes breaking an enclave or KMS. A KMS key per mailbox would close it, at the cost of a key per mailbox and a seven-day wait to delete one; not done. Said in docs/threat-model.md.

## Lifecycle

### Making a mailbox

`POST mailboxes {label}`, by a person whose account may change (not closing, terms agreed: `Accounts.MayChange`). In this order:

1. Read `mailboxes-<account>` at its version (none: it is made). Identifiers added more than ten minutes ago whose record does not exist are dropped (a creation that stopped). Five left already: `too_many_mailboxes`. Otherwise the new identifier is added with `added_at` and written at the version read; `ErrChanged` reads again, four times at most.
2. Draw the address and write its pointer at version 0, leading to the new identifier and the account. `ErrExists`: draw again.
3. Make the record with `PutRecord(create)`: `active`, no `receiving_since`, the default retention (90), a new data key, the sealed address and label.
4. Send the notice "a mailbox was added". Answer 201 with the mailbox.

A failure after 1 holds a slot for ten minutes; after 2 it leaves a pointer to a record that does not exist, which refuses mail like no pointer. Nothing half made receives mail or shows an address.

### Label and retention

`PATCH mailboxes/{id}`: the record is read at its version, changed and written with `PutRecordAt`, four tries. The label is one printable line of at most 64 characters (`domain.MailboxLabel`), may be empty, and is sealed. A new retention applies to mail received afterwards, as in F1: each message's expiry is fixed when it is stored. Neither needs a fresh proof.

### Gmail's forwarding confirmation

- **Detected** at ingest as F1 does it (sender `forwarding-noreply@google.com`, the code in the subject or the body), and stored as a message of the inbox like any other, marked `confirmation`.
- **Kept** as a sealed entry `confirm#<message id>` for thirty days, so the forwarding set-up shows the code without opening the inbox. The record is not written for it: a flood of fake confirmations writes no commits.
- **What the person is shown**: the code, with Copy; the Gmail account that asked, as the message says; when it came; and "Signed by Google", or "Not signed by Google: if you didn't ask Gmail to forward here, ignore it". The link in the message is not shown, and the enclave never follows it (ADR 0009): Gmail's settings take the code.
- **The verified Gmail account** is recorded once: the first confirmation signed by `google.com` (DKIM pass, `d=google.com`) that arrives while the mailbox is `waiting` writes `verified_at` (readable) and the requester (sealed) into the record, at the version read, and sends the notice "Gmail asked to forward". Later confirmations, from another Gmail account or after mail started arriving, are listed and change nothing: someone who learns the address can make Gmail send one, and must not change which account the app names. To change it, the person makes another mailbox.
- **For a mailbox that was disconnected** the confirmation is refused at `RCPT TO` like any mail to it, and Gmail tells its account the address does not accept mail. Nothing is stored.

### Mail starts arriving

The first message that is not a confirmation sets `receiving_since` in the record (one write at the version read; when another enclave set it first, the earlier time stays). The state shown is `waiting` without it, `receiving` with it, and `paused` while `paused_at` is set.

### Disconnecting

`DELETE mailboxes/{id}`, with a fresh proof (ADR 0022). Allowed while the terms are not agreed (it only removes data, like deleting the account); refused while the account is closing (`account_closing`), whose removal does it. In this order:

1. Read the list and the record at their versions. A mailbox not in the list, or not the account's, is `not_found`.
2. Write the entry `mailboxes-disconnecting/<now>#<id>`.
3. Write the record at its version: `disconnecting`, `disconnected_at`, `disconnected_by: person`, no `WrappedKey`, no sealed part. `ErrChanged`: read again; when another request disconnected it, the answer is the same (204). From this write on neither enclave accepts mail for it (each reads the record at `RCPT TO` and before storing) or opens its mail.
4. Take the identifier out of the list at its version, so the slot is free.
5. Delete the pointer as it was read.
6. Send the notice "a mailbox was disconnected". Answer 204.

**The sweep** (`mailboxes.Sweep`), run by every enclave every ten minutes and once right after a disconnect, goes through the entries of `mailboxes-disconnecting` and reads each record. A record `disconnecting`: delete its messages a page of 100 at a time (the S3 object, then the item; `ErrNotFound` counts as done); redo 4 and 5 if they did not happen; when a listing finds nothing and two minutes have passed since `disconnected_at` (longer than an ingest may take, one minute), delete the rest of the partition (`txt#`, `seen#`, `confirm#` and F1's `sk=mailbox` item), remove the record (`RemoveRecord`) and delete the entry as it was read. A record that is gone: delete the entry. A record still `active` an hour after its entry: the disconnect failed at 3; delete the entry.

**A failure in between** leaves, after 2, a mailbox exactly as it was, and the person sees an error and asks again; after 3, a mailbox that accepts nothing and that nobody can read, whose slot, pointer and mail the sweep frees and deletes. Never a mailbox that receives mail nobody can see, and never mail that can be read after the answer said it was deleted. A message stored by an ingest that read the record just before 3 is sealed under the key that is gone, and the sweep's last pass, two minutes later, deletes it. If whoever writes to the table deletes the entry of step 2, what is left is unreadable and expires with its retention.

### Accounts removed, closing and blocked

- **Removed.** `accounts.removeLocked` calls `Mailboxes.RemoveAccount(ctx, id)` before it removes the account's record: every mailbox of the list is disconnected as above (`disconnected_by: account_removed`, no notice), then the list record is removed. Every step can be done twice, and a failure fails the removal, which the account's loop tries again (ADR 0017).
- **Closing** (the seven days): mailboxes go on receiving, and the person can read them. Changes are refused (`account_closing`), as everything else is.
- **Blocked** by the operator (for the owner): mailboxes go on receiving and storing. A block keeps the person out; it says nothing about their mail, Gmail keeps its own copy, and refusing would make Gmail tell the person their forwarding fails. Nobody reads it while the account is blocked: every request reads the account's record.

### Paused by the operator

For abuse (an address flooded, or mail that harms the service), for the owner. `paused_at` is written in the record at the version read: the mailbox refuses mail at `RCPT TO` with the same `550 5.1.1` as an address that does not exist and keeps everything it holds; the person reads, deletes and disconnects as before, sees "Paused by Permaily", and is told by mail. Resuming clears it. The operator cannot disconnect a mailbox or delete mail: that destroys what is the person's, and pausing stops the harm.

## Limits

| Limit | Value | Kept | With two enclaves |
|---|---|---|---|
| Mailboxes an account has (for the owner) | 5 | In `mailboxes-<account>`, written at the version read | Exactly 5 |
| Mailboxes made an hour by an account | 10 | Each enclave's memory | 20 |
| Size of a message | 25 MiB (SMTP `SIZE`, as F1) | Compiled in | The same |
| Recipients of a message | 10 (as F1) | Compiled in | The same |
| Messages an hour into a mailbox | 600; one more is `451 4.7.1` and the sender tries again later | Each enclave's memory, by mailbox | 1,200 |
| Bytes a day into a mailbox | 2 GiB, the same answer | Each enclave's memory | 4 GiB |
| Messages or bytes stored | None (ADR 0019: no limits per person in the first version; retention bounds it and the spending alarm watches it) | | |
| Messages deleted in one request | 100 | Compiled in | The same |
| Messages a search page looks at | 2,000 | Compiled in | The same |
| Search pages an account asks a minute | 30 | Each enclave's memory | 60 |
| Raw messages open at once (a message, an attachment, a download) | 8; one more waits | Each enclave's memory | 8 each |

Counts in memory follow ADR 0017: one in the storage could be set back by whoever writes to it. None of these is dangerous at twice its value.

## Ingest with two enclaves

Both enclaves listen on port 25 behind the same address, and a connection lands on either.

1. **`RCPT TO`**: normalise, compute the HMAC, read the pointer (one item), check its signature, read the record (anchored), check that its `rcpt` and `account` are the pointer's, that it is `active` and `paused_at` is unset, and that this enclave's counts for the mailbox are under the limits. Otherwise `550 5.1.1` (or `451 4.7.1` for a limit). A second `RCPT TO` of the same mailbox in one message is taken once.
2. **`DATA`**, once the message is read: for each mailbox, read the record again; one that stopped accepting meanwhile is skipped (all skipped: `550 5.1.1`). Unwrap the key. Write the `seen#` mark at version 0; when it is there already, the message was stored before (a sender that sent again after losing the answer, or the same message through the other enclave) and is not stored again. Seal and store the meta, the `txt#` entry and the raw message (`inbox.Deliver`). Then, for a confirmation, its `confirm#` entry and perhaps `verified_at`; for the first mail, `receiving_since`. `250` only once everything of every mailbox is stored; any failure is `451 4.3.0`, and the sender tries again, which the mark makes safe. A message without a `Message-ID` has no mark and may be stored twice.
3. **Retention** is DynamoDB's TTL and the S3 lifecycle rule of the object's tag, as in F1: nothing in the enclave sweeps expired mail, so there is nothing for two enclaves to do twice. Reads filter what has expired.
4. **Counters** are only the limits above, in memory. `receiving_since` and `verified_at` are each written once, at the version read.

What it costs: a refused address is one DynamoDB read. A message for a mailbox is two anchored reads (each an S3 listing of the record's commits, its newest commit and one DynamoDB read; a mailbox's record gets few commits: made, a label, a retention, `verified_at`, `receiving_since`, a pause), one conditional write, two item writes and one S3 write: about what a signed-in request costs today.

## Two enclaves: every record and flow

| What | How it holds | Test (named for the implementer; the set-up of `enclaves_test.go` in `services/accounts` and `inbox`) |
|---|---|---|
| Making a mailbox | The list is written at the version read, so two creations through two enclaves cannot pass the limit; the pointer at version 0; the record by a write that only creates | `TestTwoEnclavesMakeMailboxesUpToTheLimit`, `TestAMailboxMadeThroughOneEnclaveReceivesThroughTheOther` |
| Label and retention | `PutRecordAt` on the record, four tries | `TestTwoEnclavesChangingAMailboxDoNotUndoEachOther` |
| Mail after a disconnect | Each enclave reads the record at `RCPT TO` and before storing; nothing is cached | `TestADisconnectThroughOneEnclaveStopsMailThroughTheOther` |
| Disconnecting twice at once | One write of `disconnecting` wins; the other reads it and answers the same | `TestTwoEnclavesDisconnectOneMailbox` |
| A disconnect against an ingest | The ingest that read `active` stores under a key that is gone; the sweep's last pass deletes it | `TestMailStoredWhileAMailboxIsDisconnectedIsDeleted` |
| A disconnect against a change of label | One `PutRecordAt` is refused; the change reads `disconnecting` and answers `not_found` | `TestAChangeDoesNotBringADisconnectedMailboxBack` |
| The sweep run by both | Every deletion can be done twice; entries are deleted as read | `TestTwoEnclavesSweepOneMailbox` |
| One message delivered through both | The `seen#` mark at version 0 | `TestAMessageDeliveredThroughTwoEnclavesIsStoredOnce` |
| The verified Gmail account | Written once at the version read; the first confirmation wins | `TestTwoConfirmationsAtOnceVerifyOneAccount` |
| The test mailbox moved by both at the first start | Every step is a create or a write at the version read, skipped when done | `TestTwoEnclavesMoveTheTestMailboxOnce` |
| Removing an account with mailboxes, by both | Disconnecting each can be done twice; the list record goes last | `TestTwoEnclavesRemoveAnAccountWithMailboxes` |
| Pause and resume | `PutRecordAt` at the version read | `TestAPauseThroughOneEnclaveRefusesMailThroughTheOther` |
| A forwarding set-up whose confirmation arrives through one and is read through the other | Everything is in the storage | `TestAConfirmationReceivedThroughOneEnclaveShowsThroughTheOther` |

What stays one enclave's: the limits above, and nothing else.

## What goes through store.Anchored, and why

- **The mailbox record.** Its going back would make a disconnected or paused mailbox accept mail again, bring back a destroyed key, set a shorter retention back to a longer one, or undo `verified_at`. Read with `GetRecordAt`, written with `PutRecordAt`. `store.ErrLost` (the committed version is gone) is an error and never "no such mailbox": the mailbox refuses mail, its person is told it cannot be read, and nothing makes it again.
- **The list of an account's mailboxes.** Its going back would hide a mailbox from its person while it went on receiving, or give back a slot. `ErrLost` is an error: the person cannot make or list mailboxes until the operator looks.
- **Not the pointer**: it proves nothing, the record must bear it out. One put back after a disconnect leads to a record that is `disconnecting` or gone.
- **Not messages, `txt#`, `confirm#` or `seen#`**: a message removed or put back by whoever writes to the table is the audit log's job (ADR 0018, ADR 0019); a confirmation put back shows an old code; a mark deleted lets one message be stored twice. None of it moves mail between accounts or opens it: each is sealed under its mailbox's key and bound to its mailbox and message.
- **Not the list of mailboxes being disconnected**: an entry deleted by hand leaves unreadable mail to expire with its retention.

## The inbox

### What a message returns

- **Text only.** The text/plain part (the first, decoded); with none, text the enclave takes out of the text/html part with `golang.org/x/net/html`'s tokenizer (already in the module graph through `go-message`, so no new module): text nodes, a line break for each block element, nothing of `script`, `style` or `head`, entities decoded, and each link's target written after its text in brackets, so nothing is hidden behind words. With neither, nothing (`text_from: none`). At most 256 KiB; `text_truncated` past it. The HTML itself never leaves the enclave.
- **No link is made clickable.** The app shows text as text; a person copies what they want to open. The app leaves for another site in one place only (web/AGENTS.md), and a link in mail is the commonest way to fool someone.
- **Rendering HTML is deferred**, not refused. Doing it later needs at least a separate origin for message content, a `sandbox` iframe without `allow-scripts` or `allow-same-origin`, a CSP of `default-src 'none'; style-src 'unsafe-inline'; img-src data:`, no remote images, and links rewritten to show their target. It gets an ADR of its own.
- **Attachments** are listed by index, name, type and size, and downloaded only as files: `GET .../attachments/{index}` answers `Content-Type: application/octet-stream` whatever the message said, `Content-Disposition: attachment` with the name cleaned (`domain.AttachmentFilename`: no path, no control characters, at most 120 characters, `attachment-<n>` when nothing is left; `filename*` in UTF-8 and an ASCII `filename`), `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox; default-src 'none'`, `Cross-Origin-Resource-Policy: same-origin` and `Cache-Control: no-store`. The raw message the same way, as `message/rfc822`, as F1 does.
- **Who sent it**: the DKIM results are returned (`auth`), and the app says "Signed by <domain>" or nothing; it never says a sender is who they claim to be.

### Listing and paging

Newest first; `limit` 1 to 100 (50 without one); `after` is the `next` of the page before, a message identifier (32 hex characters, otherwise `invalid_cursor`). `next` is absent on the last page. Expired messages are left out, as in F1.

### Searching

- **What**: the senders' names and addresses, the subject and the text (its first 16 KiB). The query is split on spaces; each part must appear in one of them, without regard to case. No operators, no phrases.
- **How**: inside the enclave, by reading the mailbox's `txt#` entries newest first, opening each and matching, until `limit` matches or 2,000 entries looked at. The page answers what it found, `searched_to` (when the oldest entry it looked at arrived) and `next` to go on from there. Messages stored before `txt#` existed (the test mailbox's) are matched on what their meta holds: sender, subject and snippet.
- **Cost, at most, per page**: 2,000 entries of about 18 KiB, so about 36 MiB read from DynamoDB in pages of 1 MB, about 4,600 read units (a thousandth of a dollar on demand), about a second, and one page of entries in memory at a time. At the forty messages a day the forwarding screens expect, ninety days are 3,600 messages: two pages search a whole mailbox.
- **Rejected: a sealed index** (an HMAC per word, kept per mailbox). It would answer in one read, but it shows whoever reads the table how often each hidden word appears and when, it has to be kept right as messages are deleted or expire, and the scan is fast enough at the sizes ADR 0018 expects. To be revisited when a mailbox holds more than about 20,000 messages.

### Deleting

One (`DELETE .../messages/{id}`, 204, `not_found` when it is not there) or up to 100 (`POST .../messages/delete`, answering how many were there). Each deletes the S3 object, then the item, then its `txt#` and `confirm#` entries. No fresh proof: a session that can read a message can delete it, as in any mail app.

## The test mailbox

Production has one real account (the owner's) and the test mailbox `test`, `u-9b393600758a2985dae3@inbox.permaily.com`, read with a token by the owner and in docs/operations.md, and the address TLS-RPT reports go to (`deploy/tofu/mail.tf`). The verifier only checks that port 25 answers STARTTLS with the attested key: no address is part of it.

**What happens.** The release that builds this moves the test mailbox into an account at its first start, with its address, its key and its mail, and removes the token:

- `deploy/enclave.json` keeps `mailbox_id` and `mailbox_address` (now: the mailbox to move; `mail.tf` still reads the address for TLS-RPT) and gains `legacy_mailbox_owner`, the contact address of the account that receives it (for the owner; assumed `hello@permaily.com`, the super admin). `mailbox_token_sha256` and `-mailbox-token-sha256` go. Storage and SMTP are on whenever the image names a table (`dynamodb_table`), no longer because it has a mailbox.
- **The move** (`mailboxes.MoveLegacy`) is run by every enclave at start and then every ten minutes until it is done; each step is skipped when done, and is a create or a write at the version read:
  1. If the record `mailbox-test` does not exist: read F1's item (`pk=mailbox#test`, `sk=mailbox`) and open its state with its key. No item: there is nothing to move (a new deployment). Make the record with `PutRecord(create)`: the same `WrappedKey`, the retention and the address from before, `account` empty, `active`, `receiving_since` when its oldest message arrived, the label "Test mailbox", and `verified_at` with the requester when one of its confirmations was signed by Google. `ErrExists`: another enclave made it.
  2. Write the pointer from the address at version 0 (`ErrExists` from a pointer that leads to `test`: done). Its confirmations become `confirm#` entries.
  3. Find the account of `legacy_mailbox_owner` (`Accounts.AccountID`). None yet: stop and try again in ten minutes; the mailbox receives mail meanwhile and nobody reads it. Found: add `test` to its list (not counted against the limit), then write `account` in the record at its version. Only this step ever writes the `account` of a record that has none, and nothing else writes `account`.
- **F1's item stays** where it is, unread by the new release, so v0.11.0 can still run after a rollback (it would see the retention and the codes from before the move, and every message). A later release deletes it (docs/plan.md).
- **`/v1/mailbox` answers 410 `mailbox_moved`** on every path, without looking at a token: the token reads nothing from this release on, and the route goes in the release after. The owner reads the mailbox in the app, like any person: the operator's token reads nobody's mail.
- **What changes for whoever used it.** docs/operations.md's "Read the test mailbox" becomes "in the app, signed in as the owner". `cmd/permaily-enclave/main_dev_test.go` sends mail to a mailbox made through the API. TLS-RPT reports keep arriving at the same address, now in the owner's inbox. Dev mode takes `-mailbox-owner` (by default the super admin, `admin@permaily.test`).

## What the operator and admins see and do

- **Counts and records, never addresses.** `AccountRecord` gains `mailboxes` (how many the account has), `Stats` gains `mailboxes: {total}`, and an account's mailboxes are listed as `MailboxRecord`s: identifier, account, state, created, retention, receiving since, paused at, verified or not. No address, label or Gmail account: the operator does not need them to help, and with them could put mail into anyone's inbox.
- **Finding a mailbox by its address**, for an abuse report that names one: the address is hashed as `RCPT TO` hashes it and the pointer followed, and the answer is a list of none or one `MailboxRecord`. It tells the operator which account owns an address they know already, as finding an account by its address does.
- **Pause and resume**, counted on `/status` as `mailbox_paused` and `mailbox_resumed` (added to `attestation.AdminActions` and printed by `permaily-verify`), logged by kind alone, and written in the admin log when done through the panel. Through the panel they need a fresh proof, as every admin change does.
- **Not**: reading, disconnecting, deleting mail, changing a label or a retention, moving a mailbox.
- **`/status` shows no count of mailboxes**: it is public, and one enclave's.
- **`scripts/accounts.sh`** prints the count with each account, and gains `mailboxes <account id>`, `mailbox find <address>`, `mailbox pause <id>` and `mailbox resume <id>`.
- **The narrow interface stays narrow.** The operator's routes get a second interface, `httpapi.MailboxOperator`, with four methods, each taking an identifier or an address to look up: `MailboxRecords(accountID)`, `MailboxByAddress(address)`, `PauseMailbox(id)`, `ResumeMailbox(id)`. `TestTheOperatorsRoutesCannotChangeAnAddressOrAWayIn` names them too.

## Notices by mail

To the account's contact address, through `notify` (ADR 0017: after the change is stored, never failing it, five seconds at most), in the blocks of ADR 0025 (`internal/domain/mail`). No address, label, code, link or identifier in any of them; the facts are times.

| Kind | Subject | Blocks |
|---|---|---|
| `mailbox_added` | A mailbox was added to your Permaily account | Lead "A mailbox was added to your Permaily account." Paragraph "Mail forwarded to it is kept in your account, and only you can read it, in the app." Facts: When. Note "If this wasn't you, sign in at app.permaily.com, disconnect it, and write to support@permaily.com." |
| `forwarding_asked` (once a mailbox: the first confirmation signed by Google) | Gmail asked to forward to your Permaily mailbox | Lead "Gmail asked to forward mail to one of your Permaily mailboxes, and Google signed the request." Paragraph "Open Accounts in the app to see which Gmail account asked, and the code that turns forwarding on." Facts: When. Note "If you didn't ask, do nothing: no mail is forwarded until the code is entered in Gmail." |
| `mailbox_disconnected` | A mailbox was disconnected from your Permaily account | Lead "A mailbox was disconnected from your Permaily account, and its mail deleted." Paragraph "Its address no longer accepts mail. Turn forwarding to it off in Gmail, or Gmail will tell you that forwarding fails." Facts: When. Note: the support line |
| `mailbox_paused` | Permaily paused one of your mailboxes | Lead "Permaily paused one of your mailboxes." Paragraph "It refuses new mail until Permaily resumes it. The mail it holds stays, and you can read, delete or disconnect it." Facts: When. Note "Write to support@permaily.com to ask why." |
| `mailbox_resumed` | Permaily resumed one of your mailboxes | Lead "Permaily resumed one of your mailboxes. It accepts mail again." Facts: When. Note: the support line |

None on an account's removal (its deletion was told already), for a label, a retention or deleted messages.

## Confirming again

Disconnecting a mailbox needs the fresh proof of ADR 0022 (403 `confirmation_needed`): it deletes mail for good. Pause and resume through the admin panel need it, as every admin change. Nothing else: making a mailbox gives nobody anything (mail only comes in), a label and a retention are the person's own and can be changed back, and deleting a message is what any mail app lets a session do. The web app runs disconnecting through `useConfirmed()`.

## The web app

Screens, routes and states are in docs/web.md ("Mailboxes and the inbox"). In short: Accounts lists the real mailboxes; connecting makes one and opens its forwarding set-up, which shows the address to copy, the Gmail steps, and Gmail's confirmation code as soon as it arrives (asking `GET mailboxes/{id}` every five seconds while that step shows, for fifteen minutes at most); a mailbox shows "Verified: <Gmail account>" or "Not verified"; disconnecting asks, says the mail is deleted, and confirms it's the person. A new Inbox section lists, searches, opens and deletes the messages of one mailbox at a time, and says that nothing from before `receiving_since` is there. What stays sample: which connections use a mailbox (step 5) and connecting with a Google account (F3). The sample's Gmail filter step goes, since ADR 0018 forwards all mail (for the owner).

## The API

The web app's own API: under `/api/v1/` on the app's host, JSON, the session cookie, same origin for every request that changes something (ADR 0017). Errors are `{"error": {"code", "message"}}`. Every route answers 401 `unauthorized` without a session and 404 `not_found` for a mailbox or a message that is not the account's. Changes answer 400 `terms_needed` while the terms are not agreed (disconnecting excepted) and 409 `account_closing` while the account is closing. Types: `internal/contracts/mailboxes.go`; the same shapes in `web/src/lib/api.ts`.

| Request | Needs | Answers | Errors besides the above |
|---|---|---|---|
| `GET mailboxes` | a session | 200 `MailboxList`: `{mailboxes: [Mailbox], limit}`, the oldest first, without the ones being disconnected | |
| `POST mailboxes` `{label}` | a session | 201 `Mailbox` | 400 `invalid_label`, `invalid_body`; 409 `too_many_mailboxes`; 429 `too_many_requests` |
| `GET mailboxes/{id}` | a session | 200 `Mailbox` | |
| `PATCH mailboxes/{id}` `{label?, retention_days?}` | a session | 200 `Mailbox` | 400 `invalid_label`, `invalid_retention`, `invalid_body` |
| `DELETE mailboxes/{id}` | a session and a fresh proof | 204 | 403 `confirmation_needed` |
| `GET mailboxes/{id}/messages?limit=&after=&q=` | a session | 200 `MessagePage`; with `q`, a page of what a search found, with `searched_to` | 400 `invalid_limit` (1 to 100), `invalid_cursor`, `invalid_query` (1 to 200 characters); 429 `too_many_requests` (searches) |
| `GET mailboxes/{id}/messages/{message}` | a session | 200 `Message` | |
| `GET mailboxes/{id}/messages/{message}/raw` | a session | 200 `message/rfc822`, as a file `<message>.eml` | |
| `GET mailboxes/{id}/messages/{message}/attachments/{index}` | a session | 200 `application/octet-stream`, as a file with the attachment's cleaned name | 404 for an index that is not one |
| `DELETE mailboxes/{id}/messages/{message}` | a session | 204 | |
| `POST mailboxes/{id}/messages/delete` `{ids}` | a session | 200 `MessagesDeleted`: `{deleted}`, how many were there | 400 `too_many_messages` (more than 100), `invalid_body` |

A `Mailbox` is `{id, address, label, state: waiting|receiving|paused, created_at, retention_days, retention_options, receiving_since?, paused_at?, source?: {provider: gmail, address, verified_at}, confirmations: [{code, requester?, received_at, verified, message?}]}`. A `MessageSummary` is `{id, received_at, expires_at, size, from: [{name?, address}], subject, snippet, attachments, confirmation}`; a `MessagePage` `{messages, next?, searched_to?}`; a `Message` `{id, received_at, expires_at, size, from, to, cc, reply_to, subject, date, text, text_from: plain|html|none, text_truncated, attachments: [{index, name, type, size}], auth: [{method, domain, result}], confirmation, unparsed}`. Lists are always lists, never null; absent fields are left out. `TestMailboxShapes` pins the JSON.

The admin panel, under `/api/v1/admin/` (ADR 0022: an admin whose account is "passkey only", 404 for anyone else, a fresh proof for changes):

| Request | Answers |
|---|---|
| `GET admin/accounts/{id}/mailboxes` | `MailboxRecordList` |
| `GET admin/mailboxes?address=<address>` | `MailboxRecordList` of none or one |
| `POST admin/mailboxes/{id}/pause`, `POST admin/mailboxes/{id}/resume` | The `MailboxRecord` after it; counted, logged by kind, written in the admin log (`mailbox_paused`, `mailbox_resumed`, with the account) |

`AccountRecord` gains `mailboxes` (a number) and `Stats` gains `mailboxes` (`MailboxCounts`, `{total}`), added by the admin pull request.

The operator's tool, with the admin token on the API host: `GET /v1/admin/accounts/{id}/mailboxes`, `GET /v1/admin/mailboxes?address=<address>`, `POST /v1/admin/mailboxes/{id}/pause` and `/resume`, answering as above.

The F1 API: every path under `/v1/mailbox` answers 410 `mailbox_moved`.

## Options considered

- **A list inside the account's record**, or entries: rejected above.
- **A cache of mailbox records for ingest** (a minute): one enclave would accept mail for a minute after the other disconnected or paused the mailbox.
- **Refusing the mail of a blocked account**: Gmail would tell the person forwarding fails, and nothing is gained while nobody can read it.
- **Letting the operator disconnect**: it destroys the person's mail without their word; pausing stops the harm.
- **Deleting the mail inside the request that disconnects**: up to tens of thousands of objects in one request; removing the key makes it unreadable at once, and the deletion can wait for the sweep.
- **Rendering HTML in a sandboxed frame now**: deferred above.
- **One inbox merging every mailbox**: each page would read every mailbox's partition and merge by time. One mailbox at a time first; a merged view needs no change of format later.
- **Keeping `/v1/mailbox` for the operator**: the operator would read a person's mail.

## Implementation pull requests

Four, built at the same time once this merges, each against the contract above and fakes of the others. Merge order: 1 first; 2 and 4 after it, in either order; 3 at any time. A release takes all four. Whoever owns a file changes it; the others do not, except where a seam says so.

### 1. Server core: records, keys, lifecycle, SMTP, the move

- **Owns**: `internal/services/mailboxes/` (new: the records and the list, the address, making, changing, disconnecting, pausing, `RemoveAccount`, ingest for `smtpd`, the sweep, `MoveLegacy`, and their tests with `enclaves_test.go`); `internal/domain/mailbox.go` (the address, the label, the retention, the attachment's filename); `internal/inbox/inbox.go` and `messages.go`, replaced by `deliver.go` (`Box`, `Deliver`, `DeleteAll`, `DeleteMessages`) on today's parsing; `internal/smtpd` (it asks the service per recipient); `internal/store` (`ListMessageIDs`, which includes expired items, and what `Deliver` needs); `internal/httpapi/mailbox.go` (the 410); `cmd/permaily-enclave` (wiring, flags, storage on with a table, the loops of `Sweep` and `MoveLegacy`); `deploy/enclave.json`; in `internal/services/accounts` only `MayChange`, `AccountID` and the call to `Config.Mailboxes.RemoveAccount` in `removeLocked`; docs/data.md's rows, docs/operations.md ("Read the test mailbox", "Two enclaves", "A new deployment"), internal/AGENTS.md.
- **Must not touch**: `internal/httpapi/mailboxes.go`, `internal/inbox/read.go`, `search.go`, `text.go`, `parse.go`, the web app, the admin routes and the notices.
- **Tests**: every row of "Two enclaves"; the order of a disconnect, with a store that fails at each step; the move run twice and by two at once; every refusal at `RCPT TO` the same (never made, disconnected, paused, another domain, a `+tag`); no address and no identifier in the log while mail is handled (`TestMailEndToEnd`); a record put back to `active` after a disconnect refused.

### 2. Server messages: the inbox, and all of `/api/v1/mailboxes`

- **Owns**: `internal/inbox/read.go` (list, get, raw, attachment), `search.go`, `text.go` (text from HTML), `parse.go` (names with addresses, what `txt#` holds); `internal/httpapi/mailboxes.go` (every route of the table, with the session and same-origin checks taken out of `AccountsAPI` into a helper both use); `go.mod` (`golang.org/x/net` from indirect to direct) and the `vendorHash` in `nix/go.nix`.
- **Must not touch**: `internal/services/`; `deliver.go`, but for the one call that writes `txt#`; `cmd/`, but for mounting the handler (`app.Handle("/api/v1/mailboxes", …)` and with a trailing `/`); the web app.
- **Tests**: every route's answers and errors against a fake `httpapi.MailboxService`; text from HTML (scripts, styles and comments dropped, link targets kept, entities, truncation); the headers of attachment and raw downloads; cleaned filenames; the bounds of search (2,000 looked at, `next`, `searched_to`, the fallback to meta); a message of another account is 404.

### 3. Web: Accounts wired, and the Inbox

- **Owns**: `web/src/pages/accounts/*`, `web/src/pages/inbox/*` (new), the calls in `web/src/lib/api.ts` (the types are there already), `web/src/App.tsx`, `web/src/sections.ts`, `web/tests/pages/accounts/*` and `inbox/*`, coverage thresholds for both, docs/web.md.
- **Must not touch**: `web/src/pages/admin/*`, the server.
- **Tests**: `fetch` played as for the other pages: the list, connecting, the set-up with the code arriving while it asks, verified and not, disconnecting with `confirmation_needed`; the inbox's list, paging, a search with `next`, one message, deleting one and many, the empty states. `expectAccessible()` on each; no mailbox address, search or code in storage or the address bar (`expectNotKept`).

### 4. Operator, admin panel and notices

- **Owns**: `internal/services/accounts/notices.go` (the five notices, as the `Notifier`), `operator.go` and `admins.go` additions (counts, the admin log's kinds), `internal/httpapi/accounts.go` (the admin and operator routes above), `pkg/attestation/status.go` (the two kinds), `cmd/permaily-verify` (printing them), `scripts/accounts.sh`, `web/src/pages/admin/*`, the `mailboxes` fields of `AccountRecord` and `Stats` in `internal/contracts`.
- **Must not touch**: `internal/services/mailboxes` beyond calling it, the person's own screens of the web app.
- **Tests**: each notice's blocks, sent once and never failing what caused it; the operator's and the admin panel's routes, with the method list of the operator updated; the counts on `/status`; the admin log's entries.

### The seams, exactly

```go
// internal/services/mailboxes (pull request 1). What it needs from accounts,
// which *accounts.Service implements (pull request 1 adds the two methods):
type Accounts interface {
	// MayChange is nil when the account may change something now,
	// contracts.ErrAccountClosing or contracts.ErrTermsChanged when not, and
	// contracts.ErrUnauthorized when it is blocked or gone.
	MayChange(ctx context.Context, accountID string) error
	// AccountID is the account whose contact address is address; "" for none.
	AccountID(ctx context.Context, address string) (string, error)
}

// What it needs to tell the person (pull request 4 implements it in accounts;
// nil sends nothing). kind is one of the five of "Notices by mail".
type Notifier interface {
	MailboxNotice(ctx context.Context, accountID, kind string, at time.Time)
}

// Who asks: the adapter fills it in from the session.
type Caller struct {
	Account        string
	ConfirmedUntil time.Time // the session's; Disconnect refuses when it has passed
}

// What *mailboxes.Service gives:
//   List(ctx, Caller) (*contracts.MailboxList, error)
//   Create(ctx, Caller, contracts.MailboxCreate) (*contracts.Mailbox, error)
//   Get(ctx, Caller, id string) (*contracts.Mailbox, error)
//   Update(ctx, Caller, id string, contracts.MailboxUpdate) (*contracts.Mailbox, error)
//   Disconnect(ctx, Caller, id string) error
//   Open(ctx, Caller, id string) (inbox.Box, error)        // the owner checked, the record read, the key unwrapped
//   Records(ctx, accountID string) (*contracts.MailboxRecordList, error)
//   ByAddress(ctx, address string) (*contracts.MailboxRecordList, error)
//   Pause(ctx, id string) (*contracts.MailboxRecord, error)
//   Resume(ctx, id string) (*contracts.MailboxRecord, error)
//   Count(ctx, accountID string) (int, error)
//   RemoveAccount(ctx, accountID string) error              // accounts.Config.Mailboxes
//   Accepts(ctx, rcpt string) error                         // smtpd; nil or the SMTP refusal
//   Ingest(ctx, from string, rcpts []string, raw []byte) error

// internal/inbox: pull request 1 writes Box and deliver.go, pull request 2 the rest.
type Box struct {
	ID            string
	Key           []byte
	RetentionDays int
}
//   Deliver(ctx, store.Store, store.Entries, Box, from string, rcpts []string, raw []byte, auth []AuthResult, now time.Time) (Delivered, error)
//   DeleteAll(ctx, store.Store, store.Entries, mailboxID string, page int) (done bool, err error)
//   DeleteMessages(ctx, store.Store, store.Entries, Box, ids []string) (int, error)
//   List(ctx, store.Store, Box, limit int, after string) (*contracts.MessagePage, error)
//   Search(ctx, store.Store, store.Entries, Box, q string, limit int, after string) (*contracts.MessagePage, error)
//   Get(ctx, store.Store, Box, id string) (*contracts.Message, error)
//   Raw(ctx, store.Store, Box, id string) ([]byte, error)
//   Attachment(ctx, store.Store, Box, id string, index int) (name string, data []byte, err error)
```

`httpapi.MailboxService` (pull request 2) is the person's half of that list with `Open` hidden behind it, and `httpapi.MailboxOperator` (pull request 4) the four operator methods. `cmd/permaily-enclave` passes the one `*mailboxes.Service` to both, with a compile-time assertion that whichever of 2 and 4 merges second adds.

## What it rests on, and what is left

- **The root secret stays inside enclaves.** A copy of the table from before a disconnect, together with the root secret, reads that copy's mail (see "Keys").
- **Google's word for the Gmail account.** "Verified" means Google signed a message saying that account asked to forward here; it does not prove that later mail comes from it.
- **Whether an address exists** is learnt only by guessing 100 bits.
- **A deleted message put back** in the table and the bucket by whoever writes to them shows again in the inbox, until the audit log of ADR 0019.
- **Search scans**: a mailbox of more than about 20,000 messages takes several pages to search through.
- **For the owner**: who receives the test mailbox, five mailboxes an account, a blocked account's mail kept, pausing by the operator, text only for now, one mailbox at a time in the inbox, the Gmail filter step dropped. Listed in the pull request with recommendations.
- **To design while building**: the exact words of the screens and notices, with the designer.
