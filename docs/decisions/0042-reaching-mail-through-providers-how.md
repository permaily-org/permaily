# 0042 Reaching mail through providers: how (Gmail's API, Microsoft Graph and IMAP)

Status: Proposed, not built; revised after its design review and simplified after its second (2026-11-27; "Changed after the second design review (2026-11-27)" answers E1 to E24 and the first review's D1 to D26 by number, and the record below states the simplified design as one whole). Designed on 2026-11-27 for F3's first step (docs/plan.md, "F3: order of work") within the owner's fourteen decisions of 2026-11-27 ("The owner's decisions"), which this record does not reopen. Amends ADR 0018 (mailboxes are no longer forwarding only) and ADR 0026 (the mailbox's record gains a kind and fixed provider fields, a provider mailbox has no address, its partition gains three kinds of entry the sweep deletes, a provider message's claim lives as long as its copy, and a claim taken over checks the deletion mark of the message it names); builds on ADR 0016 (strict anchored records), 0017 (the providers' applications, the sign-in trip), 0024 (two enclaves, `ClockSkew`), 0026 (creation, the claim, `IngestDeadline` and `SweepGrace`, disconnecting, the sweep), 0028 (deletion with the message's kept decisions), 0029 (secrets sent only to the attested key), 0030 (the egress), 0031 (holds, unchanged), 0039 (the due index, the lease, the repair) and 0041 (the queue of notices). What the owner did not decide is decided here, each time the choice that fails closed and is simplest to build and test, and listed in "Choices made for the owner (to confirm)"; what only the owner can settle with counsel is in "The owner's legal gate". Its tests are T1 to T128 (some removed, each saying why); its pull requests are in "Implementation pull requests". A re-review of this revision comes before any of them; the spike ("The spike, before building") comes before the provider adapters merge; no real person's mail is read from any provider before the opening release ("Launch: one barrier").
Date: 2026-11-27

## Context

Today a mailbox receives mail only by forwarding (ADR 0018, how in ADR 0026): the person tells Gmail or another provider to forward to a random address at `inbox.permaily.com`, and the MX inside the enclave seals and stores what arrives. That has three costs the owner wants gone in F3: forwarding brings nothing from before it started; Outlook.com and many work tenants forbid or hide automatic forwarding to outside addresses; and forwarding is a set-up of several steps at the provider that many people do not finish.

The owner decided on 2026-11-27 that a mailbox may instead read the person's mail from the provider: Gmail through its API, Outlook through Microsoft Graph, and a closed list of IMAP providers; the person chooses forwarding or a provider for each mailbox; what is read is stored as a sealed copy exactly as forwarded mail is, so rules, receipts, matches and decisions do not change. This record says how, exactly enough that the record, the sync engine, the three provider adapters, the routes and the web app can be built at the same time against seams committed first.

It is the opposite direction to ADR 0030. There Permaily is an authorization server and vendors are its clients; here Permaily is an OAuth client of Google and Microsoft, holding a refresh token that reads a person's mailbox. That token, and an IMAP app password, are the most powerful secrets the enclave will ever hold for a person: whoever has one reads the whole mailbox at the provider, also what Permaily never stored. The design keeps them inside the enclave, sealed under the mailbox's own key, gone with it.

Synchronising a mailbox is also the first thing in Permaily that deletes mail on someone else's say-so. The design's main rule follows from that: **no stored position, cursor or list ever authorises a deletion**; each deletion is authorised by evidence about that one message, read from the provider in the same step that deletes it. Everything a sync stores is then only a way to do less work, and can be lost or put back without harm beyond work done again. That is what lets this design use no new strict record and no new fence.

## The owner's decisions

Taken on 2026-11-27 in a round of questions; final.

1. Gmail (API), Outlook (Microsoft Graph) and IMAP, built and released in one step together.
2. A mailbox is either forwarding or provider; the person chooses.
3. A sealed copy in the enclave's storage, as forwarding stores it (rules, receipts, matches and decisions unchanged); the provider is read only when syncing.
4. On connecting: new mail from then on, plus 0, 30 or 90 days back, which the person picks (30 by default).
5. New mail by polling only, every five minutes (Gmail's history, Graph's delta, IMAP). No Pub/Sub, no Graph subscriptions, no IDLE.
6. Google in testing mode first (at most 100 test users, added by hand); Google's verification and the CASA assessment only when real users wait.
7. IMAP only to a closed list of providers in the image, port 993, implicit TLS; the egress stays a verifiable allowlist. Arbitrary servers were explained (SSRF, self-signed TLS, hostile servers, abuse from our address) and declined.
8. Outlook: personal and work accounts (some organisations' administrators must approve the application).
9. Folders: every folder except spam and trash; not sent mail.
10. Deletions mirrored: a message deleted at the provider is deleted from Permaily with its kept decisions.
11. Read-only permissions (`gmail.readonly`, `Mail.Read`): Permaily never changes, moves or sends anything in the mailbox.
12. IMAP authentication by an app password, sealed in the enclave.

Added after the second design review, the same day:

13. The three providers open to users together. If Gmail waits on the legal gate (Google's Limited Use), Outlook and IMAP wait too: they may be built and deployed dormant, never opened to users before Gmail.
14. Simplify: one safe effect boundary per protocol, fewer new mechanisms.

## Decision in short

- **A mailbox has a kind**: `forwarding` (every mailbox so far), `gmail`, `outlook` or `imap`, fixed when it is made and signed in its record (ADR 0026), with three more fixed fields (`connected_at`, `history_days`, and the provider account's identity, sealed). A provider mailbox has no address at `inbox.permaily.com`; it counts against the five mailboxes and the ten creations in thirty days like any other.
- **One safe effect boundary per protocol**, each an existing commit ("Effect boundaries"): a provider mailbox is published by ADR 0026's `CreateNew` of its record; it ends at ADR 0026's terminal commit; a message is stored by ADR 0026's claim; a deletion is authorised by fresh provider evidence for that message; the grant changes only at the version read of its one entry; the feature opens only with a release whose image says so.
- **One activation barrier for the whole feature** ("Launch: one barrier"): every release from the first that carries provider code is dormant (`providers.open: false`, compiled): no provider mailbox can be made, nothing is polled. One later release, the opening release, sets it true for the three kinds together, and is cut only once the owner's legal determination is recorded here and the KMS keys of every release before the first dormant one are removed.
- **The legal gate has one choke point** ("The owner's legal gate"): before the opening release no provider mailbox exists in production at all; after it (and in any dormant release a rollback lands on), the mailboxes service's access table refuses, by the signed kind of the mailbox, every opening of it for the model or for a vendor that the compiled `providers.uses` closes. Every path to the model or to a vendor opens a mailbox first.
- **Connecting is an OAuth trip the web app starts and the enclave finishes**, on the pattern of the sign-in trip (ADR 0017): authorization code with PKCE (S256), a state and a nonce, bound to the initiating session, a `__Host-` cookie with `Path=/`; the enclave exchanges the code, checks the granted scopes against a fail-closed allowlist, reads the stable identity, rechecks the session's standing immediately before the record's commit, and makes the mailbox through ADR 0026's creation; the grant is written after.
- **Client identifiers are in the image**, a Google Cloud project and a Microsoft application separate from signing in's; **client secrets come from the operator** over the attested connection (`scripts/provider.sh`, ADR 0029).
- **The grant is one sealed entry** in the mailbox's partition, under the mailbox's key, changed only at the version read: connecting again, a rotated refresh token and a grant that stopped working are all writes of it. Disconnecting removes the key (ADR 0026), so the grant opens to nothing. **Permaily does not call Google's revocation**: the person is told where to remove Permaily's access at their provider, as for Microsoft and IMAP.
- **Cursors are sealed entries, not strict records**: Gmail's history position, Graph's delta links and IMAP's `UIDNEXT` per folder. A cursor lost or put back means messages are read again, and the claim stores each once.
- **A provider message is stored through ADR 0026's claim**, named by the provider's identifier (Gmail's message id, Graph's immutable id, IMAP's folder, `UIDVALIDITY` and UID); the claim lives as long as the copy. A person's delete keeps the claim, so the copy is not imported again; a mirrored deletion deletes the copy, then the claim, so a message restored at the provider is copied again.
- **Deletions need fresh positive evidence about that message**: Gmail's current labels or a 404 for its id; Graph's current parent folder for its immutable id or a 404; an IMAP `UID SEARCH` for its UID completed under an unchanged `UIDVALIDITY`. Evidence is read in the step that deletes. Moves keep a message where the provider's identifier survives them (Gmail, Graph); in IMAP a move is a deletion and a new copy, and the message's kept decisions are lost.
- **One admission rule for history**: whatever the provider lists of the past is admitted only when its provider date is within `history_days` before `connected_at` (less ten minutes' margin) and within the retention from now; new mail from the live stream is admitted always. Retention counts from when Permaily copied the message, as for forwarding.
- **A reset is a resync**: a new baseline, then an add-only enumeration beside the live stream, then each copy that the completed enumeration did not list checked by its own evidence. It runs only when the provider loses the position.
- **The poller** reuses ADR 0039's due index, lease and repair; the lease only saves work, since nothing a poll writes needs a fence.
- **Egress is a policy compiled into the image and enforced inside the enclave**: exact scheme, host, port, method and path template, checked before connecting and before a credential is attached, on every request including continuations; IMAP only to the listed hosts on 993 with the listed name verified by TLS. `/status` publishes the policy and `permaily-verify` compares it exactly.

## Effect boundaries

Each protocol commits its effects at one record and version; everything before that commit is invisible or inert, and everything after it is idempotent or authorised again by its own evidence.

| Protocol | Commits at | Before the commit | After the commit |
|---|---|---|---|
| Provisioning | The mailbox record `mailbox-<id>`, made with `CreateNew` (strict; ADR 0026's creation, step 5) | The trip's checks, the code's exchange, the scope and identity checks, the reservation in the account's list (strict, ADR 0026's steps 1 to 3), and the session's standing read again; nothing of the provider written | ADR 0026's settings and index entry, then the grant entry and the schedule entry, inside `IngestDeadline` from a strict read of the record. A crash after the commit leaves a mailbox that shows "Connect again" ("Provisioning") |
| The grant (connecting again, a rotated token, a grant that stopped working) | The grant entry `grant` in the mailbox's partition, at the version read | A strict read of the mailbox record (active, the same provider identity) | The new token used; the notice of a break queued ("Notices by mail") |
| A sync step and its cursor | Nothing strict. The cursor entries (`sync`, `fold#<folder>`) at the version read | The step's strict read of the record and its read of the grant; the provider's reads; the imports and deletions below, each one its own boundary | The next step starts from the cursor. A cursor lost, stale or put back costs messages read again |
| Import | ADR 0026's claim `seen#<occurrence>` marked stored (`Writer.Mark`, at the claim's version) | The claim, then the raw object, the search entry and the item, in ADR 0026's order | Nothing |
| A mirrored deletion | No commit of its own: authorised by the provider's evidence about that message, read in the same step, under that step's strict read of the record | The evidence | `Reader.Delete` (ADR 0028's mark first, then the copy and its kept decisions), then the claim deleted at the version read |
| A person's delete | ADR 0028's mark (unchanged) | | The copy and its kept decisions deleted; the claim kept, so the copy is not imported again while it lives |
| Disconnecting | ADR 0026's terminal commit of the record (strict) | ADR 0026's fresh proof | Steps in flight end within `IngestDeadline` of their read; the sweep after `SweepGrace` deletes the partition, the grant with it |
| The legal gate and the launch | The image (PCR0): `providers.open` and `providers.uses`, compiled | Dormant releases: no provider mailbox can be made | The opening release: the three kinds offered together; the uses it compiles enforced at every opening of a mailbox |

Two enclaves may run the same step of the same mailbox at once (a lease lost or skewed): both claim the same messages and one stores each; both read evidence and the deletions are the same; both write the cursor at the version read and one loses. Nothing in the table needs a run generation, a grant epoch, a journal or an identity row, because none of those effects is authorised by stored state that an earlier or concurrent run could have left.

## The provider mailbox in the record

### Records and layout

What ADR 0026's table gains. "Sealed" and "signed" mean what they mean there; "strict" means a strict anchored record (ADR 0016).

| What | Where | Treatment | Written how |
|---|---|---|---|
| A mailbox | Record `mailbox-<id>`, as ADR 0026 | Readable and signed, as today, plus `kind` (`gmail`, `outlook`, `imap`; absent only on a forwarding record written before), `connected_at` (the record's creation time) and `history_days` (0, 30 or 90). Sealed inside `sealed`, beside the address forwarding keeps there: the provider account's stable identity (`subject`: Google's `sub`; Microsoft's `tid` and `oid`; for IMAP the host's identifier and the username, lowercased), and its display address as the provider gave it (untrusted, optional) | Strict, as ADR 0026; these fields are set by `CreateNew` and never changed |
| A grant | Entry `pk=mailbox#<id>`, `sk=grant` | Sealed under the mailbox's data key, bound to the mailbox's identifier (`mailbox-grant`): `{v, provider, subject, refresh_token \| imap: {host, username, app_password}, scopes, granted_at, state: ok \| broken, broken_at, broken_why, noticed}` | Version 0 after the record's commit; at the version read after |
| The sync state | Entry `pk=mailbox#<id>`, `sk=sync` | Sealed (`mailbox-sync`), at most 16 KiB: `{v, phase: importing \| syncing \| resyncing, gmail_history, resync: {baseline, page, started_at}, import: {examined, stored, skipped, bytes, started_at, stopped_why}, too_large, partial_folders}` | At the version read, at most once a step |
| A folder's cursor | Entry `pk=mailbox#<id>`, `sk=fold#<hex HMAC of the folder>` | Sealed (`mailbox-fold`), at most 8 KiB: Graph `{folder id, role, delta link, backfill link}`; IMAP `{name, uidvalidity, baseline_uidnext, uidnext, highestmodseq, missing_since, presence_checked_at}` | At the version read, at most once a step; at most 200 a mailbox |
| A provider message's claim | Entry `pk=mailbox#<id>`, `sk=seen#p<hex HMAC of the scope>.<hex HMAC of the identifier>`, ADR 0026's claim | ADR 0026's `SeenEntry` (the message's identifier, when it was received and when it expires), sealed as today, plus the provider identifier and, for IMAP, the UID | ADR 0026's claim and mark; its lifetime is the copy's expiry plus a day, not `SeenFor`; deleted by a mirrored deletion |
| The schedule of a mailbox | Entry `pk=providers#sched#<shard>`, `sk=<mailbox id>` | Sealed under a key derived from the root secret: `{next_due, lease: {holder, until}, backoff_until, failures, last_ok, last_outcome}`. Nothing of the mail, no secret | At the version read; a hint that decides nothing ("The poller") |
| A due minute | Entry `pk=providers#due#<minute>`, `sk=<mailbox id>` | Signed: nothing but its key | Written before the old one is deleted; expires 72 hours after its minute |

- **The scope of a claim's name** is `gmail`, `graph`, or `imap\0<folder name>\0<uidvalidity>`; so the claims of one IMAP folder under one `UIDVALIDITY` share a prefix, which the presence check lists ("Per provider"). The HMACs are under the key ADR 0026 derives for `seen#` names, from the mailbox's key: the table's reader sees how many claims a scope has and when they were written, never an identifier or a folder name.
- **Why the grant is in the mailbox's partition, under its key.** Disconnecting removes the key from the record (ADR 0026, "What disconnecting does to the key"); from that commit on no enclave opens the grant, and the sweep deletes it with the partition. A grant copied from another mailbox does not open (its seal is bound to its mailbox's identifier); one whose `subject` is not the record's is not used (T14).
- **Why nothing here is strict, apart from the record.** ADR 0016's strict records are the right tool for state whose replay would do harm. Here a replayed grant is an older token of the same provider account with the same scopes, or a break already mended (denial of service, which whoever writes the table can do anyway); a replayed cursor reads messages again; a replayed claim either keeps a message from being copied again or lets it be copied once more ("The threat model"). None authorises a deletion. A strict record written by every poll would also cost what the second review measured: every strict read lists the record's whole history and fetches at most 500 objects not read before, so a record written every five minutes outgrows a cold read within two days (E4).
- **The sweep's prefixes** (`partitionPrefixes`, ADR 0026's sweep) gain `grant`, `sync` and `fold#`; provider claims are `seen#` entries already swept. A schedule entry whose mailbox has ended is deleted by the poller or by the repair when it reads the record ("The poller"), and its due minutes expire. A test lists every key a provider mailbox's life writes and checks that the sweep or the poller deletes it (T40).
- **Writes of the mailbox record.** Unchanged from ADR 0026 (made, `receiving_since` once, pauses, disconnect, removal). Connecting again, a rotated token and cursors never write the record.

### Launch: one barrier

Production runs one enclave at a time (docs/operations.md: `scripts/handoff.sh` hands the root secret from the running release to the next on the same instance, and the old enclave is terminated); ADR 0024's second enclave is not deployed. Two releases therefore meet the same data only through a rollback, which `scripts/deploy.sh` makes possible while the older release's KMS key exists (docs/operations.md, "Roll back"). A release from before this record decodes a mailbox's record and an account's list into typed structures and writes a fresh encoding (`internal/services/mailboxes/service.go`, `mailboxDoc`, `listDoc`), dropping fields it does not know, and has no idea that some mail must not reach the model or a vendor. So:

1. **The floor.** Pull request 1 fixes the record's provider fields, the provider claims and the partition's new entries, teaches the sweep and the disconnect every provider kind, and adds the access table's column ("The owner's legal gate"). The first release that carries it is the floor, F. From F on, every release reads, keeps and rewrites provider records and lists whole; no later pull request changes those fields (a change would need its own record).
2. **Dormant releases.** F and every release after it until the opening one compile `providers.open: false`: the connect routes answer `providers_closed`, `Import` refuses a provider kind, the poller and the operator's provider route exist but act on nothing, the web app offers no provider. The code ships and runs in production with nothing to do.
3. **The opening release, O.** One small pull request sets `providers.open: true` for the three kinds together (decision 13), compiles `providers.uses` from the owner's determination, and adds that determination to this record as a dated section. It is cut, and deployed, only when all of these hold:
   - the owner's determination is recorded ("The owner's legal gate");
   - every release before F has been removed from `release_keys` and the change applied, so its KMS key is disabled (docs/operations.md, "Delete the key of an old release": the key is unusable from the apply, deleted seven days later) and no release before F can open the root secret again;
   - the release running when O is handed over is F or later, so a rollback from O lands on a release that keeps provider records and closes their uses;
   - O's staging checks passed (T62, T68, T81, T107 to T110).
4. **Rollback from O** to a dormant release R (F ≤ R < O) is safe by construction: R keeps every record and entry, polls nothing, connects nothing, and its access table refuses the model and vendor uses of every provider mailbox (T68, T118). The person still reads, downloads and deletes their copies, and can disconnect.

What the barrier rests on, stated: that a pre-F enclave cannot open the root secret is verifiable (its key's state is in KMS, which the owner reads; the running image's own key policy is on `/status`). That no pre-F enclave is still running from before its key was disabled is the operator's fact: the handoff terminates the old enclave on the one instance, and with ADR 0024's two instances both would have to run F or later. `/status` says `providers.open` and the compiled uses, which `permaily-verify` checks against its pinned determination (T53, T128).

### Provisioning

A provider mailbox is made by ADR 0026's creation, unchanged except that it has no address:

1. ADR 0026's steps 1 to 3: the account's list gains the reserved identifier, within the five and the ten; its standing read strictly.
2. No address and no pointer (ADR 0026's step 4 is skipped for provider kinds).
3. The session's standing read again ("The trip", step 6), immediately before the commit.
4. **The record made with `CreateNew`**, `active`, holding the wrapped key, the kind, `connected_at`, `history_days` and the sealed `subject`. This is the commit that publishes the mailbox.
5. ADR 0026's settings and index entry, and the notice "a mailbox was added", as today.
6. The grant entry at version 0, then the schedule entry and its due minute, under a context of `IngestDeadline` from a strict read of the record made after step 4.

A crash or a failure before step 4 leaves a reservation that ADR 0026's settler fences after `ReservationAge`, and nothing else: no grant, cursor or schedule is written before the record exists. A crash after step 4 and before step 6 leaves an active mailbox with no grant, which the person sees as "Connect again" and which counts against the five until they connect it again or remove it (T64). A missing schedule entry is remade by the repair or by the person's next look at their mailboxes ("The poller"). A grant written late after a disconnect's commit is inside `SweepGrace`, which the sweep's last pass covers, as for a late delivery (T21). Account removal during provisioning is ADR 0026's M20 (T70).

### One provider account, one mailbox per account

Connecting a provider account already connected to a live mailbox of the same Permaily account answers `provider_account_in_use`: the check reads the `subject` of the account's other provider mailboxes (at most four records, read and unsealed while the list is settled). It is best effort: two connections of the same provider account racing in one Permaily account may both succeed, and the person then has two mailboxes with the same mail, either removable. Two Permaily accounts may connect the same provider account; each completed the provider's own consent, so neither gains access the provider did not grant (choice 3). Nothing deployment-wide records a provider account, so the table's reader learns nothing of who is connected where.

### Kinds and the five

- **Making**: `POST /api/v1/mailboxes` for forwarding as today; a provider mailbox only through the connect flow below, and only when providers are open.
- **The limits** are ADR 0026's: five mailboxes that count, ten creations in thirty days, whatever their kind. Connecting again is not a creation and needs no free slot; it is bounded at ten in thirty days a mailbox, counted in the grant (`MaxReconnectsPerPeriod`), past which it answers `too_many_reconnects` (Google's testing mode needs about four).

### States

The record's `state` keeps ADR 0026's machine (`active`, `disconnecting`, `cancelled`, gone; lost and forked unreadable). What the person sees of a provider mailbox is read from the record, the grant, the sync state and the schedule:

| Shown | When |
|---|---|
| `importing` | The sync state's `phase` is `importing`: the live stream is followed, and the history window is being read too |
| `syncing` | `phase` is `syncing`: polled every five minutes |
| `backoff` | The schedule's `backoff_until` is later than now: the provider answered 429 or 5xx, or did not answer |
| `resyncing` | `phase` is `resyncing`: the provider lost the position ("Resets") |
| `needs_reauth` | No grant, or the grant's `state` is `broken`; not polled; keeps what it holds |
| `provider_off` | The operator removed the provider's secret, or the provider left the list; not polled, no notice |
| `paused` | The record's `paused_at`, as ADR 0026; not polled |
| `disconnecting`, `unreadable` | The record, as ADR 0026 |

| From | To | When |
|---|---|---|
| (made) | `importing`, or `syncing` with 0 days | The first poll captures the baselines ("Bootstrap") |
| `importing` | `syncing` | The backfill read its window, or reached a bound |
| any polled state | `resyncing` | Gmail 404 on the history position, Graph `syncStateNotFound`, `resyncRequired` or 410, IMAP `UIDVALIDITY` changed |
| `resyncing` | the phase before | The resync's enumeration and its checks completed |
| any polled state | `needs_reauth` | `invalid_grant` to a refresh; a 401 that survives one refresh; a refresh answered outside the scope allowlist; IMAP `AUTHENTICATIONFAILED` in two consecutive polls; `LOGINDISABLED`: the grant written `broken` at the version read |
| `needs_reauth` | the phase before | Connecting again with the same provider account writes the grant `ok` |
| any | `provider_off` and back | The provider's secret removed or set again, or the provider removed from the list by a release |

**What the person sees** (each sentence to review with the designer): "Importing: 1,240 messages so far from the last 30 days"; "Up to date, checked 3 minutes ago"; "Checking again in 15 minutes: Gmail asked us to slow down"; "Catching up after Gmail lost our place"; "Permaily can't read this mailbox any more. Connect it again"; "Gmail isn't available right now" (`provider_off`). After an import that reached a bound: "Copied 20,000 messages from the last 90 days; some older ones weren't copied."

**Accounts closing, blocked, not agreed, removed**: as ADR 0026 says for forwarding, polling goes on while forwarded mail would go on arriving, and stops at the commit that ends the mailbox. An operator's pause stops polling, as it stops `RCPT TO`. A step already running when a pause, a disconnect or a secret's removal is committed ends within `IngestDeadline` of its own read, as a delivery in flight does; it may finish the reads and imports it started, and starts nothing.

## Connecting with Google and Microsoft

### The applications

- **Separate from signing in**, and for Google in a separate Cloud project (`permaily-mail`): verification of a restricted scope and its yearly CASA assessment never touch sign-in, and removing a mail secret leaves sign-in alone.
- **Client identifiers in the image**: `deploy/enclave.json` gains `providers: {open, uses, google: {client_id}, microsoft: {client_id}}` (and under `staging`, with `test_subjects`). Choice 1.
- **Client secrets from the operator**, never in the image: `PUT /v1/admin/providers/{google-mail|microsoft-mail}` with the admin token, sent by `scripts/provider.sh set google-mail <client-id>` only to the attested key (ADR 0029), refused when the identifier is not the image's; sealed under the root secret in the providers' applications' record (ADR 0017). Secrets may be set while providers are dormant, so the opening release has them at once. A provider whose secret is not set is off: the connect screen does not offer it, and its mailboxes are `provider_off`.
- **Staging before the opening release.** A staging image may compile `staging.providers.open: true` with every use closed and a list of test subjects (`staging.providers.test_subjects`: the owner's synthetic Google accounts' `sub`, Microsoft `tid:oid`, IMAP host and username); a connection whose identity is not listed is refused after the exchange and before any mail API request (T117). Google's testing mode adds its own list of test users, which holds only those accounts.

### The trip

1. `POST /api/v1/mailboxes/connect/start {provider: gmail|outlook, history_days, label}` (or `{provider, mailbox}` to connect again) by a signed-in session of an open, agreed account, with a free slot for a new mailbox (none needed to connect again). The enclave draws a state (32 bytes), a PKCE verifier (S256) and a nonce, and keeps them sealed in `pending#mailconnect#<SHA-256 of state>` for ten minutes with the account, the session's identifier and its sign-out generation, the provider, the purpose (`connect`, or `reconnect` with the mailbox), `history_days` and the label; sets `__Host-permaily-mailconnect` exactly as the sign-in trip sets its cookie (`internal/httpapi/accounts.go`): `Path=/`, no `Domain`, `Secure`, `HttpOnly`, `SameSite=Lax`, ten minutes, a random value whose hash the entry holds; answers the provider's address. One trip at a time per browser: a second start replaces the cookie, and the first trip's callback fails (T113).
2. The provider's address:
   - Google: `https://accounts.google.com/o/oauth2/v2/auth` with `response_type=code`, `client_id`, `redirect_uri`, `scope=openid email https://www.googleapis.com/auth/gmail.readonly`, `access_type=offline`, `prompt=consent`, `include_granted_scopes=false`, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`.
   - Microsoft: `https://login.microsoftonline.com/common/oauth2/v2.0/authorize` with `response_type=code`, `response_mode=query`, `client_id`, `redirect_uri`, `scope=openid email offline_access https://graph.microsoft.com/Mail.Read`, `prompt=select_account`, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`.
3. `GET /api/v1/mailboxes/connect/callback?code&state` (or `error`) on the app host, one more cross-site arrival in `internal/httpapi/crosssite.go`. Malformed (missing or repeated parameters, a state not 43 base64url characters): refused before any read. The cookie is checked against the entry's hash first; then the entry is consumed by `DeleteEntryAt` at the version read, so one callback consumes it. The cookie is cleared, with the same attributes, on every answer.
4. The code exchanged at the token endpoint with the secret and the verifier, through the provider egress ("Egress"), reading at most 16 KiB.
5. **Checks**, each a refusal with a fixed reason, against a typed token response (`internal/providers/oauth`): the ID token's signature (keys fetched through the provider egress), issuer, audience, nonce and `iat`, with `internal/idp`'s checks exported for mail without its fallback from `oid` to `sub`; a refresh token present; the stable identity (Google `sub`; Microsoft `tid` and `oid`, both required); the granted scopes ("Scopes"); on staging, the test subjects.
6. **Standing, immediately before the commit**: the sealed session still open with the same sign-out generation (accounts' `sessionOpen`, the check the sign-in trip's link makes); the account open, its terms in force agreed; for a new mailbox, then ADR 0026's creation, whose strict read of the list checks the slot again ("Provisioning"); to connect again, the mailbox the account's, active, and the provider identity the record's (`provider_account_mismatch` otherwise). A sign-out, a terms change or a full account that lands before this read makes nothing (`signed_out`, `provider_failed`, `too_many_mailboxes`); one that lands after it does not undo what it made, as a forwarding mailbox's creation racing a sign-out is not undone: the mailbox is the account's and can be disconnected (T114, choice 32).
7. The grant written ("Provisioning", step 6; to connect again, the grant entry at the version read, `ok`). The browser goes to `/mailboxes/<id>`; a failure to `/mailboxes#connect_error=<reason>` with one of the fixed reasons (`cancelled`, `admin_approval_needed` for AADSTS65001 and AADSTS90094, `scope_not_granted`, `provider_account_in_use`, `provider_account_mismatch`, `too_many_mailboxes`, `too_many_reconnects`, `signed_out`, `providers_closed`, `provider_failed`), never a word of the provider's.

A trip saved and replayed after it was consumed meets a code the provider has already redeemed and a cookie the browser no longer holds; it makes nothing (T7, T72).

### Scopes

Requested scopes are compiled in. Granted scopes are parsed from a typed response, canonicalised, then compared with a fail-closed allowlist at the first exchange and at every refresh:

| Provider | Canonicalisation | Must hold | May hold | Anything else |
|---|---|---|---|---|
| Google | `email` and `https://www.googleapis.com/auth/userinfo.email` are one; `openid` as is | `https://www.googleapis.com/auth/gmail.readonly` | `openid`, `email` | Refused, nothing stored |
| Microsoft (the access token's resource scopes, as the response reports them) | `Mail.Read` and `https://graph.microsoft.com/Mail.Read` are one; `openid`, `email`, `profile`, `offline_access` dropped from the comparison | `Mail.Read` | `User.Read` | Refused, nothing stored |

- **An omitted `scope`** is the requested set at the first exchange (RFC 6749, section 5.1: omitted means as requested) and the grant's stored set at a refresh; an empty one is refused (T115).
- **A refresh** asks Microsoft for `https://graph.microsoft.com/Mail.Read offline_access` (bound to the Graph resource); a refresh answered outside the allowlist writes the grant `broken` and stores nothing of it; one with no new refresh token keeps the stored one.
- **The display address** (`email`, Microsoft's `preferred_username` or `email`) is optional and untrusted: shown to the person only, never matched; without it the mailbox shows "Outlook account" or "Gmail account".
- What a 403 or a conditional-access challenge from Graph means is established by the spike; until then a 403 is `backoff` with outcome `forbidden`, and three in a row write the grant `broken`.

### Tokens

- **Refresh** when the access token held in memory is missing or within five minutes of its end, re-reading the grant first. A rotated refresh token replaces the stored one at the version read before the access token is used; a write that fails (another enclave wrote first, or the person connected again) drops this access token and the step ends. Access tokens are never written anywhere.
- **Connecting again** writes the grant at the version read with the new refresh token and `state: ok`. A worker still holding the old access token finishes its step reading the same provider account's mail with the same scopes; its own write of the grant then fails at the version read, so it can neither mark the new grant broken nor put the old token back (T77).
- **Lifetimes**, as documented (confirmed by the spike): Google's refresh token lives until revoked, unused for six months, or the password changes; **in testing mode, seven days**; Google keeps at most 100 refresh tokens per account and client. Microsoft's lives 90 days, renewed by each use; it ends at a password reset, a revocation, or a conditional-access policy.

### Disconnecting

ADR 0026's disconnect, with a fresh proof, unchanged: from the terminal commit no enclave opens the grant (the key is gone from the record), every step in flight ends within `IngestDeadline` of its read, closing its IMAP session and dropping its access token, and the sweep deletes the partition after `SweepGrace` (T5, T74). Permaily does not call any provider's revocation (choice 21): the refresh token or app password becomes unreadable at the commit and is deleted by the sweep, and Google keeps listing Permaily as having access until the person removes it or the token ends. The disconnect's answer, the mailbox list and the notice "a mailbox was disconnected" say so, in fixed words per kind: "Permaily deleted its access. To remove Permaily from your Google account too, open myaccount.google.com/permissions" (Microsoft: account.live.com/consent/Manage or myapps.microsoft.com; IMAP: the provider's app-password page).

## IMAP

### The closed list

In the image (`internal/providers/imap/hosts.go`): nine host entries under six brands and six failure domains, each with its documented support as read on 2026-11-27; a host is listed in a release only with the spike's evidence for every cell of the release matrix.

| Identifier | Brand | Host (993, implicit TLS) | Failure domain | App password (as documented; spike to verify) | Notes |
|---|---|---|---|---|---|
| `yahoo` | Yahoo | `imap.mail.yahoo.com` | Yahoo | Required for any third-party client | Username is the full address |
| `aol` | AOL | `imap.aol.com` | Yahoo (shared infrastructure) | Required | |
| `icloud` | iCloud | `imap.mail.me.com` | Apple | Required (app-specific; two-factor accounts) | Username: the address or its local part |
| `fastmail` | Fastmail | `imap.fastmail.com` | Fastmail | Required | `SPECIAL-USE` |
| `gmx-com` | GMX | `imap.gmx.com` | GMX (1&1) | Offered when two-factor is on; IMAP must be turned on | |
| `gmx-net` | GMX | `imap.gmx.net` | GMX (1&1) | As `gmx-com` | |
| `zoho` | Zoho | `imap.zoho.com` | Zoho US | Offered when two-factor is on; IMAP must be turned on | |
| `zoho-eu` | Zoho | `imap.zoho.eu` | Zoho EU | As `zoho` | |
| `zoho-pro` | Zoho | `imappro.zoho.com` | Zoho US | As `zoho` | Organisations |

Six failure domains: Yahoo (with AOL), Apple, Fastmail, GMX, Zoho US (two hosts) and Zoho EU. Hosts of one failure domain share its connection limits ("Quotas and backoff"). The release matrix (docs/acceptance.md, pull request 9) lists per host: login with an app password (required or offered), `SPECIAL-USE`, `CONDSTORE`, `UIDVALIDITY` stability, connection limits, and the spike's date. Not listed: Gmail and Outlook over IMAP, Proton, and any host the person types. A provider leaves the list only by a release; its mailboxes are then `provider_off`.

### The connection

- **TLS** through the provider egress ("Egress"): `CONNECT <listed host>:993`, then TLS 1.2 or later inside the enclave before any IMAP byte, the listed name as SNI and as the name verified against the image's roots (WebPKI only: no pinning, no exception, no `STARTTLS`, no other port).
- **Authentication**: `AUTHENTICATE PLAIN` (or `LOGIN` without `AUTH=PLAIN`), after TLS. `AUTHENTICATIONFAILED` in two consecutive polls, or `LOGINDISABLED`: the grant `broken`, with "turn IMAP access on at <provider>" for the latter.
- **Read-only on the wire**: a command writer allows only `CAPABILITY`, `AUTHENTICATE`, `LOGIN`, `ENABLE CONDSTORE`, `LIST`, `EXAMINE`, `UID FETCH` (`BODY.PEEK[]`, `UID`, `INTERNALDATE`, `RFC822.SIZE`, `FLAGS`, `MODSEQ`), `UID SEARCH`, `NOOP` and `LOGOUT`, and refuses any other command before it is sent (T33).
- **One session per mailbox at a time**, held only within a step and closed at its deadline.
- **The app password and username** are sealed in the grant; typed in the connect screen (`POST /api/v1/mailboxes/connect/imap {host, username, app_password, history_days, label}`, with the trip's standing check, step 6), tried once before anything is made, never shown or answered back. **Connecting again** is `POST /api/v1/mailboxes/{id}/reconnect/imap {app_password}`: the same host and username, the grant written at the version read, the cursors kept.

### The client and its bounds

- **Library**: `github.com/emersion/go-imap/v2`, at a version pinned by pull request 6 with evidence (the version, and transcript tests) that `EXAMINE` is sent for its read-only select, `CONDSTORE` can be enabled, and a body's literal is read as a stream. `QRESYNC` and `OBJECTID` are not used.
- **The wrapped connection** bounds what any answer can make the enclave hold, before allocating: a line 64 KiB; a body's literal checked against its announced length before it is read, at most 25 MiB plus one byte, streamed into the import's reservation ("Memory"); any other literal 64 KiB; everything but body literals at most 1 MiB a command; nesting 16; 10,000 tokens a line; 1,000 unsolicited responses a command; each command 30 seconds. Over any bound the session closes and the step ends with the outcome `bounds`; a range whose answer passed 1 MiB is asked again in halves (T35, T123).
- **Fuzz targets** (`scripts/fuzz.sh`, a group `imap`): the wrapped reader with go-imap's decoder over arbitrary server bytes, the `LIST`, `FETCH` and `ESEARCH` answers, folder names in modified UTF-7.
- **A played server** for tests: go-imap v2's in-memory server behind TLS with a test CA, scripted to misbehave.

## Egress

The egress for providers is a policy compiled into the image and enforced inside the enclave; the host is untrusted and its allowlist is defence in depth only.

| Destination | Port | Methods | Paths | For |
|---|---|---|---|---|
| `oauth2.googleapis.com` | 443 | POST | `/token` | Google's tokens |
| `www.googleapis.com` | 443 | GET | `/oauth2/v3/certs` | Google's ID-token keys |
| `gmail.googleapis.com` | 443 | GET | `/gmail/v1/users/me/profile`, `/gmail/v1/users/me/history`, `/gmail/v1/users/me/messages`, `/gmail/v1/users/me/messages/{id}` | The Gmail API |
| `login.microsoftonline.com` | 443 | POST; GET | `/common/oauth2/v2.0/token`; `/common/discovery/v2.0/keys` | Microsoft's tokens and keys |
| `graph.microsoft.com` | 443 | GET | `/v1.0/me/mailFolders`, `/v1.0/me/mailFolders/{id}`, `/v1.0/me/mailFolders/{id}/childFolders`, `/v1.0/me/mailFolders/{id}/messages/delta`, `/v1.0/me/messages/{id}`, `/v1.0/me/messages/{id}/$value` | Microsoft Graph; `{id}` of `mailFolders/{id}` may be a well-known name ("Folders") |
| Each listed IMAP host | 993 | IMAP | none | IMAP over implicit TLS |

- **Where it is enforced.** `internal/providers/egress` holds the table (`egress.Policy`) and two dialers: an HTTP transport that, for every request, before it connects and before any `Authorization` header is attached, checks scheme `https`, the exact host, port 443, the method and the path template (one path segment per `{id}`, percent-encoded, no `..`), and drops the request otherwise (`egress_refused`, counted in the log); and an IMAP dialer that connects only to a listed host on 993. Both send `CONNECT` over the allowlisted proxy (vsock 8000), read its reply bounded at 4 KiB, send nothing before the TLS handshake ends, and do TLS inside the enclave with the image's roots and the policy's host as SNI and verified name. HTTP/2 (ALPN `h2`) for every destination the spike shows offers it, HTTP/1.1 for one it shows does not, compiled per destination; no redirect followed, no cookie, `Accept-Encoding: identity`, headers at most 16 KiB, each call 30 seconds, bodies bounded (a history, delta or folder page 4 MiB, a Gmail raw message 34 MiB of base64url, a Graph `$value` 25 MiB plus one byte). The ID-token verifier fetches its keys through this transport too.
- **Continuations.** Graph's `@odata.nextLink` and `@odata.deltaLink` are opaque: each, for folder listings as for delta, is parsed and checked against the same policy and against the route it came from (the same host, the same path template) before it is stored or followed; one that is not is refused, and that folder or listing starts again from its beginning.
- **The host's list.** `cmd/permaily-host`'s `defaultEgress` gains the hosts too, and a test fails when it and `egress.Policy` differ (T45); nothing relies on it: a test runs every adapter through a permissive proxy, an off-origin continuation, and servers with valid certificates for other names, and sees nothing leave (T75, T124).
- **DNS** (choice 23, an amendment of the design brief, which asked for the providers' names to be resolved over DNS over HTTPS). The host resolves the name of a `CONNECT`, untrusted, as for every allowlisted destination; DNS over HTTPS stays for DKIM. What is guaranteed is the WebPKI identity of the logical destination, never its address or location: a host that routes a correctly checked `CONNECT` elsewhere reaches a server that cannot prove the name, and the handshake fails before any credential or mail byte is sent. The host can deny service, and can route to any server that holds a valid certificate for the same name.

## Sync

### The poller

`internal/services/providersync`, run by every enclave, on ADR 0039's pattern as implemented in `internal/services/webhooks`:

- **Due index**: each pass reads the due minute of now from its start and, while the scan is behind, each minute back to `scanBack` (ten minutes), then continues its forward scan, at most `scanEntriesMax` entries a pass, paginating within a minute; a due entry that does not fit the ready queue stays where it is. The next due minute is written before the old one is deleted.
- **Admission**: due mailboxes go into ready queues, one per provider kind and within it one per account, taken round robin; at most 1,024 ready. Per enclave: 8 polls at once, 1 mailbox of an account at a time, 4 IMAP sessions, 2 to one IMAP failure domain.
- **The lease**: the schedule entry is read; a poll is skipped while another holder's `lease.until` plus `ClockSkew` has not passed; it is taken by a write at the version read with this enclave as holder and `until` = now + `LeaseFor` (six minutes); and **released** by the poll's last write, which also sets `next_due` (T126). The lease saves duplicate work and quota; it fences nothing, and nothing needs it to (T22).
- **Steps**: a poll is a sequence of steps, at most `PollBudget` (four minutes) in all. Each step reads the mailbox record strictly (stopping when it is not `active`, is paused, or its kind's provider is off), opens the grant, and does all its work, remote calls and writes, under a context of `IngestDeadline` (one minute) from before those reads, so ADR 0026's `SweepGrace` covers a step across a disconnect exactly as it covers a delivery (T21). A step starts only while the lease leaves `IngestDeadline` plus `ClockSkew`.
- **Interval**: due again at the poll's start plus five minutes (`PollEvery`), plus a jitter up to 30 seconds from the mailbox's identifier; `backoff_until` when later.
- **The repair**, hourly: walks ADR 0026's mailboxes' index, reads each provider mailbox's record strictly, remakes a missing schedule entry or due minute, deletes the schedule entry of a mailbox that has ended, and queues a `needs_reauth` notice not yet marked queued ("Notices by mail"). The person's list of their mailboxes also remakes a provider mailbox's missing schedule entry, which covers one whose index entry was never written.

### Bootstrap

`connected_at` is the record's creation time, taken before any baseline. The first poll captures the baselines and starts the backfill:

- **Gmail**: `users.getProfile`'s `historyId` is the live stream's first position. The backfill lists `users.messages.list` with `q=after:<connected_at − history_days − 1 day>` (no spam or trash) and admits each message by its exact date.
- **Graph**: each included folder's first delta round, unfiltered (no `$filter`, so no 5,000-message cap), is read page by page as backfill; its final `deltaLink` is the folder's live position. A message that arrives during the round appears in it or in the next delta; either way it is admitted.
- **IMAP**: each included folder's `UIDVALIDITY` and `UIDNEXT` (and `HIGHESTMODSEQ` with `CONDSTORE`) from `EXAMINE`, the first time that folder is seen. UIDs at or above the baseline are the live stream; below it, the backfill, by date.

**The admission rule**, the same for the backfill, a resync and a folder discovered later: a message listed from the past is admitted only when its provider date (Gmail `internalDate`, Graph `receivedDateTime`, IMAP `INTERNALDATE`) is at or after `connected_at − history_days − ConnectMargin` (ten minutes) and at or after now less the mailbox's retention; a message with no date, or a date more than `ClockSkew` in the future, is not admitted from a listing. New mail from the live stream is admitted always. So with 0 days a message that arrives in any folder between `connected_at` and that folder's baseline is admitted by its date, and mail from up to ten minutes before connecting may be too (T80, T122).

### Per provider

- **Gmail, live.** `users.history.list?startHistoryId=<cursor>&historyTypes=messageAdded&historyTypes=messageDeleted&historyTypes=labelAdded&historyTypes=labelRemoved&maxResults=500`, page by page. Records are processed in order; the cursor moves to each record's `id` once that record's effects are done, written at the end of each step and at least every 100 records, and to the response's `historyId` after the last page. A cut resumes after the last record done, so a page larger than a step never stalls (T91). For a message added, or whose labels changed, that Permaily does not hold: when its history entry's labels are excluded ("Folders") nothing is fetched; otherwise `users.messages.get?format=raw`, whose current `labelIds` decide (included: stored; excluded: the bytes dropped). For a message Permaily holds that was deleted or had labels added or removed: `users.messages.get?format=metadata&metadataHeaders=` for its current labels, which are the evidence ("Deletions").
- **Graph, live.** Folders from `GET /me/mailFolders?includeHiddenFolders=false` and `childFolders`, paged through checked continuations, at most 200, depth 8; per included folder, `delta` from its cursor with `Prefer: IdType="ImmutableId", odata.maxpagesize=100` and `$select=id,receivedDateTime,parentFolderId`. An addition not held: `GET /me/messages/{id}/$value`. An `@removed` for a message held is a candidate only: `GET /me/messages/{id}?$select=parentFolderId` is the evidence.
- **IMAP, live.** `LIST` (with `SPECIAL-USE` when offered); per included folder `EXAMINE`; a changed `UIDVALIDITY` is a reset of that folder ("Resets"). New messages: `UID FETCH <cursor UIDNEXT>:<current UIDNEXT − 1>` only when the interval is not empty, responses outside it refused. **Presence**, at most hourly a folder (`presence_checked_at`): the folder's claims listed by their prefix (scope = folder and `UIDVALIDITY`), then `UID SEARCH UID <their UIDs>` in commands of at most 10,000 UIDs; each command completed with its tagged `OK`, under the `UIDVALIDITY` the claims were made under, is evidence for the UIDs it asked and did not return; a command that failed or was cut is evidence for nothing (T37). UIDs are never reused under one `UIDVALIDITY` (RFC 9051), so a UID absent from a completed search was expunged from that folder, whatever happened in other sessions meanwhile.
- **Cursor writes.** At the end of each step that changed anything, each changed `fold#` entry and the `sync` entry, at the version read. A step that read nothing new writes nothing but the schedule.

### Folders

One precedence table per provider; exclusion wins over inclusion:

| Provider | Excluded (wins) | Included | Not read at all |
|---|---|---|---|
| Gmail (labels on the message now) | `SPAM`, `TRASH`, `DRAFT`, `CHAT`; `SENT` without `INBOX` | Anything else: `INBOX`, `IMPORTANT`, `STARRED`, `CATEGORY_*`, user labels, no label (archived) | |
| Graph (by opaque id and ancestry) | The folders that `GET /me/mailFolders/{name}` returns for `junkemail`, `deleteditems`, `sentitems`, `drafts`, `outbox`, `conversationhistory`, `syncissues`, `recoverableitemsdeletions`, `scheduled`, and every descendant | `inbox`, `archive`, `clutter`, every folder the person made outside the excluded subtrees | Search folders |
| IMAP (`SPECIAL-USE`, RFC 6154) | `\Junk`, `\Trash`, `\Sent`, `\Drafts`, and their children | `INBOX`, `\Archive`, every other selectable folder | `\All`, `\Flagged` (virtual), `\NoSelect`, `\NonExistent`. Without `SPECIAL-USE`: the pinned names per host, compared after decoding |

- A Gmail message with `SENT` and `INBOX` (sent to oneself) is read (choice 16). Drafts and the providers' system folders are excluded because decision 9 excludes sent mail and drafts are unsent mail (choice 26, to confirm with the owner).
- **Graph roles** come only from the well-known lookups, by opaque id, never from a display name, so a renamed or localised system folder is classified correctly; the ancestry is the listing's `parentFolderId`. A folder whose ancestry is not complete (a parent not listed) is `unknown`: not imported, and a message found in it is not evidence of anything (T119).
- **Discovery that is incomplete** (over 200 folders or depth 8, a listing that failed, a continuation refused) is `partial_folders`, shown: folders not listed are not imported, and nothing is deleted because its folder was not listed.
- **IMAP folders that disappear**: a folder missing from a complete `LIST` is marked `missing_since`; only when it is missing again from a complete `LIST` at least an hour later are its copies deleted, its absence being the evidence. A renamed folder is a deleted one and a new one (choice 20).

### Storing

- **One path for every kind.** `mailboxes.Service.Import(ctx, box, occurrence, msg)` runs ADR 0026's sequence from its step 2.1 under the step's context, with a provider ingress: no SMTP quota is charged and no forwarding confirmation is looked for; DKIM checked and parsed as delivery does, the envelope sender empty, `StoredMeta.Source` (`gmail`, `outlook`, `imap`) and `StoredMeta.Origin` (`live`, `history`) set.
- **The claim** is ADR 0026's, named by the occurrence ("Records and layout"), with two changes to `inbox.Writer`: a provider claim lives until the copy's expiry plus a day (the claim takes its lifetime from the caller), so a message held is never copied twice; and **a claim taken over** (older than `ClaimStale`) first reads the deletion mark (ADR 0028) of the identifier it fixed, and when the mark is there writes nothing of the message and marks the claim stored, so a copy the person deleted while its first writer was stopped is not written again (T121). The second change applies to forwarded mail too, where the same window exists today.
- **Identifiers and dates.** Every message's identifier and `received_at` are from when Permaily copied it, as for forwarded mail, so ADR 0026's walk of the decision days from the mailbox's creation holds unchanged and ADR 0039's cursors see each copy once; the inbox lists history in the order it was copied, each message showing its own date (choice 28). Retention counts from the copy (choice 5).
- **Origin**: `live` when the message came from the live stream or its provider date is at or after `connected_at`; `history` otherwise. Webhooks select only `live` (choice 7), so new mail found by the backfill or a resync during the first minutes still notifies.
- **`ErrClaimed`** (another worker's younger claim) skips the message for this step; the next step finds it stored or takes the stale claim over.
- **Too large**: a known size over 25 MiB is not fetched; bytes read past 25 MiB are dropped; either is counted in the sync state ("2 messages were too large to copy") and the cursor moves past it (choice 8).

### Memory

`providersync` holds one pool of its own, `ProviderBytes` (256 MiB), beside SMTP's budget, which it does not touch: a raw import reserves its worst case before it allocates (Gmail raw 34 MiB encoded + 25 MiB decoded + 25 MiB sealed + 2 MiB parse and text = 86 MiB; Graph `$value` and IMAP literals 25 + 25 + 2 = 52 MiB; an unknown size the maximum), at most two at once; a page (4 MiB) and an IMAP session's non-literal answers (1 MiB) reserve theirs, at most eight and four at once; waiting more than ten seconds ends the step `busy`. That is at most 172 + 32 + 4 = 208 MiB, inside the pool. ADR 0026's arithmetic (1,032 MiB at every limit at once, under the runtime's 1,536 MiB) becomes 1,288 MiB; measured on staging with SMTP, raw imports, pages and hostile but in-bound IMAP answers at once (T81). Entries are bounded by their codecs (`sync` 16 KiB, `fold#` 8 KiB, a claim as ADR 0026's), within DynamoDB's item, and a codec refuses a value past its bound rather than writing it (T123).

### Deletions

- **Evidence, per message, in the step that deletes.** Gmail: the message's current labels excluded, or a 404 for its id. Graph: its current `parentFolderId` in an excluded folder, or a 404 for its immutable id (a parent in an included folder is a move: nothing happens; a parent `unknown` is no evidence). IMAP: a completed presence search that did not return its UID, a changed `UIDVALIDITY` for its folder, or its folder missing twice an hour apart. A missing report, a truncated listing, an incomplete resync, a cursor or a stored list is never evidence.
- **The order**: the evidence; then `Reader.Delete` (ADR 0028: the mark first, then the raw object, the item, `txt#`, `confirm#` and `dec#`); then the claim deleted at the version read. A step stopped between the copy and its claim leaves a claim with no copy: a message restored at the provider before the claim is gone is not copied again until the next resync, or until its claim ends with the copy's retention (T122). A step stopped before the copy is deleted is finished by the next event or evidence for that message.
- **Moves.** Gmail's id and Graph's immutable id survive moves between included folders, which change nothing. In IMAP a move is an expunge from one folder and a new UID in another: the copy is deleted and a new one made, and the old copy's kept decisions are lost; two equal copies in two folders are two messages (choice 20, T36, T120). No content is ever compared to carry a message's identity.
- **Kept decisions** go with the copy, as for a person's delete: `Reader.Delete` deletes `dec#`. A strict hold of ADR 0031 for that message stays in its day's block, holding nothing, until its block's next write an hour after the copy's original expiry, exactly as after a person's delete; its earlier versions stay in the block's immutable history under the anchor's retention, sealed under a key derived from the root secret (choice 29, T89).
- **Receipts** keep naming the message, as they do for a message the person deleted: they record what left (choice 27).

### Resets

A reset (Gmail 404 on the history position; Graph `syncStateNotFound`, `resyncRequired` or 410 for a folder; IMAP `UIDVALIDITY` changed for a folder) is a resync of that scope:

1. A new baseline first (Gmail `getProfile`; a fresh unfiltered delta round for the folder; the new `UIDVALIDITY` and `UIDNEXT`), written with `phase: resyncing`; the live stream continues from it at once.
2. An add-only enumeration of the scope, beside the live stream: Gmail `messages.list` with `q=after:<now − retention − 1 day>`, its page token kept in the sync state so a cut resumes (a token that no longer works restarts the enumeration); Graph the new delta round; IMAP `UID SEARCH ALL`. Each listed message not held is admitted by the admission rule.
3. Only when the enumeration completed (the last page, the `deltaLink`, the tagged `OK`): each claim of the scope that it did not list is checked by its own evidence ("Deletions"), as the quota allows, and deleted only on that evidence. For an IMAP `UIDVALIDITY` change, the new value is itself the evidence for every claim of the old one: those copies are deleted and the folder's messages copied again under the admission rule.
4. Then the phase returns to what it was. The person sees "Catching up". A reset during a resync starts again from a new baseline.

Resyncs are rare (a Gmail position lasts at least a week, and a five-minute poll keeps it fresh), which is why their cost is not bounded further: a Gmail resync of a 20,000-message mailbox costs up to 400,000 units of evidence, about three hours of one mailbox's budget, spent in the background.

### History import

- **Order**: as the provider lists it; IMAP folders are taken a page at a time in turn, so one large folder does not consume the bounds. No order across folders or within Gmail's and Graph's listings is claimed.
- **Bounds a mailbox**: 20,000 messages admitted, 10 GiB, 24 hours of import time (choice 10), counted as examined, stored and skipped in the sync state. Reaching one ends the backfill; the person sees how many were copied and that some were not ("Copied 20,000 messages from the last 90 days; some older ones weren't copied"), never a claim of which. Live sync is unaffected.
- **The backfill is add-only**: it never deletes.

### Quotas and backoff

Budgets are per enclave, fixed, and set so that two enclaves together stay under each provider's published limit (choice 24); values from the providers' guides on 2026-11-27, compiled in, confirmed by the spike:

| Provider | Published limit | Cost (units or requests) | Per enclave, per mailbox | Per enclave, project |
|---|---|---|---|---|
| Gmail | 6,000 units a user a minute; 1,200,000 a project a minute | `getProfile` 1, `history.list` 2, `messages.list` 5, `messages.get` 20 (60 with `raw`) | 2,400 units a minute (live first; imports and resyncs at most 1,800) | 480,000 units a minute |
| Graph | 10,000 requests per app per mailbox in ten minutes; 4 concurrent | 1 a request | 400 in ten minutes, 1 at a time | As documented per tenant; the spike confirms |
| IMAP | Per failure domain, unpublished | 1 a command | 1 session | 2 sessions to a failure domain |

| Answer | What the poller does |
|---|---|
| 429, Gmail's 403 `rateLimitExceeded` or `userRateLimitExceeded`, Graph's 429, 503 or 504 | `backoff`: the provider's `Retry-After` honoured as given, up to seven days (longer is seven days and the outcome `long_retry`); without one, 1, 5, 15, 60 minutes then hourly, each with ±20 % jitter; written in the schedule entry |
| 5xx, a timeout, a reset, IMAP `BYE` or `UNAVAILABLE` | The same |
| Gmail's 403 `dailyLimitExceeded` | Every Gmail poll of this enclave waits until the next hour |
| 401 | Refresh once; again 401: the grant `broken` |
| `invalid_grant`, `AUTHENTICATIONFAILED` in two consecutive polls | The grant `broken` |
| A reset response | "Resets" |
| A bound passed | The step ends `bounds` |

What these budgets do not hold, stated: two Permaily mailboxes of the same provider account (in two Permaily accounts) each have their own budget, and the provider answers 429 when they pass its limit; a schedule entry lost during backoff costs at most one early request, which the provider answers again (T127).

## What changes for the person

### Screens (docs/web.md gains "Mailboxes from a provider")

- **Adding a mailbox** asks first how: "Forward mail to Permaily" (today's flow), and, only once providers are open, "Connect Gmail", "Connect Outlook" and "Connect another provider (IMAP)" together. The provider choices say, before the person leaves: what Permaily will read ("all your mail except spam, trash, sent and drafts"), that it never changes or sends anything, how far back (0, 30 or 90 days; 30 chosen), how often (every five minutes), that the copy is sealed in the enclave and deleted when they disconnect, and, during Google's testing mode, "You'll need to connect Gmail again every 7 days".
- **IMAP**: the provider picked from the closed list, then the username and the app password, with the provider's own page on app passwords linked as text.
- **A provider mailbox's page**: the kind, the display address, the state and its sentence ("States"), how much history was copied, messages too large, `partial_folders`, and the buttons "Connect again" (in `needs_reauth`; for IMAP, the reconnect form) and "Disconnect".
- **Disconnect**, with a fresh proof as today, says what will happen and where to remove Permaily's access at the provider ("Disconnecting").
- **The inbox** is the same; a provider mailbox's delete says "Deletes Permaily's copy; it stays in your Gmail" (`web/src/pages/inbox/`).

### Notices by mail

Through ADR 0041's queue, fixed words, each kind held to its facts:

- "A mailbox was added" (ADR 0026) names the kind in fixed words ("a Gmail mailbox"), never an address or a domain (`internal/AGENTS.md`).
- **New**: "Permaily can't read one of your mailboxes" when its grant is written `broken`: the kind's fixed word, no label, no address, no link. Its event key is `mailbox-needs-reauth/<mailbox>/<broken_at>`. The worker that wrote `broken` queues it with its own time as the event's, then writes the grant's `noticed` at the version read; a worker or the repair that finds a `broken` grant not `noticed` queues it then, with its own time, within ADR 0041's five minutes, and the queue sends each key once. So each break is noticed once, best effort: a crash between queuing and `noticed` is queued again under the same key, which the queue's 30-day mark drops (T101). Not for `provider_off`, `paused` or `backoff`.
- "A mailbox was disconnected" (ADR 0026) adds where to remove Permaily's access at the provider.

### The privacy page and terms

Each to be classified and worded by the owner. TODO LEGAL: owner's review.

- What is read: the person's mail at the provider, every folder but spam, trash, sent and drafts, from `history_days` before connecting, every five minutes, read-only; the account's display address.
- What Permaily sends the provider: only what connecting and reading need: the authorization code, the refresh token and, for IMAP, the username and app password, and its read requests. Nothing of the person's from elsewhere.
- What is kept, and for how long: the sealed copy, as forwarded mail, for the retention from when it was copied; the refresh token or app password, sealed, until disconnect (then unreadable, and deleted by the sweep within minutes); cursors and the sync state until disconnect; a message's claim until its copy expires plus a day, or disconnect; the schedule until an hour after disconnect; the trip ten minutes; sealed backups as docs/data.md says of all stored mail.
- That Permaily does not revoke its access at Google when the person disconnects, and where they do it.
- Deletions at the provider mirrored, with the copy's kept decisions; receipts keep naming deleted messages.
- What the storage's untrusted reader sees: that an account has a provider mailbox of a kind, when it was connected, how far back, how many messages per folder it holds and when they are written; never content, a folder name or the provider account.
- Google API Services User Data Policy and Limited Use: "The owner's legal gate".
- Microsoft's terms for applications using Graph (the publisher's terms and privacy URLs on the registration).
- **Google, Microsoft and the IMAP providers are not subprocessors**: they are the person's own email provider, which Permaily reads on their instruction; `/subprocessors/` says so.
- The terms: connecting a mailbox the person is authorised to read; what happens when a provider stops Permaily's access; testing mode.

## The owner's legal gate

TODO LEGAL: owner's review. Google's Limited Use governs every use and transfer of data obtained through `gmail.readonly`, testing mode included, and a determination made after a transfer cannot undo it. By decision 13, the gate holds all three kinds: none opens to users before the owner records a determination in this record (a dated section, with counsel's input as the owner chooses) and the opening release carries it ("Launch: one barrier").

**Before the opening release**, the gate is closed by construction: production images are dormant, so no provider mailbox can exist in production, and no path to the model or a vendor can meet provider mail. Staging images open only for listed test subjects, with every use closed (T117). The spike uses synthetic or owner-only accounts.

**The choke point, after it and in any dormant release.** Every path that sends a message's content to the model or to a vendor reads it through an `inbox.Box`, and every `Box` is made by `mailboxes.Service.Open` (for a person's request, with an `Access`) or `OpenForConnection` (for a connection). The access table those two apply gains one column: for a mailbox whose signed `kind` is a provider's, an `Access` is refused (`source_closed`) when the compiled `providers.uses[kind]` closes what it is for:

| Access | Used by | Closed by |
|---|---|---|
| `AccessListMessages`, `AccessReadMessage`, `AccessDownload`, `AccessDeleteMessages` | The person's own list, read, download and delete | Never: the person always reaches their own copy |
| `AccessSearch` | The person's search without a condition on meaning | Never |
| `AccessModel` (new) | Every person's request that may ask the model: a search or the matches route (`about` and `mentions`), rules' criteria preview, the removal preview | `uses[kind].model` false |
| `AccessConnection` | Everything a connection does: search, read and attachments over the API and MCP, the consent preview, the default rule's and the person's rules' evaluation in the discloser, webhook candidate selection and the recheck before every frozen notice is sent | `uses[kind].vendors` false |

The routes that may ask the model move to `AccessModel` (`internal/httpapi/mailboxes.go`'s search with a condition on meaning and its matches route; `internal/httpapi/rules_preview.go`'s criteria and removal previews); the webhook worker's recheck already opens through `OpenForConnection` before each send, so work queued while a use was open is not sent once it is closed (T83). Forwarding mailboxes (a record with no `kind`, or `forwarding`) are unaffected. A local preview refused is not a disclosure and makes no receipt; a connection's read that skips a provider mailbox records it as withheld, as for a mailbox the connection does not cover. One test runs every route on the mux and every MCP tool over a provider mailbox in an image with both uses closed, with a model and a vendor egress that fail on any call; a route added without a case in that test fails it (T116).

The questions the owner must answer, each with what it decides:

1. **Uses.** Which user-facing features may use provider mail: showing the person their own sealed copy; rules the person writes; the default rule; connections to vendors the person approves; webhooks; previews. Which are "user-facing features prominent in the requesting application's user interface" under Limited Use, and how the app presents each. The answer becomes `providers.uses` per kind.
2. **Consent.** What the in-product disclosure and consent say before the provider's screen, and whether connecting a vendor or turning on a rule needs a further consent naming Gmail data.
3. **Onward transfers to OpenAI.** Whether sending a Gmail message's subject and text to OpenAI (ADR 0028's Decisions API and Responses API) for a rule, the default rule or a connection is permitted, under what processor terms (no training, retention: Zero Data Retention was asked of OpenAI on 2026-11-15 and is not approved; until it is, ordinary retention, docs/data.md), and whether masking or a narrower part is required.
4. **Onward transfers to vendors.** Whether disclosing approved parts of Gmail messages to a vendor the person connects is a transfer Limited Use allows, and what the vendor must be told or bound to.
5. **Retention.** The sealed copy's retention for provider mail (choice 5 assumes forwarding's 7 to 365 days from the copy), claims, receipts that outlive the copy, and sealed backups.
6. **Human access.** Who may ever read Gmail data (nobody in Permaily's design; the policy's exceptions for security, law and the person's consent), and how that is said.
7. **Outlook and IMAP.** Whether their uses may differ from Gmail's once the three open together (Microsoft's terms for Graph; the IMAP providers' terms); each kind has its own row of `uses`.
8. **Verification and CASA timing.** When to request Google's verification and the CASA assessment, which only matter once real users wait.

## The threat model

- **What the host sees**: that the enclave connects to `gmail.googleapis.com`, `graph.microsoft.com` or an IMAP host, when, for how long and how many bytes each way, and the names it resolves. It cannot open the TLS. It can delay, drop or misroute connections (polls back off; mail waits at the provider); it cannot make the enclave send a credential anywhere a server without a valid certificate for the policy's name could read it.
- **What the storage's reader sees**: the mailbox record's readable fields (its owning account, as today; its kind, `connected_at`, `history_days`); the number of claims per scope (per IMAP folder and `UIDVALIDITY`), their names (HMACs) and write times; the schedule's keys and timing; the sizes and timing of every write, and so how often a mailbox changes and when it is deleted. Never the provider account (sealed in the record), an address, content, a token or a folder name. No entry joins two mailboxes of the same provider account.
- **What the storage's writer can do**: delete or put back entries. A grant: the mailbox reads nothing ("Connect again") or reads with an older token of the same account and scopes. A cursor or the sync state: work done again, or a resync. A claim: a message copied a second time, a copy the person deleted copied again from the provider while it is still there (choice 11), or a message restored at the provider not copied again until the claim ends. A schedule entry: polls missed or doubled. It cannot make the enclave delete a copy without the provider's evidence, roll back the mailbox record (strict), widen the egress or open a use (both compiled).
- **Refresh tokens and app passwords never leave the enclave**: sealed under the mailbox's key, sent only to the provider's token endpoint or the listed IMAP host over the checked egress, never logged (the scrubber drops their shapes, T46), never in an answer. After a disconnect they stay valid at the provider until the person removes Permaily or they end, unreadable to anyone.
- **The operator** registered the applications and knows their client secrets; with a secret alone nobody reads a mailbox. The operator can turn a provider off (denial of service, shown). The operator cannot add scopes (compiled requests, a fail-closed allowlist), cannot open providers or their uses (compiled), and is trusted, as stated in "Launch: one barrier", to have stopped every release before the floor.
- **A compromised Google or Microsoft account of the person**: whoever holds it reads the mail at the provider already; through Permaily they can put mail in (by receiving it) and delete Permaily's copy (by deleting it at the provider), which mirrored deletions make possible by design (decision 10). Receipts of what already left stay.
- **A hostile or compromised IMAP server** on the list: bounded and preflighted reads, a fuzzed parser, a command allowlist on the wire, TLS verified; it can send hostile mail, as any sender can, and claim any message expunged, which deletes only Permaily's copy.
- **Google's testing mode**: at most 100 test users added by hand (so Google, and whoever runs that console, know them); refresh tokens that end after seven days.
- **Microsoft's work tenants**: an administrator may need to approve; a tenant's policies may end tokens at any time; publisher verification is needed for many tenants.
- **Abuse from Permaily's address**: polls bounded per failure domain and per enclave; IMAP a closed list; a person cannot make the enclave connect anywhere they choose.
- **Public telemetry**: `/status` publishes the configuration only, no counts (choice 25); counts per kind and outcome go to the enclave's log once an hour, which the host reads anyway as connection timing.
- docs/threat-model.md has a short section pointing here; pull request 9 fills it in.

## What /status and permaily-verify show

`/status` gains `providers` (`attestation.ProvidersStatus`): `open` and `uses` per kind (compiled); `kinds` and, for each, whether its secret is set; `scopes` (the requested sets and the granted allowlists, exactly as compiled); `read_only: true`; `push: false`; `revokes: false`; `poll_seconds: 300`; `history_days: [0, 30, 90]`; `client_ids`; `egress` (the complete `egress.Policy`: each destination with port, methods, path templates and HTTP version, and the IMAP hosts with port 993 and `tls: "implicit"`); `token_storage: "sealed_per_mailbox"`; on a staging image, the number of test subjects. Never a count, an address, an account, a mailbox identifier or a provider's error text.

`permaily-verify` prints the section and fails when the published `egress` differs in any way from its pinned policy (a missing, extra or duplicate destination, a port, a method, a path or a version), a requested or allowed scope is not in its pinned read-only set, `read_only` is false, `push` is true, `poll_seconds` is under 300, or `open` and `uses` differ from its pinned determination (`open: false` for every release before the opening one) (T53, T128).

## Choices made for the owner (to confirm)

Numbers 1 to 27 keep their meaning from the previous revision where they still apply; "changed" and "withdrawn" say what the simplification did.

1. **Client identifiers in the image**, secrets through the operator's route. *Alternative*: both through the route, as sign-in.
2. **Separate Google Cloud project and Microsoft application for mail**, not the sign-in ones.
3. **Changed. A provider account connects to one mailbox per Permaily account**, best effort; two Permaily accounts may connect the same one. *Alternative*: a deployment-wide strict record per provider account, for a guarantee across accounts.
4. **A move to spam or trash is a deletion** for Permaily, and the copy goes.
5. **Changed. Retention as forwarding's** (7 to 365 days, 90 by default), counted from when Permaily copied the message; history choices only up to the retention.
6. **Every Gmail mailbox reconnects weekly during testing mode**, with a notice each time.
7. **History imports do not notify webhooks**; mail from the live stream, or dated at or after connecting, does, including delayed mail and mail found by a resync.
8. **Messages over 25 MiB are skipped and counted**, as the MX refuses them.
9. **Changed. Concurrency per enclave**: 8 polls, 1 per account, 4 IMAP sessions, 2 to one IMAP failure domain.
10. **Import bounds**: 20,000 messages, 10 GiB, 24 hours a mailbox; how many were copied shown, not which.
11. **Changed. A person's delete in Permaily deletes only Permaily's copy**; the copy is not imported again while its claim lives (the copy's retention), under honest storage.
12. **The IMAP list**: nine host entries under six brands and six failure domains, each kept only if the spike confirms it.
13. **No fresh proof to connect**; the session's standing read again immediately before the commit; a fresh proof to disconnect (as today).
14. **What disconnecting does to the copy**: deleted, as forwarding.
15. **A broken grant is told by mail once per break**, and shown in the app.
16. **Sent mail sent to oneself is read** in Gmail (it carries `INBOX`).
17. **Changed. No strict record for synchronisation**: cursors are sealed entries, because no cursor authorises a deletion.
18. **Changed (decision 13). Every provider kind dormant until the opening release**, which opens the three together.
19. **Changed. The uses of provider mail compiled per kind from the owner's determination**, enforced at the mailboxes' access table.
20. **Changed. IMAP moves and renames are a deletion and a new copy**: the old copy's kept decisions are lost; two copies in two folders are two messages.
21. **Changed. No revocation call at disconnect**: the token becomes unreadable and is deleted; the person removes Permaily at the provider. *Alternative*: a best-effort revocation at Google, which also ends every other Permaily mailbox of that Google account and can end a grant made again within seconds.
22. **Changed. A floor and an opening release**, not a compatibility release: providers dormant from the first release carrying their code; the opening release only once older releases' keys are removed.
23. **DNS from the host**, untrusted; WebPKI identity, not address, is what is guaranteed. This amends the design brief, which asked for DNS over HTTPS. *Alternative*: the providers' names over DNS over HTTPS, which adds a resolver to the egress and still attests no location.
24. **Changed. Fixed per-enclave quota budgets**, set so two enclaves together stay under each provider's limit. *Alternative*: a shared budget in the storage, written per call.
25. **Changed. No provider counts on `/status`**; counts in the log.
26. **Drafts and the providers' system folders are excluded** with sent mail (decision 9 names spam, trash and sent).
27. **Receipts outlive a mirrored deletion**, as they outlive a person's delete.
28. **New. History is listed in the order it was copied**, each message dated with its own date; identifiers are never before the mailbox's creation.
29. **New. A mirrored deletion leaves ADR 0031's strict hold** for the message until the copy's original expiry plus an hour, holding nothing, as a person's delete does.
30. **New. IMAP deletions are mirrored within about an hour** (the presence check); Gmail's and Graph's within a poll.
31. **New. A resync runs only when the provider loses the position**, not on a schedule.
32. **New. A sign-out that lands in the last moment of connecting does not undo it**; the mailbox can be disconnected.
33. **New. Staging opens providers only for listed test subjects**, with every use closed.

## The spike, before building

What must be proven against the real providers, each with what it decides. It runs with the owner's applications and **synthetic or owner-only test accounts** (never a person's mailbox; Gmail only through test accounts that hold mail the owner sent for the purpose), from a workstation, with no Permaily code in production. Its code lives in `spikes/providers/` (its own readme, never imported or deployed).

1. **Google's trip in testing mode**: the consent screen with `gmail.readonly` unverified; the refresh token with `prompt=consent`; the `scope` string returned, ticked and unticked, and whether a refresh omits it; the seven-day end.
2. **Gmail's history**: `history.list` across label changes, archive, spam, trash, delete forever and a move back from trash; the labels its entries carry; how long a `historyId` stays valid (an idle account read after 1, 7 and 14 days: the day-14 reading lands in week 3, "Calendar"); the 404; `getProfile`'s `historyId` as a baseline; units charged.
3. **Gmail's messages**: `format=metadata` with no headers returns `labelIds`, `internalDate` and `sizeEstimate`; `format=raw` of a 24 MiB message and its `labelIds`; that reading does not mark it read; `messages.list` with `q=after:`.
4. **Microsoft's trip**: a personal account and a work tenant, consent with and without publisher verification, AADSTS codes for admin approval, the token response's `scope` (resource scopes, `offline_access` present or not, omitted or not), the rotated refresh token, the ID token's `oid`, `tid`, `email` and `preferred_username`, and what a 403 or a conditional-access challenge looks like.
5. **Graph's folders and delta**: `GET /me/mailFolders/{well-known name}` for every excluded name, in a localised account; listing continuations; an unfiltered first round of a folder above 5,000 messages; immutable ids across moves to another folder, to junk and to deleted items; `@removed` on a move; `GET /me/messages/{id}?$select=parentFolderId` after each; `syncStateNotFound`, `resyncRequired`, 410; `$value` of a large message; throttling headers.
6. **IMAP on each listed host**: login with an app password, `SPECIAL-USE`, `CONDSTORE`, `UIDVALIDITY` stability, `EXAMINE` and `BODY.PEEK[]` leaving `\Seen` unset (seen on the wire), connection limits per account and per address, folder names without `SPECIAL-USE`. A host that fails leaves the list.
7. **go-imap v2**: the pinned version's transcript for read-only select, `CONDSTORE` and a streamed literal.
8. **HTTP versions**: which of the token, keys, Gmail and Graph endpoints negotiate `h2`, for the policy's per-destination version.

**What the owner must create before the spike**:

- **Google Cloud**: a project for Permaily's mail (`permaily-mail`), separate from sign-in's; the Gmail API enabled; the OAuth consent screen: External, Testing, app name, support email, logo, home page, privacy and terms URLs, authorised domain `permaily.com`, developer contact; scopes `openid`, `email`, `.../auth/gmail.readonly`; test users: only the owner's synthetic test accounts; a Web client with redirect URIs `https://app.permaily.com/api/v1/mailboxes/connect/callback` and `http://localhost:8443/api/v1/mailboxes/connect/callback`.
- **Microsoft Entra**: an application registration for "Accounts in any organizational directory and personal Microsoft accounts"; platform Web with the same redirect URIs; a client secret (24 months at most, its end in docs/operations.md); delegated `openid`, `email`, `offline_access`, `Mail.Read`, no application permissions; publisher verification; terms and privacy URLs.
- **IMAP test accounts** at each listed host, with two-factor on and an app password each.

## Tests

T for this record. Each is a test of the pull request named in brackets ("Implementation pull requests"); a test with two numbers has its translation in the adapter's and its effects in the sync engine's. Played servers: `gmailtest`, `graphtest` (HTTPS with certificates for the real names under a test CA, scripted) and `imaptest`. "Two enclaves" means two services over one fake store with a controllable interleaving (`providersynctest.Cut`), and "stopped-worker replay" means a worker paused at a named write and resumed after the other acted. Every pull request passes `make check` on its merge base.

- **T1** [1] A legacy record without `kind` reads as `forwarding`; a provider record has no address and no pointer; `RCPT TO` of any address never finds a provider mailbox.
- **T2** [1] Making a provider mailbox with four mailboxes through A and B at once: one succeeds, the other `too_many_mailboxes`; ten creations in thirty days counted across kinds.
- **T3** removed (E1): no deployment-wide claim of a provider account.
- **T4** removed (E1): as T3.
- **T5** [1] Disconnecting a provider mailbox: after the terminal commit no enclave opens the grant; the sweep deletes every key of T40; the same provider account can connect again at once.
- **T6** [1] The account's removal ends a provider mailbox as ADR 0026's M20; a step in flight writes nothing after `IngestDeadline` from its reads.
- **T7** [3] The trip (revised, E11): start sets `__Host-permaily-mailconnect` with `Path=/`, no `Domain`, `Secure`, `HttpOnly`, `SameSite=Lax`, checked by a cookie jar that applies RFC 6265bis's prefix rules, and the entry; the callback without the cookie, with another browser's, with a used state, after ten minutes, with a state never made, or malformed: nothing made, the cookie cleared with the same attributes, no entry left.
- **T8** [3] The exchange with the secret and the verifier; an ID token with another audience, nonce, issuer or an old `iat`: refused.
- **T9** [3] Scopes against the allowlist: Google with `email` or `userinfo.email` accepted, without `gmail.readonly` `scope_not_granted`; Microsoft with `Mail.Read` alone, with `https://graph.microsoft.com/Mail.Read`, with `User.Read`, with or without echoed `offline_access`: accepted; with `Mail.ReadWrite`, `Mail.Send` or an unknown scope: refused and never stored.
- **T10** [3] Microsoft's `access_denied` with AADSTS65001 or AADSTS90094: `admin_approval_needed`; any other error `provider_failed`; never the provider's text in the redirect or a log.
- **T11** [3] Connecting again with another provider account: `provider_account_mismatch`, the grant unchanged.
- **T12** [3] The callback with no session cookie: the sealed session rechecked and used; with a session of another account: refused.
- **T13** [7] Refresh: a rotated refresh token written to the grant at the version read before the access token is used; a failed write drops the access token and keeps the stored grant; no access token ever written (every write of the fake store checked).
- **T14** [7] (revised, E5) A grant copied from another mailbox (its seal bound to another identifier), or one whose `subject` is not the record's: not used, nothing read.
- **T15** removed (E13): no revocation call.
- **T16** [3] The operator's route: a secret for another client identifier refused; set: offered once providers are open; removed: not offered, its mailboxes `provider_off`, no notice.
- **T17** [7] One message seen through A and B at once: stored once; the claim's name is the occurrence.
- **T18** [7] Replay (revised, E3, E6): a `sync` or `fold#` entry, the grant or a schedule entry put back: at most work done again or a resync, no copy deleted without fresh evidence, nothing stored twice while its claim lives; a claim put back for a message deleted at the provider and restored: not copied again until the claim ends.
- **T19** [7] Every `fold#` entry deleted: the folders start again, nothing stored twice, deletions only with fresh evidence.
- **T20** [7] A step stopped after each write of a message: the next completes it, one message.
- **T21** [7] A step reads the record, then B disconnects: the step's writes, a late grant's included, end within `IngestDeadline`; the sweep's last pass after `SweepGrace` deletes them.
- **T22** [7] (revised, E5) Leases: A takes a mailbox; B skips it; A dies; B takes it after `until` plus `ClockSkew`; A, resumed mid-step, does nothing a fresh step would not: its imports are claims, its deletions read their evidence, its cursor write fails at the version read or moves the cursor back.
- **T23** [7] Fair shares: 1,000 Gmail mailboxes due and one Outlook mailbox: the Outlook one starts within one pass; one account's five mailboxes one at a time; no poll beyond `PollBudget`.
- **T24** [7] (revised, E16) Backoff: `Retry-After: 120` waits 120 s; two days waits two days; ten days waits seven and says `long_retry`; without it 1, 5, 15, 60 minutes with jitter; success returns to the phase before.
- **T25** [7] (revised) A broken grant: `invalid_grant`, a second 401, two polls of `AUTHENTICATIONFAILED`: written `broken` at the version read; through A and B at once one transition and one queued notice; not polled while broken; connecting again resumes from the cursors.
- **T26** [7] Paused not polled; closing, blocked, terms not agreed polled.
- **T27** [4, 7] Gmail: history of added, `SPAM`, `TRASH`, archived, deleted, restored from trash: stored, deleted, deleted, kept, deleted, a new copy.
- **T28** [4] Gmail's precedence: `SENT` without `INBOX` excluded; with `INBOX` read; `DRAFT`, `CHAT` excluded; a user label with `SPAM` excluded.
- **T29** [4, 7] (revised, E8) Gmail: 404 on the history position: a new baseline first, the live stream from it and the enumeration beside it; copies missing added; each held copy not listed checked by its own evidence; a message arriving between the old position and the new baseline stored.
- **T30** [5, 7] Graph: a move between included folders read in both orders and across two polls: one message, its decisions kept; a move to junk: deleted after the `parentFolderId` check.
- **T31** [5] (revised, E9) Graph: excluded folders found by their well-known names, renamed and localised, and their subtrees never read; 201 folders: `partial_folders`, nothing deleted for the 201st; folder listings followed through checked continuations.
- **T32** [5, 7] Graph: `syncStateNotFound`, `resyncRequired`, 410 on a folder: that folder resyncs; others untouched.
- **T33** [6] IMAP: the command writer refuses everything outside its allowlist before it is sent; a full poll's transcript holds only allowed commands; `\Seen` never set on the played server.
- **T34** [6] IMAP: TLS with a certificate for another name, self-signed, TLS 1.1, or `STARTTLS` asked on 993: refused, no password on the wire.
- **T35** [6] (revised, E14) IMAP bounds: a line of 65 KiB, a body literal announced at 26 MiB (refused before reading), a non-literal answer past 1 MiB, nesting 17, 10,001 tokens, 1,001 unsolicited responses, a silent command: the session closed, `bounds` or `timeout`, the allocator under the preflight.
- **T36** [6, 7] (revised, E10) IMAP moves: a move between folders is a deletion and a new copy, nothing carried; two equal copies in one folder are two messages, deleting one leaves the other; a `UIDVALIDITY` change deletes the folder's old copies and copies its messages again under the admission rule.
- **T37** [6, 7] (revised, E10) IMAP presence: each `UID SEARCH UID` completed with its tagged `OK` under an unchanged `UIDVALIDITY` is evidence for its own UIDs only; a failed or cut command is evidence for nothing; deletions found within the hour, with and without `CONDSTORE`.
- **T38** [6] IMAP without `SPECIAL-USE`: the pinned names excluded, modified UTF-7 decoded first.
- **T39** [6] Fuzz: the wrapped reader and decoder over arbitrary bytes.
- **T40** [1, 7] (revised) Every key a provider mailbox's life writes in its partition (`grant`, `sync`, `fold#`, provider `seen#`, the copies) is deleted by the sweep; its schedule entry by the poller or the repair once the record has ended, its due minutes by expiry; a forwarding mailbox's sweep unchanged.
- **T41** [7] (revised, E22) History: 0 days imports nothing dated before `connected_at` less the margin; 30 and 90 days import within the window, never clamped; a message dated outside it not imported; the bounds end the backfill with its counts; live mail stored meanwhile.
- **T42** [7] (revised, E22) Identifiers and origin: every identifier from when the message was copied; `Origin: live` for the live stream or a provider date at or after `connected_at`, `history` otherwise.
- **T43** [7] Over 25 MiB not stored and counted, by known size (not fetched) and by bytes read when the size lied or was absent; the cursor moves past it.
- **T44** removed (D13): deletions are no longer held to the head of a poll.
- **T45** [9] `cmd/permaily-host`'s `defaultEgress` holds every destination of `egress.Policy`.
- **T46** [9] (revised, E19) Nothing of a refresh token, access token, app password, address, username, subject or provider error text reaches a log line or an answer, over a full composed run: the callback, the `CONNECT`, the transport, the adapters and the sync.
- **T47** [7] (revised, E14) Memory: an import reserves its worst case from `ProviderBytes` before allocating; with the pool held the step ends `busy` within ten seconds, nothing half stored; SMTP's budget untouched.
- **T48** [7] (revised, E16) Quotas: Gmail units debited by method and format, never past 2,400 a minute a mailbox in one enclave, live before import; `dailyLimitExceeded` holds that enclave's Gmail polls until the next hour.
- **T49** [8] (revised, E24) Web: the add screen offers the three provider kinds only when providers are open, together, and only those whose secret is set; the history choices that fit the retention.
- **T50** [8] Web: the IMAP form and the reconnect form against the contract; the password never echoed or kept.
- **T51** [8] (revised) Web: a provider mailbox's page in every state, `partial_folders`, the counts of the import, "Connect again", the disconnect's words for each kind.
- **T52** [8] Web: the callback's fixed reasons with their words; an unknown one as `provider_failed`.
- **T53** [9] (revised) `/status`'s `providers` with every key and nothing else, no count; `permaily-verify` fails on each of its failure conditions, one at a time, and on a missing, extra or duplicate egress entry.
- **T54** [9] (revised, E12, E19) `TestMailEndToEnd`'s provider half through two dev enclaves and the played servers: with an image open and every use open (test only): connect, import 30 days, a new message stored and its webhook notice sent, a deletion mirrored, a rule holding a message, a connection's read, disconnect; with a dormant image: no connection possible.
- **T55** [3] Connecting starts only for an open, agreed account with a free slot, and checks again before the commit.
- **T56** [7] (revised, E17) The repair: within the hour remakes a deleted schedule entry and due minute from the index; the person's list remakes one whose index entry was never written; drops one whose mailbox ended; queues a broken grant's notice not yet queued.
- **T57** [2] (revised, E15) The provider transport: the policy's HTTP version per destination, a 302 an error, bodies past their bounds refused, 30 seconds a call, a `CONNECT` reply over 4 KiB refused, nothing sent before the handshake ends.
- **T58** [7] A provider whose secret was removed: `provider_off`, no break, no notice.
- **T59** [7] Notices: "a mailbox was added" names the kind only; "can't read one of your mailboxes" holds no label, address, domain, link or code; its key is the break's.
- **T60** [3] The IMAP connect: one login tried before anything is made; a failure makes nothing; the password never in the answer, a log or the trip entry. The adapter is a fake satisfying `providers.IMAPLogin` until pull request 6 merges, then the real adapter in pull request 9's composition.
- **T61** [1, 7] (revised, E6) A person's delete of a provider message through either delete route: the copy and its kept decisions gone, the claim kept, nothing sent to the provider, not copied again on the next poll or resync.
- **T62** [9] The staging check: the owner's synthetic Google test account, a Microsoft personal and a work account and one IMAP account of each listed host connected to a staging image with every use closed; history imported; a new message within ten minutes; a deletion and a move mirrored; disconnect and its words.
- **T63** [7] Each mailbox polled every 300 to 330 seconds over an hour with two enclaves, never twice in one interval; after one enclave dies, its mailboxes polled by the other within `LeaseFor` plus `ClockSkew`.
- **T64** [1] (revised, E1) Provisioning stopped after each step (reservation, record, settings and index, grant, schedule): before the record nothing but a reservation ADR 0026's settler fences; after it an active mailbox shown "Connect again" until its grant is written, counted, removable; never a grant without a record.
- **T65** removed (E2): no compatibility release.
- **T66** removed (E2): as T65.
- **T67** removed (E2): as T65; T118 covers what a dormant release keeps.
- **T68** [9] (revised, E2) Rollback from an open image to a dormant one on staging with provider mailboxes, then forward again: nothing lost or rewritten; while dormant nothing polled or connected, nothing of them sent to the model or a vendor, the person's list, read, download, delete and disconnect working.
- **T69** removed (E1): no deployment-wide claim.
- **T70** [1] Account removal during provisioning: no mailbox appears.
- **T71** [9] (revised, E24) A production image with providers dormant answers `providers_closed` to every connect route, imports nothing, offers no provider kind.
- **T72** [3] A consumed trip replayed after a restart through either enclave: nothing made; concurrent callbacks for one state: one consumer.
- **T73** removed (E13): no revocation.
- **T74** [7] An IMAP session open across a disconnect is closed by its step's deadline.
- **T75** [2] Through a permissive proxy: an off-host, off-port, off-path or off-method request, and a Graph continuation pointing elsewhere, are refused before connecting and before a header is attached; the proxy sees nothing.
- **T76** [7] (revised, E3, E6) Stopped-worker replay at each write of a poll (cursor, claim, raw, item, mark, deletion, the claim's deletion) with the other enclave taking over: no copy deleted without evidence, none stored twice while its claim lives.
- **T77** [7] (revised, E5) Connecting again while a request with the old token is in flight: the old worker's grant write fails at the version read; it marks nothing broken; what it imports is the same account's mail.
- **T78** [7] A schedule entry deleted and remade during a poll: at most one poll done twice.
- **T79** removed (E3): no strict sync record.
- **T80** [4, 5, 6, 7] (revised, E8) With 0 days, a message arriving between connecting and the first poll, or during Graph's first round, is stored as new mail, for each provider.
- **T81** [9] Memory on staging: two raw imports, eight pages, four IMAP sessions with hostile but in-bound answers and a full SMTP budget at once stay under the runtime's bound, measured.
- **T82** [1] (revised, E12) A provider mailbox with its vendor use closed: `OpenForConnection` refuses; a connection's search, read and attachments over the API and MCP, and the consent preview, show nothing of it, the read recording it withheld.
- **T83** [1] (revised, E12) Webhooks: candidate selection skips a provider mailbox whose vendor use is closed, and work queued while it was open is not sent once closed, because the recheck before each send opens through `OpenForConnection`.
- **T84** [1] (revised, E12) With the model use closed: a search or matches with `about` or `mentions`, rules' criteria preview and the removal preview open with `AccessModel` and are refused before any model call; the person's own list, read, download, plain search and delete work.
- **T85** [1] `Import` with a provider ingress charges no SMTP quota and reads no forwarding confirmation.
- **T86** [1] (revised, E22) A provider claim lives until its copy's expiry plus a day; retention counts from the copy; a listing does not admit a message dated before now less the retention, or with no date or a future one.
- **T87** removed (E6): no identity rows.
- **T88** removed (E6): no reverse lookup.
- **T89** [1] (revised, E7) A mirrored deletion removes the message's `dec#` as a person's delete does; its strict hold, if any, is dropped by its block's next write an hour after the copy's expiry, as ADR 0031 drops any.
- **T90** removed (E7): identifiers are never before the mailbox's creation, so ADR 0026's walk of decision days holds unchanged.
- **T91** [4, 7] (revised, E8) Gmail: the cursor moves to each history record once its effects are done; a cut resumes after the last record done; a page larger than a step's budget, and a stream advancing faster than one step, still reach the head.
- **T92** [5, 7] Graph: a folder of 6,000 messages read completely by the unfiltered first round.
- **T93** [7] (revised) A resync cut by a bound or a step's end resumes from its saved page; checks of copies not listed start only once its enumeration completes, and each deletion has its own evidence; a reset during a resync starts from a new baseline.
- **T94** [4, 7] Gmail `format=metadata` without `sizeEstimate` or `internalDate`: unknown, not zero; the bytes bound holds.
- **T95** [5, 6, 7] (revised) Folder discovery truncated, then complete: nothing deleted while truncated; an IMAP folder missing from one complete `LIST`, then from a second an hour later: its copies deleted only after the second.
- **T96** [6] IMAP `UID FETCH` with an empty interval not sent; an out-of-interval UID in a response refused.
- **T97** [7] (revised, E19, E22) Webhooks with a test-only open policy: a vendor connected during an import gets no notice for history; live mail, delayed mail and mail dated after connecting found by a resync notify; an arrival whose hint was lost is found by the late scan.
- **T98** [7] Due index: an entry inserted behind the scan's minute, a full ready queue, a minute rollover, two-enclave saturation, a dead lease and repair during backoff: each mailbox polled within its interval plus one pass.
- **T99** [7] (revised, E16) Quotas with two enclaves: each keeps its fixed budget; together under the provider's per-user limit.
- **T100** removed (E16): the project gate is per enclave (T48).
- **T101** [7] (revised, E17) A grant written `broken`, then the worker dies before queuing: the next finder queues it once with its own time; a crash after queuing and before `noticed` queues the same key again and the queue sends it once.
- **T102** [7] Gmail's weekly expiry across two enclaves with a lost cursor: connect again, then a 404 history, then a safe resync.
- **T103** [3] (revised) IMAP reconnect replaces the app password in the grant at the version read and keeps the cursors.
- **T104** removed (E18): no counts on `/status`.
- **T105** [7] Logs at source: an app password typed into the username field, a URL with a token in an error: neither in any log line.
- **T106** [9] (revised, E19) Egress policy equality: the verifier's pinned copy and `egress.Policy` compared field by field, in the pull request that holds both.
- **T107** [9] Reproducible builds of the dormant release on both architectures with go-imap pinned (`nix build .#eif` equal on x86_64 and arm64).
- **T108** [9] Quota soak on staging: 100 played Gmail mailboxes and 300 Graph and IMAP mailboxes for 24 hours under the budgets, no provider 429 unanswered, one vCPU within budget, the store's writes counted (no strict commit from polling).
- **T109** [9] A seven-day token expiry and reconnect cycle on the owner's synthetic Gmail test account.
- **T110** [9] Real routes and callbacks on staging for Google and Microsoft with synthetic mail, including a refused scope, an admin-approval error and a subject not on the test list.
- **T111** removed (E6): no suppression blocks; a person's delete keeps the claim (T61).
- **T112** [7] A grant refresh answered outside the allowlist: `broken`, nothing stored.
- **T113** [3] (new, E11) Two trips started in one browser: the second's cookie replaces the first's; the first's callback answers `provider_failed`, the second's succeeds.
- **T114** [3] (new, E11) The final standing check: a sign-out, a terms change or a full account landing before the read immediately preceding the record's commit makes nothing; one landing after it leaves the mailbox made and disconnectable; connecting again needs no free slot.
- **T115** [3] (new, E21) Token responses with `scope` omitted (the requested set at the first exchange, the stored set at a refresh), empty (refused), narrowed below the must-hold set (refused) or broadened (refused); `oid` or `tid` missing: refused; a refresh with no new refresh token keeps the stored one; `invalid_grant` writes `broken`.
- **T116** [1, 9] (new, E12) Every route on the mux and every MCP tool, over a provider mailbox in an image with model and vendor uses closed, a model that fails on any call and a vendor egress that fails on any request: nothing asked, nothing sent; a route added without a case in the test fails it. [1] runs it over the routes and services of its time, [9] over the composed image.
- **T117** [3] (new, E12) Staging: a provider identity not in the image's test subjects is refused after the exchange and before any mail API request; nothing stored.
- **T118** [1] (new, E2) A dormant image over a store with provider mailboxes made by an open one: its pause, label change, list writes, disconnect and account removal keep every provider field and entry; it polls and connects nothing.
- **T119** [5] (new, E9) Graph classification by opaque id and ancestry: a folder whose parent is not listed is not imported and gives no evidence; a folder moved under deleted items deletes its copies only through each message's own evidence.
- **T120** [6, 7] (new, E10) IMAP: A expunged and an identical B appended in the same poll: A's copy deleted, B a new copy, nothing carried from A.
- **T121** [1] (new, E6) A claim whose item was written, its worker stopped before the mark, the message then deleted by the person: the takeover finds the deletion mark and writes nothing; the copy stays deleted; the same for a forwarded message.
- **T122** [1, 7] (new, E6) A mirrored deletion stopped between the copy and its claim: the next evidence or resync for that message finishes it; a message restored at the provider after the claim is gone is copied again as a new message.
- **T123** [7] (new, E14) A Gmail or Graph page past 4 MiB asked again at a smaller page size; an IMAP range past 1 MiB halved and resumed; a `sync` or `fold#` value past its bound refused by its codec, never written.
- **T124** [2] (new, E15) Through a permissive proxy, each HTTPS destination and each IMAP host answered by a server with a valid certificate for another name, an untrusted one, and the right one: only the right one gets a byte after the handshake; the ID-token keys fetched through the provider egress.
- **T125** [1] (new, choice 3) Connecting a provider account already connected in the same Permaily account: `provider_account_in_use`; two such connections at once may both succeed, and both work.
- **T126** [7] (new, E17) A healthy poll releases its lease with the write that sets its next due; the other enclave then polls it at that due, not a lease later.
- **T127** [7] (new, E16) A schedule entry lost during backoff: at most one early request, answered again by the provider's `Retry-After`.
- **T128** [10] (new, E2, E24) The opening release: `/status` says `open: true` and the uses compiled from the determination; `permaily-verify` fails on an image whose `open` or `uses` differ from its pinned determination, and on a dormant image that says `open: true`.

**Matrix of invariants**: each invariant this record relies on, the lowest layer that has the authority to keep it, and its adversarial tests.

| Invariant | Layer | Tests |
|---|---|---|
| No provider mailbox before the opening release; the three kinds open together | The image (`providers.open`), `services/mailboxes`, the routes | T49, T71, T118, T128 |
| A rollback lands on a release that keeps provider data and closes its uses | `services/mailboxes` from the floor on; the operator's key removal | T68, T118 |
| Provider content reaches no closed model or vendor path | `mailboxes.Service.Open` and `OpenForConnection`'s access table | T82 to T84, T116, T54 |
| A mailbox is published only by its record's commit; nothing of the provider before it | `services/mailboxes` (ADR 0026's creation) | T64, T70, T114 |
| A session ended before the final check cannot connect a mailbox | the trip, `services/accounts` seam | T12, T72, T114 |
| No copy deleted without fresh evidence about that message | adapters translate; `providersync` decides | T18, T22, T27, T29 to T32, T36, T37, T76, T93, T95, T119, T120 |
| A message held is stored once; a person's delete holds | ADR 0026's claim with the copy's lifetime and the takeover's mark check | T17, T20, T61, T86, T121, T122 |
| Stored sync state can be lost or replayed without harm beyond work | `providersync` (cursors as hints) | T18, T19, T76, T78 |
| Only the current grant is written; a broken grant is noticed once | the grant entry at the version read | T13, T14, T25, T77, T101 |
| Egress cannot be widened by the host | `providers/egress` | T75, T106, T124, T53 |
| Memory peaks are admitted before allocation | `providersync`'s pool, the IMAP wrapper | T35, T47, T81, T123 |

## Implementation pull requests

Ten, then the opening release's pull request once the owner's gate is passed. Each merges onto a green, buildable `develop`; pull request 0's seams and fakes let 1 to 8 be built at once; a pull request merges only after those it integrates with ("Depends on").

| # | What | Owns (may edit) | Depends on | Tests | Engineer-weeks |
|---|---|---|---|---|---|
| 0 | **The contract and the seams**: this record as re-reviewed; `internal/contracts/mailboxes.go` (`MailboxKind`, the mailbox's provider fields, `ConnectStart`, `ConnectIMAP`, `ReconnectIMAP`, the fixed reasons) and `web/src/lib/api.ts`'s mirror; `internal/providers/providers.go` (`Source`: `Baseline`, `Live`, `Enumerate`, `Evidence`, `Raw`; `Occurrence`, `Page`, `Outcome`, `IMAPLogin`, `Tokens`), `providerstest.Contract`; `mailboxes.Importer`, `GrantStore` and `AccessModel`; `providersync`'s types and `providersynctest` (fake store with `Cut`); `egress.Policy`'s type; `attestation.ProvidersStatus`; `deploy/enclave.json`'s `providers` keys with `open: false` | `internal/contracts`, `internal/providers` (interfaces, `providerstest`), `internal/services/providersync` (types, fakes), `internal/services/mailboxes` (types only), `pkg/attestation` (type), `deploy/enclave.json` (keys), `web/src/lib/api.ts` | The re-review | Compiles; Go and TypeScript shapes compared; the codecs' bounds table-tested | 1 |
| 1 | **The floor: the record, storing and the gate**: the kind and provider fields, provisioning on ADR 0026's creation, the grant entry, `Import` with the provider ingress, provider claims and their lifetime, the takeover's mark check, the mirrored deletion's helper, sweep prefixes and disconnect for provider kinds, the access table's column and `AccessModel` in the routes that may ask the model, settings and retention for provider kinds | `internal/services/mailboxes`, `internal/inbox` (`deliver.go`, `StoredMeta.Source` and `Origin`), `internal/httpapi/mailboxes.go` and `rules_preview.go` (their `Access` only), tests in `internal/services/connections`, `internal/services/webhooks` and `internal/httpapi` | 0 | T1, T2, T5, T6, T40, T61, T64, T70, T82 to T86, T89, T116, T118, T121, T122, T125 | 2 |
| 2 | **The egress**: `internal/providers/egress` (policy table, HTTP transport, IMAP dialer), the vsock dial reused from `internal/enclave`, `defaultEgress` | `internal/providers/egress`, `internal/enclave`, `cmd/permaily-host` | 0 | T57, T75, T124 | 1 |
| 3 | **OAuth as a client, and the routes**: the trip, the session seam, the exchange, scopes, the operator's route and the providers' applications record's seam, the IMAP connect and reconnect routes, the staging test subjects | `internal/providers/oauth`, `internal/httpapi/mailboxes_connect.go`, `internal/httpapi/crosssite.go`, `internal/httpapi/mailboxes.go` (mount), `internal/services/accounts` (export `sessionOpen`), `internal/idp` (export the ID-token check without the `sub` fallback, its key fetch given a transport), `scripts/provider.sh`, `cmd/permaily-enclave/main.go` (admin mount only) | 0, 2 | T7 to T12, T16, T55, T60, T72, T103, T113 to T115, T117 | 1.5 |
| 4 | **Gmail's adapter** and `gmailtest` | `internal/providers/gmail` | 0, 2, the spike's items 1 to 3 | T27 to T29, T80, T91, T94 (translation), the contract | 1 |
| 5 | **Graph's adapter** and `graphtest` | `internal/providers/graph` | 0, 2, the spike's items 4, 5 | T30 to T32, T80, T92, T95, T119 (translation), the contract | 1 |
| 6 | **IMAP's adapter**: the closed list, the wrapped connection, the command writer, go-imap pinned, `imaptest`, the fuzz group | `internal/providers/imap`, `scripts/fuzz.sh`, `go.mod`, `go.sum`, `nix/go.nix` (`vendorHash`), `scripts/third-party-notices.ts` and the image inventory | 0, 2, the spike's items 6, 7 | T33 to T39, T80, T95, T96, T120 (translation), the contract | 1.5 |
| 7 | **The sync engine**: due index, lease, steps, cursors, imports, evidence and deletions, resyncs, backfill and its admission rule, quotas, backoff, the pool, the repair, notices through the queue and their templates | `internal/services/providersync`, `internal/services/notices` (kinds), `packages/mail` (the two templates) and `internal/domain/mail/templates` (built) | 0, 1 (merged); adapters through `providerstest` | T13, T14, T17 to T27, T29, T30, T32, T36, T37, T40, T41 to T43, T47, T48, T56, T58, T59, T61, T63, T74, T76 to T78, T80, T91 to T95, T97 to T99, T101, T102, T105, T112, T120, T122, T123, T126, T127 | 2.5 |
| 8 | **Web**: adding by kind, the trip's hand-off, the IMAP and reconnect forms, a provider mailbox's page and states, disconnect words, the inbox's delete words; docs/web.md | `web/src/pages/mailboxes/`, `web/src/pages/inbox/`, `web/src/lib/`, docs/web.md | 0 | T49 to T52 | 1 |
| 9 | **Composition, documents and the dormant release**: `cmd/permaily-enclave/providers.go`, `/status`'s `providers`, `cmd/permaily-verify` with its pinned policy and determination, `TestMailEndToEnd`'s provider half and the real store's composition; docs/threat-model.md, docs/data.md (inventory rows and lifetimes), docs/legal.md, docs/operations.md (secrets, Microsoft's rotation, Google's test users, the floor and the opening checklist), docs/verification.md, docs/acceptance.md (the IMAP release matrix), the privacy page and terms for the owner's review (`site/`); the staging checks | `cmd/permaily-enclave`, `cmd/permaily-verify`, `pkg/attestation` (check), docs, `site/` (legal pages) | 1 to 8 | T45, T46, T53, T54, T62, T68, T71, T81, T106 to T110, T116 | 1.5 |
| 10 | **The opening release** (after the owner's gate and the barrier): the determination as a dated section of this record, `providers.open: true`, `providers.uses` per kind, the verifier's pinned determination, docs/acceptance.md | `deploy/enclave.json` (`providers`), this record, `cmd/permaily-verify` (pin), docs | 9 deployed; "Launch: one barrier" | T128 | 0.25 |

### Calendar

One engineer-week is five working days of one person. The table sums to 14.25 engineer-weeks, plus the spike's 1: about 15.25, against the previous revision's 17.5; these are estimates of the rows, without the external waits below. With three people (P1, P2, P3), by weeks from the re-review's approval:

| Weeks | P1 | P2 | P3 | Owner and waits |
|---|---|---|---|---|
| 0–1 | Pull request 0 | | | Registrations (Google project, Entra application, IMAP test accounts); publisher verification may take weeks and does not block personal accounts |
| 1–2 | Pull request 1 | The spike | Pull request 2 | |
| 2–3 | Pull request 1 | Pull request 4 (Gmail) | Pull request 3 | The spike's day-14 Gmail reading lands in week 3; it tunes, and does not block, pull request 4 |
| 3–4 | Pull request 7 | Pull request 5 (Graph) | Pull request 3, then 6 | |
| 4–5 | Pull request 7 | Pull request 8 (web) | Pull request 6 (IMAP) | |
| 5–5.5 | Pull request 7 | Reviews, staging set-up | Pull request 6 | |
| 5.5–7 | Pull request 9 | Staging checks | Staging checks | The seven-day token cycle (T109) from the first composed staging image (week 5.5) to week 6.5 |
| 7 | The dormant release (pull request 9) deployed | | | The floor's checklist: keys before F removed (the owner, `tofu apply`) |
| Gate | Pull request 10, a day | | | The owner's legal determination; the opening release only then |

The critical path is P1's: 0 → 1 → 7 → 9, 1 + 2 + 2.5 + 1.5 = 7 weeks to the dormant release; pull request 7 integrates the adapters as they merge (the last, IMAP, at week 5.5) and pull request 9 starts when 1 to 8 have merged. With one person, about fifteen weeks. The opening release waits on the owner's gate, outside the calendar.

## Risks

- **The legal gate may not open**, or may open only for some uses: then no provider opens (decision 13), or provider mail opens with some uses closed, readable in the app and kept from what the determination closes.
- **Google's seven-day refresh tokens in testing mode** make every Gmail mailbox reconnect weekly.
- **Microsoft publisher verification** may take time; without it many work accounts cannot consent.
- **IMAP providers change** their app-password rules and limits; Permaily's single address may be throttled.
- **go-imap v2 is beta**: the wrapper's preflight and the command writer hold what it may do wrong.
- **Quotas are published, not contracted**: Google and Microsoft may lower them; the budgets keep Permaily under them at the cost of delay.
- **Resyncs are costly when they come**: a Gmail resync spends hours of one mailbox's budget on evidence; rare by design.
- **IMAP moves and renames lose decisions** (choice 20), and IMAP deletions take up to an hour to mirror (choice 30).
- **Disconnecting does not revoke at the provider** (choice 21): a person who does not remove Permaily there leaves a token Google still lists, unreadable to anyone.
- **Mirrored deletions are a way to remove evidence** from Permaily's copy; receipts stay.
- **Five minutes is not instant.**

## What remains to verify

- Every provider fact marked as documented (lifetimes, quotas and costs, the `format=metadata` and `format=raw` fields, the history entries' labels, the well-known folder lookups, `CONDSTORE`, Graph's unfiltered first round and its continuation lifetime, `historyId`'s life, HTTP versions), in the spike.
- The memory arithmetic under SMTP and every provider class at once (T81).
- That 100 Gmail mailboxes and a few hundred Outlook and IMAP mailboxes fit the enclave's one vCPU and the store's write budget (T108).

## Options considered

- **Push instead of polling**: declined by the owner (decision 5).
- **A strict sync record per mailbox, in epochs of 1,000 versions, holding the run's fence, the grant's epoch, every folder's checkpoint digest and a deletion journal** (the previous revision): rejected after the second review (E3, E4, E5). Its rollover had no single linearisation point, its cold read outgrew the store's 500-object fetch bound, and it fenced effects that, once deletions need their own evidence, need no fence.
- **Occurrence identity rows with versioned states, a reverse lookup and strict suppression blocks** (the previous revision): rejected (E6). ADR 0026's claim, living as long as the copy, does the same work with one entry and no new state machine.
- **A deployment-wide claim of a provider account** (both earlier revisions): rejected (E1); uniqueness is per Permaily account, best effort.
- **A compatibility release before providers** (the previous revision): rejected (E2); every release from the floor knows provider data, and providers stay dormant until the opening release.
- **Google revocation with a fenced retry and a 24-hour reconnect block** (the previous revision): rejected (E13); a late revocation ends a new grant whatever is stored, and no stored row can serialise it with Google.
- **Forgetting a mirrored deletion's strict decision within a day by a sweep** (the previous revision): rejected (E7); a mirrored deletion is a deletion like a person's, and ADR 0031's holds end with the copy's retention.
- **Carrying an IMAP message's identity across folders by a content digest** (both earlier revisions): rejected (D6, E10); digest names bytes, not messages.
- **Backdating identifiers to the provider's date** (both earlier revisions): rejected (D9, E7); it put decisions before the mailbox's creation.
- **A per-message source policy** (the previous revision): replaced by the access table's column (E12): a mailbox's kind decides its source, and every path opens a mailbox.
- **Bucketed counts on `/status`** (the previous revision): rejected (E18); thresholds still leak single events.
- **Applying deletions at the end of a complete poll by elimination** (the first version): rejected (D8, D13).
- **Graph's delta with `$filter=receivedDateTime ge`**: rejected (D7); it caps at 5,000 messages.
- **The host's `CONNECT` allowlist as the egress boundary** (the first version): rejected (D12); the host is untrusted.
- **The grant under the root secret**: rejected; a grant must die with its mailbox's key.
- **The vendor egress for providers**: rejected; it reaches any public name.
- **Arbitrary IMAP servers**: declined by the owner (decision 7).
- **Storing only references and reading the provider on demand**: declined by the owner (decision 3).

## Changed after the second design review (2026-11-27)

The adversarial re-review of the revision at `e9210a0` (Codex) found 15 P1, 8 P2 and 1 P3, concluded "needs a redesign", and counted D1 to D26 as 2 resolved and 24 partly resolved. Its main point was that the revision added mechanisms (provisioning claims, sync epochs, occurrences, revocation entries, decision forgetting, a compatibility release) and each opened state-machine gaps. The owner then added decisions 13 and 14. This revision does not patch those mechanisms; it removes them. One rule replaces most of them: no stored state authorises a deletion, so stored sync state needs no fence ("Context", "Effect boundaries"). Every finding was checked against the code it cites.

**Removed**: the provider account's claim and its states; the strict sync record, its epochs, hint, run generation and grant epochs; folder checkpoint digests; the deletion journal; occurrence identity rows, `msgocc#` and their transitions; suppression blocks; the revocation entry, the revoker and the reconnect block; the decision-forgetting sweep and `decisions_since`; backdated identifiers; the content-digest move; `OBJECTID`; the compatibility release; `internal/admission`'s move out of `smtpd`; the per-message source policy; bucketed counts; the deployment-wide quota split's coordination. **Weakened, stated**: uniqueness per Permaily account only (choice 3); no revocation at the provider (21); IMAP moves lose decisions (20); IMAP deletions within an hour (30); a mirrored deletion leaves the strict hold to expiry (29); history listed by copy time (28); a sign-out in the last moment does not undo a connection (32); a person's delete holds under honest storage (11). Tests: T3, T4, T15, T65 to T67, T69, T73, T79, T87, T88, T90, T100, T104 and T111 removed; many revised; T113 to T128 added. Pull requests: thirteen became ten and the opening one; 17.5 engineer-weeks became about 15.25.

### The new findings

1. **E1 · Provisioning could publish a mailbox whose claim or repair state was missing (P1). Accepted; fixed by removing the claim.** Confirmed: the tombstone answers `ErrExists` once the record won, and the index is written after the record (`lifecycle.go`, `make`). Provisioning is now ADR 0026's creation with no address; nothing of the provider is written before the record's `CreateNew`, the one commit; after it, the grant and schedule are written inside `IngestDeadline`, and a crash leaves an honest "Connect again" mailbox. There is no deployment-wide claim to take over; uniqueness is per Permaily account and best effort (choice 3). The IMAP identity is the host's identifier and the lowercased username, with no claim that aliases are recognised. A missing schedule is remade by the repair or the person's list ("Provisioning", "One provider account, one mailbox per account"; T64, T114, T125).
2. **E2 · Release A's rollback and KMS retirement left incompatible runtimes with access (P1). Accepted; one barrier replaces release A.** Production hands over on one instance and terminates the old enclave (docs/operations.md, "Deploy a release"); the floor is the first release with provider code, dormant; the opening release requires the keys of every release before the floor removed (unusable from the apply, which is the condition named, not the deletion seven days later), a running release at or after the floor, and the legal determination. Rollback from the opening release lands on a dormant release that keeps everything and closes every use, with the person's own reads working. What is the operator's fact (no pre-floor enclave still running) is said ("Launch: one barrier"; T68, T118, T128).
3. **E3 · Epoch rollover admitted a stale successor and lost the tail (P1). Accepted; removed.** There is no strict sync record and so no rollover: cursors are sealed entries whose replay only causes work ("Records and layout"; T18, T76; T79 removed).
4. **E4 · A 1,000-version epoch exceeded the strict store's cold-read budget (P1). Accepted; removed.** Confirmed against `internal/store` (`MaxHistoryReads` 500, `maxCommitObjects` 10,000, 16 fetches, 100,000 cached objects). Polling now writes no strict record: a quiet poll writes only its schedule entry twice (take and release), DynamoDB items; the mailbox record, read strictly at each step, changes at most a few times a month, so its history stays short ("Records and layout", "The poller"; T108 counts the writes).
5. **E5 · Grant changes left a live window for old-run effects (P1). Accepted; the window is made harmless.** The grant is one entry written only at the version read, so a stale worker can neither put back an old token nor mark a new grant broken; whatever it imports is the same account's mail through claims; nothing it publishes is authorised by stored state. Pauses, a secret's removal and disconnects stop new steps; a step in flight may finish its reads and imports within `IngestDeadline`, as a delivery in flight does ("Tokens", "States"; T14, T22, T77).
6. **E6 · Occurrences and the reverse index did not fence visibility or deletion (P1). Accepted; replaced by ADR 0026's claim.** Confirmed: both delete routes call `Reader.Delete` and the writer never reads the mark (`deliver.go`, `read.go`). There are no identity rows, no `msgocc#`, no suppression blocks: the claim, living as long as the copy, is the only per-message state; a person's delete through either route keeps it, a mirrored deletion deletes the copy then the claim, and a claim taken over reads the deletion mark first, which also closes the same window for forwarded mail. A stopped mirrored deletion is finished by the next evidence; its one gap (a restore before the claim goes) is stated ("Storing", "Deletions"; T61, T121, T122; T87, T88, T111 removed).
7. **E7 · The decision-forgetting sweep could miss its deadline and reopen a hold (P1). Accepted; removed.** Confirmed: ADR 0031 keeps a decision an hour past its message's expiry and a person's delete does not forget it. A mirrored deletion now does exactly what a person's delete does: `dec#` goes with the copy, and the strict hold stays to the copy's expiry, holding nothing; its old versions stay in immutable history as for any decision. Identifiers are never backdated, so ADR 0026's walk of decision days from creation holds and no lower bound is carried through removal (choice 29; T89; T90 removed).
8. **E8 · Gmail pagination could stall; Graph and IMAP could miss connect-time mail (P1). Accepted.** Gmail's cursor moves per history record once its effects are done, so any cut resumes past finished work; a resync keeps its page token. `connected_at` is taken before any baseline, and one admission rule by provider date with a ten-minute margin covers every folder's handoff, folders discovered later and resets ("Bootstrap", "Per provider"; T80, T91, T93, T122).
9. **E9 · Graph egress omitted the lookups needed for folder exclusions (P1). Accepted as proposed.** `GET /v1.0/me/mailFolders/{id}` is in the policy for the well-known names; roles come only from those lookups by opaque id; folder listings' continuations are checked against their route; a folder with incomplete ancestry is `unknown`, never imported and never evidence ("Egress", "Folders"; T31, T119).
10. **E10 · An IMAP content match could transfer the wrong message's decisions (P1). Accepted; the match is removed.** A move is a deletion and a new copy; presence evidence is per completed `UID SEARCH` command under the `UIDVALIDITY` the claims were made under, never a snapshot across commands; a `UIDVALIDITY` change is evidence for the old claims ("Per provider", "Deletions"; choice 20; T36, T37, T120).
11. **E11 · The trip cookie was rejected and the session check preceded the effect (P1). Accepted.** Confirmed: `__Host-` requires `Path=/`, as `internal/httpapi/accounts.go` already sets. The cookie now follows that pattern, is checked before the state is consumed and cleared with the same attributes; one trip per browser; the standing check is the last read before the record's commit, and a sign-out after it does not undo what it made, which is the same rule a forwarding creation has (choice 32); connecting again needs no free slot ("The trip"; T7, T113, T114).
12. **E12 · The source policy missed direct model callers and final vendor delivery (P1). Accepted; one choke point.** Before the opening release no provider mailbox exists in production, so no path can meet provider mail. After it, and in a dormant release after a rollback, the mailboxes service's access table refuses, by the signed kind, `AccessModel` (the new access for every person's request that may ask the model: search and matches with meaning, criteria and removal previews) and `AccessConnection` (every connection path, including the recheck before each webhook send) when the compiled uses close them; every path to the model or a vendor opens a mailbox through those two functions. One test runs every route and MCP tool against a failing model and egress and fails on a route without a case. Staging opens only for listed test subjects with every use closed ("The owner's legal gate"; T82 to T84, T116, T117).
13. **E13 · Google's reconnect block had no transaction with revocation (P1). Accepted; revocation is dropped.** No stored row can serialise with Google's project-wide revocation and its propagation. Permaily no longer calls it: the token becomes unreadable at the disconnect's commit and is deleted by the sweep, and the person is told where to remove Permaily, as for Microsoft and IMAP. Reconnecting is never blocked (choice 21; T5; T15, T73 removed).
14. **E14 · Byte admission left permitted in-flight allocations unbudgeted (P1). Accepted.** One pool in `providersync` reserves every class before allocating (raw imports, pages, IMAP non-literal answers), with the IMAP wrapper streaming body literals and bounding everything else at 1 MiB a command; the `CONNECT` reply is bounded at 4 KiB; entries are bounded by their codecs; an over-bound answer is asked again smaller, and a too-large message is skipped and counted with the cursor moving past it. The journal and the 32 KiB sync record are gone ("Memory", "The client and its bounds"; T35, T47, T81, T123).
15. **E15 · Choice 23 depended on the trust model and changed the brief's DNS over HTTPS (P2). Accepted as an explicit amendment.** Choice 23 now says it amends the brief and claims only a WebPKI logical-origin guarantee; the binding is specified (the policy's name as SNI and verified name, no byte before the handshake ends, a bounded `CONNECT` reply), the ID-token keys use the same egress, and HTTP versions per destination come from the spike rather than an assumption ("Egress"; T57, T124; spike item 8).
16. **E16 · The two quota halves were not a deployment-wide budget (P2). Accepted, with a weaker stated claim.** Budgets are fixed per enclave and set so that two together stay under each provider's per-user limit; nothing coordinates them. What they do not hold is said: two Permaily mailboxes of one provider account have two budgets, and the provider's 429 then rules. `Retry-After` is honoured up to seven days; a lost schedule costs one early request; `labels.list` is no longer used; IMAP limits are per failure domain ("Quotas and backoff"; T24, T48, T99, T127; T100 removed).
17. **E17 · The repair could not keep the poll and notice guarantees (P2). Accepted.** The lease is released by the poll's last write, so a healthy mailbox is not skipped; failover is within one lease. The notice is queued with the finder's own time (inside ADR 0041's five minutes) under a key per break, and the grant's `noticed` stops a second queuing; reconnect writes a new grant and so a new break gets a new key. The repair walks the index, and the person's list covers a mailbox missing from it ("The poller", "Notices by mail"; T56, T101, T126).
18. **E18 · Public buckets and retention copy understated observable events (P2). Accepted.** `/status` publishes no provider counts; the provider account is sealed in the record and nothing joins mailboxes across accounts; the privacy text lists every entry's lifetime and what the storage's reader sees, including per-folder claim counts ("The privacy page and terms", "The threat model"; choice 25; T104 removed).
19. **E19 · Tests and ownership left integration cuts without a buildable owner (P2). Accepted.** Each test's brackets now agree with the table; the composition with the real store, the delete and matches routes (their `Access`), notices and templates, settings and retention, the applications record's seam, and the final vendor send (through the recheck) each have one owner; T106 sits in the pull request that holds both copies; T97 runs a test-only open policy and T54 and T71 the closed one; T46 and T105 cover the composed run; fuzz is pull request 6's, reproducibility and the spike's artefact pull request 9's and the spike's; every pull request passes `make check` on its merge base ("Tests", "Implementation pull requests").
20. **E20 · The eight-week chain did not hold (P2). Accepted.** The calendar now assigns each person each week, starts every pull request only after its dependencies, and places the day-14 reading, the seven-day cycle, the floor's checklist and the gate; the critical path is 7 weeks to the dormant release, stated as the estimate of the rows without external waits ("Calendar").
21. **E21 · Successful OAuth responses needed an omitted-field contract (P2). Accepted.** An omitted `scope` is the requested set at the first exchange and the stored set at a refresh, an empty one refused; `tid` and `oid` are required through an exported check without the `sub` fallback; a refresh without a new refresh token keeps the stored one, distinct from `invalid_grant` ("Scopes"; T115).
22. **E22 · Backfill order, retention facts and webhook origin conflicted (P2). Accepted.** Origin is `live` for the live stream or a provider date at or after connecting, so a race between streams cannot silence new mail; a listing never admits a message with no date, a future date, or one older than the retention, so an expired copy is not admitted again; retention counts from the copy; no order is claimed for history, and the coverage words say only how many ("Bootstrap", "Storing", "History import"; T41, T42, T86, T97).
23. **E23 · The IMAP table enumerated six failure domains (P3). Accepted.** Six, named, and limits apply per failure domain ("The closed list").
24. **E24 · The release plan contradicted the one-step launch (P1). Accepted, with the owner's decision 13.** The three kinds are dormant together and open together in one release after the gate and the barrier; staging is isolated; uses are per kind; the folder choices beyond decision 9 are still listed for the owner (choice 26) ("Launch: one barrier", "The owner's legal gate"; T49, T71, T128).

### The first review's findings, as they stand now

1. **D1 · Provisioning and uniqueness.** Resolved by E1: ADR 0026's creation is the commit; no claim to recover.
2. **D2 · Mixed-version writes.** Resolved by E2: the floor, dormant releases and one opening barrier.
3. **D3 · Poll fencing.** Resolved by E5 and "Effect boundaries": nothing needs a fence; steps stay inside `SweepGrace`.
4. **D4 · Replay of destructive authority.** Resolved by E3 and the main rule: no stored state authorises a deletion.
5. **D5 · Occurrence lifecycle.** Resolved by E6: ADR 0026's claim, its lifetime and the takeover's mark check.
6. **D6 · IMAP digest identity.** Resolved by E10: no content match.
7. **D7 · Bootstrap and resets.** Resolved by E8: per-record Gmail cursor, one admission rule, resync with a baseline first.
8. **D8 · Folder moves and absence.** Resolved by E9 and E10: well-known lookups, `unknown` folders, per-command IMAP evidence.
9. **D9 · Kept decisions and historical cleanup.** Resolved by E7: deletion as a person's, no backdated identifiers.
10. **D10 · Scope and refresh checks.** Resolved by E21.
11. **D11 · OAuth after sign-out.** Resolved by E11.
12. **D12 · Host proxy as the boundary.** Resolved in the previous revision; E15's binding added.
13. **D13 · State and memory bounds.** Resolved by E4 and E14: no growing record, one pool, codec bounds.
14. **D14 · Gmail policy and onward transfer.** Resolved by E2, E12 and E24.
15. **D15 · Revocation.** Resolved by E13: no revocation call, stated.
16. **D16 · Due scanning and fairness.** Resolved by E17.
17. **D17 · History webhooks.** Resolved in the previous revision; E22's origin rule added.
18. **D18 · Quota accounting.** Resolved by E16, with its limits stated.
19. **D19 · IMAP incremental correctness.** Resolved by E10 and E14.
20. **D20 · Timestamps and coverage.** Resolved by E22.
21. **D21 · Recovery and notices.** Resolved by E5 and E17.
22. **D22 · Metadata and retention.** Resolved by E18.
23. **D23 · PR ownership.** Resolved by E19.
24. **D24 · Adversarial coverage.** Resolved by E19 and the matrix; with no strict sync record, the production-shaped store test E4 asked for reduces to T108's count of writes.
25. **D25 · Dependency-based estimate.** Resolved by E20.
26. **D26 · IMAP failure domains.** Resolved by E23.

## Changed after the design review (2026-11-27)

The first adversarial review, of the design at `f36c9a9` (Codex), found 14 P1, 10 P2 and 2 P3 (D1 to D26) and concluded "needs a redesign". All 26 were accepted in the revision at `e9210a0`, four with a different fix than the one proposed. Where that revision answered a finding with a mechanism this record has since removed, the answer now is in "Changed after the second design review (2026-11-27)", which says for each D where it stands. What that revision introduced and this one keeps: client identifiers in the image and secrets from the operator (D10, D11's trip binding); typed scopes and their allowlist (D10); the egress policy enforced in the enclave and verified (D12); unfiltered Graph delta and baselines before enumeration (D7); positive evidence per provider and per-provider folder tables (D8); the closed IMAP list with failure domains (D26); origin on stored messages for webhooks (D17); the due index and fair shares on ADR 0039's pattern (D16); the legal gate and its questions (D14); the notice through ADR 0041's queue (D21); the inventory and threat-model rows (D22); and the test matrix, ownership table and calendar (D23 to D25).

## What it rests on

ADR 0016 (strict anchored records; the strict read's bounds), 0017 (the providers' applications and their sealed secrets, the sign-in trip and its cookie, ID-token checks, sessions), 0018 (amended), 0024 (two enclaves, `ClockSkew`), 0026 (amended: the record, the list, creation, the claim, `IngestDeadline` and `SweepGrace`, disconnecting, the sweep, older releases), 0028 (deleting a message with its kept decisions, the deletion mark), 0029 (secrets only to the attested key), 0030 (the egress), 0031 (holds, unchanged), 0039 (the due index, the lease, fair shares, the repair, `inbox.Arrivals`), 0040 (rules, unchanged), 0041 (the queue of notices).
