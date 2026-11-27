# 0042 Reaching mail through providers: how (Gmail's API, Microsoft Graph and IMAP)

Status: Proposed, not built; revised after its design review (2026-11-27; "Changed after the design review (2026-11-27)" answers its 26 findings, D1 to D26, by number, and the record below states the design as revised). Designed on 2026-11-27 for F3's first step (docs/plan.md, "F3: order of work") within the owner's twelve decisions of 2026-11-27 ("The owner's decisions"), which this record does not reopen. Amends ADR 0018 (mailboxes are no longer forwarding only), ADR 0026 (the mailbox's record gains a kind, a provider mailbox has no address, its partition gains entries the sweep deletes, the lower bound of its decisions' cleanup) and ADR 0031 (a mirrored deletion forgets the message's kept decision within a day); builds on ADR 0016 (strict anchored records), 0017 (the providers' applications, the sign-in trip), 0024 (two enclaves), 0029 (secrets sent only to the attested key), 0030 (the egress; epochs of a strict record), 0039 (the due index, fair shares, the repair), 0040 (rules, unchanged) and 0041 (the queue of notices). What the owner did not decide is decided here, each time the choice that fails closed and is simplest to build and test, and listed in "Choices made for the owner (to confirm)"; what only the owner can settle with counsel is in "The owner's legal gate". Its tests are T1 to T112; its pull requests are in "Implementation pull requests". A re-review of this revision comes before any of them; the spike ("The spike, before building") comes before the provider adapters merge; no real Gmail data is processed before the legal gate is passed.
Date: 2026-11-27

## Context

Today a mailbox receives mail only by forwarding (ADR 0018, how in ADR 0026): the person tells Gmail or another provider to forward to a random address at `inbox.permaily.com`, and the MX inside the enclave seals and stores what arrives. That has three costs the owner wants gone in F3: forwarding brings nothing from before it started; Outlook.com and many work tenants forbid or hide automatic forwarding to outside addresses; and forwarding is a set-up of several steps at the provider that many people do not finish.

The owner decided on 2026-11-27 that a mailbox may instead read the person's mail from the provider: Gmail through its API, Outlook through Microsoft Graph, and a closed list of IMAP providers; the person chooses forwarding or a provider for each mailbox; what is read is stored as a sealed copy exactly as forwarded mail is, so rules, receipts, matches and decisions do not change. This record says how, exactly enough that the record, the sync engine, the three provider adapters, the routes and the web app can be built at the same time against seams committed first.

It is the opposite direction to ADR 0030. There Permaily is an authorization server and vendors are its clients; here Permaily is an OAuth client of Google and Microsoft, holding a refresh token that reads a person's mailbox. That token, and an IMAP app password, are the most powerful secrets the enclave will ever hold for a person: whoever has one reads the whole mailbox at the provider, also what Permaily never stored. The design is shaped by keeping them inside the enclave, sealed under the mailbox's own key, gone with it.

Synchronising a mailbox is also the first thing in Permaily that deletes mail on someone else's say-so. Every destructive step here therefore rests on an authority that cannot be put back by whoever writes the table, and on positive evidence from the provider, never on the absence of a report.

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

## Decision in short

- **A mailbox has a kind**: `forwarding` (every mailbox so far, and the default only for a legacy record), `gmail`, `outlook` or `imap`, fixed when it is made and written in its strict record (ADR 0026). A provider mailbox has no address at `inbox.permaily.com` and never appears at `RCPT TO`; it counts against the five mailboxes and the ten creations in thirty days like any other.
- **Two releases.** A compatibility release first, whose decoders keep fields they do not know and which refuses to write a record or list holding them; then the release with providers, turned on only once the KMS keys of every release before the compatibility release are deleted ("Rolling out with two enclaves and a rollback").
- **The real Gmail data gate.** The Gmail kind is off in production, and every model and vendor path is closed for mail read from any provider, until the owner records a policy determination under Google's Limited Use ("The owner's legal gate"). Closed is enforced in the image and tested; the spike uses synthetic or owner-only test mailboxes.
- **Connecting is an OAuth trip the web app starts and the enclave finishes**: authorization code with PKCE (S256), a state and a nonce, bound to the initiating session and rechecked at the effect, back to `https://app.permaily.com/api/v1/mailboxes/connect/callback`. The enclave exchanges the code, checks the granted scopes against a fail-closed allowlist after canonicalising them, reads the stable identity, and only then provisions the mailbox; the mailbox becomes active only after its grant and its sync record are durable ("Provisioning").
- **Client identifiers are in the image**, separate Google Cloud project and Microsoft application from signing in; **client secrets come from the operator** over the attested connection (`scripts/provider.sh`, ADR 0029).
- **One strict authority per mailbox for synchronisation**: the sync record, `providersync-<mailbox>-<epoch>`, a strict anchored record in epochs of at most 1,000 versions. It holds the run's fence (a generation taken once per poll), the grant's epoch, the provider baseline, every folder's committed checkpoint digest, the import's progress and the deletion journal's digest. Nothing destructive happens except under a commit of it at the version read.
- **Every effect is fenced twice**: in time, because a poll is a sequence of steps each bounded by one minute from its own strict reads and a step starts only while its lease leaves room, so ADR 0026's `SweepGrace` holds unchanged; and in storage, because cursors, state changes, grant epochs and deletion intents are published only by a commit of the sync record at the version read.
- **A message is an occurrence**: Gmail's message id, Graph's immutable id, IMAP's folder, `UIDVALIDITY` and UID. An occurrence has an identity row with versioned states (`pending`, `visible`, `user_suppressed`, `provider_absent`, `expired`) and fixed facts (first observed, provider date, expiry, origin). Content digests only suggest a move; they never name a message.
- **Destruction needs positive evidence**: Gmail's current labels or a 404 for the id; Graph's current parent folder for the immutable id or a 404; IMAP's complete presence check of a folder snapshot. A missing report, a truncated listing or an incomplete reset never deletes.
- **Two streams**: a live stream from a provider baseline captured at connecting (before any enumeration), and an add-only backfill of the history window. Mail first seen in the live stream is new mail; mail first seen by backfill or a reset enumeration is history, admitted only within its window, never clamped into it.
- **Retention is a fact of the occurrence**: expiry is the earlier of the provider's receipt time and the first observation, plus the mailbox's retention, fixed at the first claim; nothing past it is imported, whatever is replayed.
- **The poller** reuses ADR 0039's due index and its scan, with fair shares, per-enclave limits, deployment-wide quota budgets split between at most two enclaves, weighted by each provider's published costs, and the provider's `Retry-After` honoured.
- **Egress is a policy compiled into the image and enforced inside the enclave**: exact scheme, host, port, method and path prefix, checked before connecting and before a credential is attached, on every request including continuations; HTTP/2 only, no redirect; IMAP only to the listed hosts on 993 with the listed name as SNI. The host's allowlist is extended too, but trusted for nothing. `/status` publishes the whole policy and `permaily-verify` compares it exactly.
- **A grant that stops working** puts the mailbox in `needs_reauth`, announced through ADR 0041's queue once per grant epoch.

## The provider mailbox in the record

### Records and layout

What ADR 0026's table gains or changes. "Sealed" and "signed" mean what they mean there. "Strict" means a strict anchored record (ADR 0016), read and written only at the version read.

| What | Where | Treatment | Written how |
|---|---|---|---|
| A mailbox | Record `mailbox-<id>`, as ADR 0026 | Readable and signed, as today, plus `kind` (`gmail`, `outlook`, `imap`; absent only on a legacy forwarding record, below), `account_ref` (the hex HMAC, under a key derived from the root secret, of the provider account's stable identifier; empty for forwarding), `history_days` (0, 30 or 90), `connected_at` (the provider baseline's time) and `decisions_since` (the earliest day any of its decisions can be of: `connected_at` minus `history_days` minus one day). Sealed inside `sealed`: the provider account's display address as the provider gave it (untrusted, optional), and for IMAP the host's identifier from the closed list | Strict, made with `CreateNew` only after provisioning ("Provisioning"); the five fields are never changed after it |
| A provider account's claim | Entry `pk=provider-account#<account_ref>`, `sk=claim` | Sealed under a key derived from the root secret: `{mailbox, nonce, state: provisioning \| active \| releasing, until}` | Version 0 at provisioning; at the version read after ("Provisioning"). An honest-storage convenience, not an authority ("One provider account, one mailbox") |
| An account's mailboxes | Record `mailboxes-<account id>`, as ADR 0026 | Each entry gains `ref`, the `account_ref` of a provider mailbox | As today; written only by a release that keeps every field ("Rolling out") |
| A grant | Entry `pk=mailbox#<id>`, `sk=grant#<grant epoch>` | Sealed under the mailbox's data key (`mailbox-grant`, bound to the mailbox and the epoch): `{v, provider, epoch, refresh_token \| imap: {host, username, app_password}, scopes, subject, granted_at}` | Version 0 when its epoch begins; at the version read when the provider rotates the refresh token. Used only when its epoch is the sync record's `grant_epoch` |
| The sync record | Record `providersync-<mailbox id>-<epoch>` | Strict, sealed under the mailbox's data key: `{v, mailbox, epoch, next, run: {gen, holder, until}, grant_epoch, state, state_seq, notice_key, baseline, folders: [{fid, digest, seq, complete}], import: {phase, coverage, counts}, journal: {gen, pages, digest}}`, at most 32 KiB of JSON | Epoch 0 with `CreateNew` at provisioning; at the version read after. An epoch takes at most `SyncVersionsMax` (1,000) versions; the tail opens its successor with `CreateNew` before its last and names it in `next` (ADR 0030's epochs) |
| A sync record's hint | Entry `pk=mailbox#<id>`, `sk=sync-hint` | Signed: the newest epoch an enclave saw | Overwritten freely; decides nothing (a reader walks `next` forward from it, and from 0 without it) |
| A folder's checkpoint | Entry `pk=mailbox#<id>`, `sk=fold#<fid>` | Sealed (`mailbox-fold`): the provider's opaque position for one folder (Graph's `deltaLink` or `nextLink`; IMAP `{uidvalidity, uidnext, highestmodseq, mailboxid}`), at most 8 KiB | Written before the sync record's commit that names its digest and `seq`; valid only while the two agree |
| A deletion journal page | Entry `pk=mailbox#<id>`, `sk=journal#<gen>#<page>` | Sealed (`mailbox-journal`): at most 1,000 `{occurrence, identity gen, evidence}` | Written before the commit that names it; deleted after the commit that clears it |
| An occurrence's identity | Entry `pk=mailbox#<id>`, `sk=occ#<hex HMAC of the occurrence>` | Sealed (`mailbox-occ`): `{gen, state, message, first_observed, provider_date, expires_at, origin, location}` | Version 0 with the first claim; every state change at the version read with `gen` + 1 when the state leaves `visible` or returns to it; lives until `expires_at` plus a day |
| A message's occurrence | Entry `pk=mailbox#<id>`, `sk=msgocc#<message id>` | Sealed: the occurrence's HMAC | Version 0 with the claim; the reverse lookup from the inbox's identifier |
| Suppressions | Records `providersync-suppressed-<mailbox id>-<n>` | Strict, sealed: at most 2,000 occurrence HMACs, the tail opening its successor; at most 32 blocks | Written before a person's delete of a provider message is applied ("A person's delete") |
| The schedule of a mailbox | Entry `pk=providers#state#<shard>`, `sk=<mailbox id>` | Sealed under a key derived from the root secret: `{next_due, backoff_until, throttle_scope, failures, last_ok, last_outcome, counts}` | At the version read; a hint the repair remakes from the records; deleted by the mailbox's sweep |
| A due minute | Entry `pk=providers#due#<minute>`, `sk=<mailbox id>` | Signed: nothing but its key | Written before the old one is deleted; expires 72 hours after its minute |
| A revocation in flight | Entry `pk=providers#revoke#<account_ref>`, `sk=<grant epoch>` | Sealed under a key derived from the root secret (`provider-revoke`): the refresh token to revoke, `until` (24 hours) | Written before the disconnect's commit; deleted on Google's confirmation or at `until` ("Revocation") |

- **Why the grant is in the mailbox's partition, under its key.** Disconnecting removes the key from the record (ADR 0026, "What disconnecting does to the key"); from that commit on no enclave opens the grant again, and the sweep deletes the entry with the partition. A grant put back opens to nothing after a disconnect; within a live mailbox, a grant of another epoch is never used, and a grant whose `subject` is not the record's `account_ref` is refused (T14).
- **Why the sync record is strict and in epochs.** It is the one authority over what the provider has been seen to hold and what may be deleted, so a replay of it would be destructive (D4). ADR 0016's strict read lists every version, and fails past 10,000 objects; an epoch of 1,000 versions keeps a cold read to one listing page. A poll commits once when it takes the mailbox and once per step that publishes something, so a quiet mailbox spends 288 versions a day and opens an epoch about every three and a half days; the closed epoch is removed with `RemoveRecord` once its successor is committed.
- **Why the schedule is a hint under the root secret.** The scheduler must read it to decide what is due without unwrapping every mailbox's key; it holds times, counts and states, nothing of the mail and no secret, and nothing in it decides anything: the sync record does.
- **The sweep's prefixes** (`partitionPrefixes`, ADR 0026's sweep) gain `grant#`, `sync-hint`, `fold#`, `journal#`, `occ#` and `msgocc#`; the mailbox's removal also removes every epoch of its sync record and its suppression blocks (walking `next`), its schedule entry and due minutes, and releases its claim. A test lists every key a provider mailbox's life writes and checks the sweep deletes them (T40).
- **Commits of the mailbox record.** Unchanged from ADR 0026 (made, `receiving_since` once, pauses, disconnect, removal): connecting again, a token's rotation, the cursor and the import commit the sync record instead.

### Rolling out with two enclaves and a rollback

The current image decodes a mailbox's record and an account's list into typed structures and signs a fresh encoding when it writes, dropping fields it does not know (`internal/services/mailboxes/service.go`, `mailboxDoc`, `listDoc`). Production runs one enclave at a time (`deploy/host/permaily-enclave.service` terminates every enclave before starting one; ADR 0024: no deployment runs two yet), so two releases meet the same data only through a rollback, or a later deployment of two instances. ADR 0026 set the precedent for new kinds of record: its guarantees hold only once releases from before it no longer run, which the operator ensures by deleting their KMS keys (ADR 0026, "Older releases"; docs/operations.md, "Delete the key of an old release"). This record follows it, with one release more, because here old releases would not merely ignore the new data but rewrite it:

1. **Release A, the compatibility release** (pull request 1), with no provider code: the record's and the list's decoders keep every member they do not know and write it back unchanged; a record or list that holds a `kind` other than `forwarding`, or any member A does not know, is read-only for A (a pause, a disconnect or a list write answers `mailbox_newer`, counted; A's sweep and delivery skip it); A's sweep already knows the provider prefixes and removes them only for a mailbox whose record has ended. A missing `kind` means `forwarding` only for a record whose `v` is a legacy version and that has no provider field.
2. **The fence.** After A is deployed and verified, the owner deletes the KMS keys of every release before A, as the normal last step of a deployment (docs/operations.md). From then, only A or a later release can open the root secret.
3. **Release B** carries providers. Its operations checklist (docs/operations.md, written by pull request 12) refuses to set a provider's secret until `release_keys` holds no release before A. Rollback from B to A is safe: A neither rewrites nor deletes what it does not know. Rollback to a release before A is impossible by construction.

Tests T65 to T68.

### Provisioning

A provider mailbox is made in an order where every intermediate state is either invisible or recoverable (D1):

1. ADR 0026's steps 1 to 4: the account's list gains the reserved identifier (`ref` set), within the five and the ten.
2. The provider account's claim at version 0, `provisioning`, a fresh `nonce`, `until` ten minutes on (`ReservationAge`). `ErrExists`: an `active` claim whose mailbox record is live answers `provider_account_in_use`; a `provisioning` claim past `until`, or one whose mailbox has ended, is taken over at the version read, after fencing its mailbox identifier with ADR 0026's tombstone (so its record can never appear), and the orphan partition and sync record are swept.
3. A data key drawn; the grant `grant#0` sealed under it; the sync record's epoch 0 made with the baseline ("Bootstrap") and `grant_epoch` 0, `state` `importing` or `syncing`.
4. The mailbox's record made with `CreateNew`, `active`, holding the wrapped key. This is the commit that publishes the mailbox.
5. The claim to `active` at the version read; the schedule entry and its due minute; the notice "a mailbox was added", queued.

A crash before 4 leaves a reservation that ADR 0026's settler fences after ten minutes and a claim that the next connection of that account takes over; a crash after 4 leaves an active mailbox that polls (5 is repaired by the hourly repair from the record). A lost acknowledgement of 4 is read again strictly before anything else is written. Account removal during provisioning ends the reservation as ADR 0026's M20 does; step 4 re-reads the account's standing strictly first. Tests T64, T69, T70.

### One provider account, one mailbox

A provider account is connected to at most one live mailbox of the deployment (choice 3). The claim is a sealed entry, not a strict record, and so this is a convenience under honest storage, not a guarantee: whoever writes the table can delete a claim, and a second mailbox for the same provider account can then be made. What that costs is bounded: the second mailbox is made only by someone who completed the provider's own consent for that account, so it grants no access the provider did not; it doubles quota use and blurs whose mail it is. This record says so rather than spend a strict record per provider account (D1). The cross-account signal is the claim's key, a root-keyed HMAC: the table's reader learns that some mailbox has a claim, not whose account it is (D22).

### Kinds and the five

- **Making**: `POST /api/v1/mailboxes` for forwarding as today; a provider mailbox only through the connect flow below.
- **The limits** are ADR 0026's: five mailboxes that count, ten creations in thirty days, whatever their kind. Connecting again is not a creation; it is bounded at ten grant epochs in thirty days (`MaxGrantEpochsPerPeriod`), past which it answers `too_many_reconnects` (Google's testing mode needs about four).

### States

The record's `state` keeps ADR 0026's machine (`active`, `disconnecting`, `cancelled`, gone; lost and forked unreadable). A provider mailbox adds a sync state in its sync record, changed only by a commit at the version read, each change incrementing `state_seq`:

| Shown | Meaning |
|---|---|
| `connecting` | A sealed trip entry, not a mailbox ("The trip") |
| `importing` | The live stream is followed; the history window is being read too |
| `syncing` | Polled every five minutes |
| `backoff` | The provider answered 429 or 5xx, or did not answer; polled again at `backoff_until` (in the schedule, a hint) |
| `resetting` | The provider lost the position (Gmail 404, Graph `syncStateNotFound` or `resyncRequired`, IMAP `UIDVALIDITY`); a reset enumeration runs, add-only until complete ("Resets") |
| `needs_reauth` | The grant of this epoch does not work; not polled; keeps what it holds |
| `provider_off` | The operator removed the provider's secret, or the provider left the list; not polled, no notice |
| `paused` | The record's `paused_at`, as ADR 0026; not polled |
| `disconnecting`, `unreadable` | The record, as ADR 0026 |

| From | To | When |
|---|---|---|
| `importing` | `syncing` | The backfill read its window, or reached a bound (coverage recorded) |
| `importing`, `syncing`, `resetting` | `backoff` and back | 429, 5xx, timeout; the state before is kept in the record |
| `importing`, `syncing`, `resetting`, `backoff` | `needs_reauth` | `invalid_grant` to a refresh, a 401 that survives one refresh, a refresh response outside the scope allowlist, IMAP `AUTHENTICATIONFAILED` in two consecutive polls; committed only under the grant epoch the worker read |
| any | `resetting` | A provider reset response, or a folder checkpoint whose digest does not match the record |
| `needs_reauth` | the state before | A new grant epoch committed by connecting again with the same provider account |
| any | `provider_off` and back | The provider's secret removed or set again (operator), or the provider removed from the list by a release |

**What the person sees** (each sentence to review with the designer): "Importing: 1,240 messages so far from the last 30 days"; "Up to date, checked 3 minutes ago"; "Checking again in 15 minutes: Gmail asked us to slow down"; "Catching up after Gmail lost our place"; "Permaily can't read this mailbox any more. Connect it again"; "Gmail isn't available right now" (`provider_off`). After an import that reached a bound: "Imported the newest 20,000 messages; older messages from the last 90 days weren't copied."

**Accounts closing, blocked, not agreed, removed**: as ADR 0026 says for forwarding, polling goes on while forwarded mail would go on arriving, and stops at the commit that ends the mailbox. An operator's pause stops polling, as it stops `RCPT TO`.

## Connecting with Google and Microsoft

### The applications

- **Separate from signing in**, and for Google in a separate Cloud project (`permaily-mail`): Google revokes per user and project, verification of a restricted scope and its yearly CASA assessment never touch sign-in, and removing a mail secret leaves sign-in alone.
- **Client identifiers in the image**: `deploy/enclave.json` gains `mail_providers: {google: {client_id}, microsoft: {client_id}}` (and under `staging`). Choice 1.
- **Client secrets from the operator**, never in the image: `PUT /v1/admin/providers/{google-mail|microsoft-mail}` with the admin token, sent by `scripts/provider.sh set google-mail <client-id>` only to the attested key (ADR 0029), refused when the identifier is not the image's; sealed under the root secret in the providers' applications' record (ADR 0017). A provider whose secret is not set is off: the connect screen does not offer it, and its mailboxes go to `provider_off`. Rotating a secret is setting it again.
- **The Gmail kind is off in production until the legal gate** ("The owner's legal gate"): `mail_providers.google.enabled` is compiled into the image and is `false` until the release that carries the owner's determination; while it is false, `PUT .../google-mail` is refused in production images and the kind is not offered. Staging images may enable it for the owner's synthetic test accounts only (T71).

### The trip

1. `POST /api/v1/mailboxes/connect/start {provider: gmail|outlook, history_days, label}` (or `{provider, mailbox}` to connect again) by a signed-in session of an open, agreed account with a free slot. The enclave draws a state (32 bytes), a PKCE verifier (S256) and a nonce, and keeps them sealed in `pending#mailconnect#<SHA-256 of state>` for ten minutes with the account, **the session's identifier and its sign-out generation**, the provider, the purpose (`connect` or `reconnect` with the mailbox), `history_days` and the label; sets `__Host-permaily-mailconnect` (`HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/api/v1/mailboxes/connect`, ten minutes, a random value whose hash the entry holds); answers the provider's address.
2. The provider's address:
   - Google: `https://accounts.google.com/o/oauth2/v2/auth` with `response_type=code`, `client_id`, `redirect_uri`, `scope=openid email https://www.googleapis.com/auth/gmail.readonly`, `access_type=offline`, `prompt=consent`, `include_granted_scopes=false`, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`.
   - Microsoft: `https://login.microsoftonline.com/common/oauth2/v2.0/authorize` with `response_type=code`, `response_mode=query`, `client_id`, `redirect_uri`, `scope=openid email offline_access https://graph.microsoft.com/Mail.Read`, `prompt=select_account`, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`.
3. `GET /api/v1/mailboxes/connect/callback?code&state` (or `error`) on the app host, one more cross-site arrival in `internal/httpapi/crosssite.go`. Malformed (missing or repeated parameters, a state not 43 base64url characters): refused before any read. The entry is consumed by `DeleteEntryAt` at the version read, so one callback consumes it; the cookie's hash must match.
4. **Standing at the effect**, through whichever enclave: the sealed session must still be open with the same sign-out generation (accounts' `sessionOpen`, the check the existing provider link makes); the account open, its terms in force agreed, a free slot (or, to reconnect, the mailbox the account's and in `needs_reauth`); a Strict session cookie, when it came, must be of the same account. Any failure: nothing made, `provider_failed` (`signed_out` for a closed session).
5. The code exchanged at the token endpoint with the secret and the verifier, through the provider egress ("Egress"), reading at most 16 KiB.
6. **Checks**, each a refusal with a fixed reason, against a typed token response (`internal/providers/oauth`, which `internal/idp` lacks): the ID token's signature, issuer, audience, nonce and `iat` (`internal/idp`'s verifier, exported); a refresh token present (the proof of offline access); the stable identity (Google `sub`; Microsoft `tid` and `oid`, both required, no fallback to `sub`); and the granted scopes ("Scopes").
7. Provisioning ("Provisioning") or, to reconnect, the same provider identity (`provider_account_mismatch` otherwise), then a new grant epoch ("Tokens"). The browser goes to `/mailboxes/<id>`; a failure to `/mailboxes#connect_error=<reason>` with one of the fixed reasons (`cancelled`, `admin_approval_needed` for AADSTS65001 and AADSTS90094, `scope_not_granted`, `provider_account_in_use`, `provider_account_busy`, `too_many_mailboxes`, `too_many_reconnects`, `signed_out`, `provider_failed`), never a word of the provider's.

A row saved and replayed after it was consumed meets a code the provider has already redeemed (single use) and a cookie the browser no longer holds; it makes nothing (T7, T72).

### Scopes

Requested scopes are compiled in. Granted scopes are canonicalised, then compared with a fail-closed allowlist at the first exchange and at every refresh (D10):

| Provider | Canonicalisation | Must hold | May hold | Anything else |
|---|---|---|---|---|
| Google | `email` and `https://www.googleapis.com/auth/userinfo.email` are one; `openid` as is | `https://www.googleapis.com/auth/gmail.readonly` | `openid`, `email` | Refused, nothing stored |
| Microsoft (the access token's resource scopes, as the token response reports them) | `Mail.Read` and `https://graph.microsoft.com/Mail.Read` are one; OIDC scopes (`openid`, `email`, `profile`, `offline_access`) dropped from the comparison | `Mail.Read` | `User.Read` | Refused, nothing stored |

A refresh asks Microsoft for `https://graph.microsoft.com/Mail.Read offline_access` (bound to the Graph resource); a refresh response outside the allowlist is `needs_reauth` and its tokens are not stored. The display address (`email`, Microsoft's `preferred_username` or `email`) is optional and untrusted: shown to the person only, never matched; without it the mailbox shows "Outlook account" or "Gmail account". What a 403 or a conditional-access challenge from Graph means is established by the spike; until then, a 403 is `backoff` with outcome `forbidden`, and three in a row are `needs_reauth`.

### Tokens

- **Grant epochs.** Connecting again writes `grant#<n+1>` at version 0, then commits the sync record with `grant_epoch` n+1, a new run generation and the state before `needs_reauth`, then deletes `grant#<n>`. A worker holding epoch n can no longer commit anything (D3).
- **Refresh** when the access token held in memory is missing or within five minutes of its end, only by the worker holding the run, re-reading `grant#<epoch>` first. A rotated refresh token replaces the grant's at the version read before the access token is used; a write that fails leaves the old one, which both providers keep valid for its lifetime. Access tokens are never written anywhere.
- **Lifetimes**, as documented (confirmed by the spike): Google's refresh token lives until revoked, unused for six months, or the password changes; **in testing mode, seven days**. Microsoft's lives 90 days, renewed by each use; it ends at a password reset, a revocation, or a conditional-access policy.

### Revocation

Local termination and the provider's confirmation are two outcomes (D15):

1. Before the disconnect's terminal commit (ADR 0026, "Disconnecting", step 3), the grant is unsealed and, for Gmail, a revocation entry is written (`providers#revoke#<account_ref>`, sealed under a root-derived key, 24 hours).
2. The terminal commit. From it, no enclave opens the grant, and every step in flight ends within its step deadline, closing its IMAP session and dropping its access token.
3. Every enclave's revoker tries each revocation entry (`https://oauth2.googleapis.com/revoke`, five seconds a try, at 0, 1, 5, 15 and 60 minutes, then hourly) until Google answers 200 (confirmed; deleted) or 400 `invalid_token` (already revoked; deleted), or `until` passes (unconfirmed; deleted).
4. While a revocation entry for an `account_ref` exists, connecting that provider account again answers `provider_account_busy` ("try again in a few minutes"), so a late revocation can never end a new grant: Google revokes per user and project, not per token.
5. The disconnect's answer and the mailbox list say what happened: "Permaily's access was revoked at Google" only after confirmation; otherwise "Permaily deleted its access. To be sure, remove Permaily at myaccount.google.com/permissions". Microsoft and IMAP offer no client revocation: the words say where to remove Permaily (account.live.com/consent/Manage, myapps.microsoft.com, the provider's app-password page).

The revocation entry is in the data inventory (docs/data.md): a refresh token kept up to 24 hours after a disconnect, sealed under a root-derived key, for one purpose. Tests T15, T73, T74.

## IMAP

### The closed list

In the image (`internal/providers/imap/hosts.go`): nine host entries under six brands, each with its documented support as read on 2026-11-27; a host is listed in a release only with the spike's evidence for every cell of the release matrix (D26).

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

Five failure domains count Yahoo and AOL as one, and Zoho's US entries as one. The release matrix (docs/acceptance.md, pull request 12) lists per host: login with an app password (required or offered), `SPECIAL-USE`, `CONDSTORE`, `OBJECTID`, `UIDVALIDITY` stability, connection limits, and the spike's date. Not listed: Gmail and Outlook over IMAP, Proton, and any host the person types. A provider leaves the list only by a release; its mailboxes go to `provider_off`.

### The connection

- **TLS** through the provider egress ("Egress"): `CONNECT <listed host>:993`, TLS 1.2 or later, the listed name as SNI, verified against the image's roots (WebPKI only: no pinning, no exception, no `STARTTLS`, no other port).
- **Authentication**: `AUTHENTICATE PLAIN` (or `LOGIN` without `AUTH=PLAIN`), after TLS. `AUTHENTICATIONFAILED` in two consecutive polls: `needs_reauth`. `LOGINDISABLED`: `needs_reauth` with "turn IMAP access on at <provider>".
- **Read-only on the wire**: a command writer allows only `CAPABILITY`, `AUTHENTICATE`, `LOGIN`, `ENABLE CONDSTORE`, `LIST`, `EXAMINE`, `UID FETCH` (`BODY.PEEK[]`, `UID`, `INTERNALDATE`, `RFC822.SIZE`, `FLAGS`, `MODSEQ`), `UID SEARCH`, `NOOP` and `LOGOUT`, and refuses any other command before it is sent (T33).
- **One session per mailbox at a time**, held only within a step and closed at its deadline; per enclave at most eight IMAP sessions and two to one host.
- **The app password and username** are sealed in the grant; typed in the connect screen (`POST /api/v1/mailboxes/connect/imap {host, username, app_password, history_days, label}`, with the session's standing as in "The trip", step 4), tried once before anything is made, never shown or answered back. **Connecting again** is `POST /api/v1/mailboxes/{id}/reconnect/imap {app_password}` (the same host and username, a new grant epoch, the state kept) (D21).

### The client and its bounds

- **Library**: `github.com/emersion/go-imap/v2`, at a version pinned by pull request 8 with evidence (the version, and transcript tests) that `EXAMINE` is sent for its read-only select and `CONDSTORE` can be enabled. `QRESYNC` is not used: it is a later optimisation, not a requirement (D19).
- **The wrapped connection** bounds what any answer can make the enclave hold, preflighting before allocation: a line 64 KiB; a literal's announced length checked before it is read (a message's body at most 25 MiB plus one byte, any other literal 64 KiB); nesting 16; 10,000 tokens a response line; unsolicited responses 1,000 a command; one command's answer 32 MiB; 200 folders; a UID range expanded at most to 10,000 UIDs; each command 30 seconds. Over any bound the session closes and the step ends with the outcome `bounds`.
- **Fuzz targets** (`scripts/fuzz.sh`, a group `imap`): the wrapped reader with go-imap's decoder over arbitrary server bytes, the `LIST`, `FETCH` and `ESEARCH` answers, folder names in modified UTF-7.
- **A played server** for tests: go-imap v2's in-memory server behind TLS with a test CA, scripted to misbehave.

## Egress

The egress for providers is a policy compiled into the image and enforced inside the enclave; the host is untrusted and its allowlist is defence in depth only (D12).

| Destination | Port | Methods | Paths | For |
|---|---|---|---|---|
| `oauth2.googleapis.com` | 443 | POST | `/token`, `/revoke` | Google's tokens and revocation |
| `www.googleapis.com` | 443 | GET | `/oauth2/v3/certs` | Google's ID-token keys |
| `gmail.googleapis.com` | 443 | GET | `/gmail/v1/users/me/profile`, `/gmail/v1/users/me/history`, `/gmail/v1/users/me/messages`, `/gmail/v1/users/me/messages/{id}`, `/gmail/v1/users/me/labels` | The Gmail API |
| `login.microsoftonline.com` | 443 | POST; GET | `/common/oauth2/v2.0/token`; `/common/discovery/v2.0/keys` | Microsoft's tokens and keys |
| `graph.microsoft.com` | 443 | GET | `/v1.0/me/mailFolders`, `/v1.0/me/mailFolders/{id}/childFolders`, `/v1.0/me/mailFolders/{id}/messages/delta`, `/v1.0/me/messages/{id}`, `/v1.0/me/messages/{id}/$value` | Microsoft Graph |
| Each listed IMAP host | 993 | IMAP | none | IMAP over implicit TLS |

- **Where it is enforced.** `internal/providers/egress` holds the table (`egress.Policy`) and two dialers: an HTTP transport that, for every request, before it connects and before any `Authorization` header is attached, checks scheme `https`, the exact host, port 443, the method and the path template (one path segment per `{id}`, percent-encoded, no `..`), and drops the request otherwise (`egress_refused`, counted); and an IMAP dialer that connects only to a listed host on 993. Both send `CONNECT` over the allowlisted proxy (vsock 8000) and do TLS inside the enclave with the image's roots and the policy's host as SNI. HTTP/2 only (ALPN `h2`, refused otherwise, as the vendor egress), no redirect followed, no cookie, `Accept-Encoding: identity`, headers at most 16 KiB, each call 30 seconds, bodies bounded (a history or delta page 4 MiB, a Gmail raw message 34 MiB of base64url, a Graph `$value` 25 MiB plus one byte).
- **Continuations.** Graph's `@odata.nextLink` and `@odata.deltaLink` are opaque: each is parsed and checked against the same policy (host `graph.microsoft.com`, the delta path of the folder it came from) before it is stored or followed; one that is not is refused and the folder resets. Opaque tokens are only ever sent inside URLs the policy accepts.
- **The host's list.** `cmd/permaily-host`'s `defaultEgress` gains the hosts too, and a test fails when it and `egress.Policy` differ (T45); but nothing relies on it: a test runs every adapter through a permissive proxy and an off-origin continuation and sees nothing leave (T75).
- **DNS.** The host resolves the name of a `CONNECT`, untrusted, as for every allowlisted destination; DNS over HTTPS stays for DKIM. What is verified is the logical destination's TLS identity, never the IP or its location: a host that routes a correctly checked `CONNECT` elsewhere reaches a server that cannot prove the name, and the TLS handshake fails before any credential is sent. It can deny service, as it can by dropping the connection. Choice 23.

## Sync

### The poller

`internal/services/providersync`, run by every enclave, on ADR 0039's pattern as implemented in `internal/services/webhooks` (D16):

- **Due index**: each pass reads the due minute of now from its start and, while the scan is behind, each minute back to `scanBack` (ten minutes), then continues its forward scan from where it stopped, at most `scanEntriesMax` entries a pass, paginating within a minute; a due entry that does not fit the ready queue stays where it is. The next due minute is written before the old hint is deleted. An hourly repair walks every live provider mailbox (the mailboxes' index, each record and the sync record's tail read strictly) and remakes a missing schedule entry or due minute, deletes a schedule entry whose mailbox ended, and requeues `needs_reauth` notices not yet queued.
- **Admission**: due mailboxes go into ready queues, one per provider kind and within it one per account, taken round robin; at most 1,024 ready; at saturation, the scan admits accounts in rotation so no account waits more than one pass longer than another. Per enclave: 16 polls at once, 4 of one API kind, 8 IMAP sessions, 2 to one IMAP host, 1 mailbox of an account. These are per-enclave limits; the deployment-wide ones are the quota budgets ("Quotas and backoff").
- **Taking a mailbox**: the sync record's tail read strictly; skipped while `run.until` plus `ClockSkew` has not passed for another holder; taken by a commit at the version read with `run.gen` + 1, this enclave as holder and `until` = now + `LeaseFor` (six minutes).
- **Steps**: a poll is a sequence of steps, at most `PollBudget` (four minutes) in all. A step begins only while `until` minus now is at least `StepDeadline` plus `ClockSkew`; it reads the mailbox record and the sync record strictly (stopping when the mailbox is not `active`, is paused, its list does not name it, or `run.gen` is not this run's), unwraps the key, opens `grant#<grant_epoch>`, and does all its work, remote calls and writes, under a context of `StepDeadline` (one minute, equal to `mailboxes.IngestDeadline`) from before those reads. ADR 0026's `SweepGrace` therefore covers a step across a disconnect exactly as it covers a delivery (T21), and no step of a run outlives its lease, so a second enclave can take the mailbox only after every step of the first has ended (D3).
- **Interval**: due again at the poll's start plus five minutes (`PollEvery`), plus a jitter up to 30 seconds from the mailbox's identifier; `backoff_until` when later.

### Who decides what

| Effect | Authority | How it is fenced |
|---|---|---|
| A message stored (additive) | The occurrence's identity row and the suppression blocks | Within the step's deadline; the claim (ADR 0026) and the identity row at the version read; never over `user_suppressed`, `provider_absent` of the same `gen`, or `expired` |
| A cursor, a folder checkpoint, the import's progress | The sync record | A commit at the version read under this `run.gen`; a folder checkpoint entry counts only while its digest and `seq` equal the record's |
| A deletion | The sync record's journal, then the identity row | The journal page committed with its evidence under this `run.gen`; each deletion then moves its identity row from `visible` at the journalled `gen` to `provider_absent` at the version read before the inbox delete; a row whose `gen` moved is skipped |
| A state change (`needs_reauth`, `resetting`, `provider_off`) | The sync record | A commit at the version read under this `run.gen` and the `grant_epoch` read |
| A grant | `grant#<epoch>` and the sync record's `grant_epoch` | A new epoch only by connecting again; rotation within an epoch at the version read |
| A person's suppression | The suppression blocks | Strict, committed before the copy is deleted |

A replayed sync record epoch, folder checkpoint or journal page is either refused by the strict read (ADR 0016) or does not match the record's digest and is ignored (a folder reset, add-only). A replayed identity row can make a stale add look stored (a copy not made, which the next reset enumeration finds) or a deletion look pending (which re-checks provider evidence before acting); it cannot revive a deleted copy (the inbox's gone mark and `Reader.Delete` hold) or bring back a suppressed one (the strict blocks hold). Tests T18, T76 to T79.

### Bootstrap

At provisioning, before any enumeration, the enclave records a baseline in the sync record's epoch 0 (D7):

- **Gmail**: `users.getProfile`'s `historyId`. The live stream reads `history.list` from it.
- **Graph**: each included folder's first delta round, unfiltered (no `$filter`, so no 5,000-message cap), is the folder's snapshot; its final `deltaLink` is the folder's baseline. The person sees "Importing" until every folder's first round ends. Messages in the snapshot are classified by `receivedDateTime` against `connected_at` (the server's receipt time, not a header): at or after it, new mail; before it, history admitted only inside the window.
- **IMAP**: each included folder's `UIDVALIDITY`, `UIDNEXT` and, with `CONDSTORE`, `HIGHESTMODSEQ` from `EXAMINE`. UIDs at or above the baseline's `UIDNEXT` are the live stream.

`connected_at` is the baseline's time. With 0 days, nothing before the baseline is imported and everything after it is (T29, T80).

### Per provider

- **Gmail, live.** `users.history.list?startHistoryId=<committed>&historyTypes=messageAdded&historyTypes=messageDeleted&historyTypes=labelAdded&historyTypes=labelRemoved&maxResults=500`, page by page, with the page token kept in the step only. Each message touched is looked at by `users.messages.get?format=metadata&metadataHeaders=` for its current `labelIds`, `internalDate` and `sizeEstimate` (the spike confirms the fields for this shape; absent ones are unknown, never zero), and classified by the precedence table ("Folders") on its current labels, never on the history event alone. Included and not stored: `format=raw`. Not included, or 404: positive evidence of absence. The returned `historyId` is committed only after the last page and its effects (D7).
- **Graph, live.** Folders from `GET /me/mailFolders?includeHiddenFolders=false` and `childFolders`, at most 200, depth 8, by canonical well-known ids; per included folder, `delta` from its committed checkpoint with `Prefer: IdType="ImmutableId", odata.maxpagesize=100`, `$select=id,receivedDateTime,parentFolderId`. An addition not stored: `GET /me/messages/{id}/$value`. An `@removed` is a candidate only: `GET /me/messages/{id}?$select=parentFolderId` gives positive evidence (404: deleted; a parent in an included folder: a move, the location updated; a parent in an excluded folder: absence under choice 4) (D8).
- **IMAP, live.** `LIST` (with `SPECIAL-USE` and `OBJECTID`'s `MAILBOXID` when offered); per included folder `EXAMINE`; a changed `UIDVALIDITY` resets the folder. New messages: `UID FETCH <committed UIDNEXT>:<snapshot UIDNEXT − 1>` only when the interval is not empty, responses outside it refused (D19). Presence: a complete `UID SEARCH UID <known ranges>` over the folder's known UIDs, paged at 10,000 UIDs a command, under one `EXAMINE` snapshot; a known UID not returned by a complete check is positive evidence. With `CONDSTORE`, `UID FETCH ... (FLAGS) (CHANGEDSINCE <modseq>)` narrows what is fetched; `NOMODSEQ` falls back to the baseline method.
- **Commit points.** At the end of each step that changed anything: the folder checkpoints written, then the sync record committed with their digests, the import's progress and the journal's state. A step that read nothing new commits nothing.

### Folders

One precedence table per provider; exclusion wins over inclusion (D8):

| Provider | Excluded (wins) | Included | Not read at all |
|---|---|---|---|
| Gmail (labels on the message now) | `SPAM`, `TRASH`, `DRAFT`, `CHAT`; `SENT` without `INBOX` | Anything else: `INBOX`, `IMPORTANT`, `STARRED`, `CATEGORY_*`, user labels, no label (archived) | |
| Graph (well-known ids, and every descendant) | `junkemail`, `deleteditems`, `sentitems`, `drafts`, `outbox`, `conversationhistory`, `syncissues`, `recoverableitemsdeletions`, `scheduled`, `searchfolders` | `inbox`, `archive`, `clutter`, every folder the person made outside the excluded subtrees | Search folders |
| IMAP (`SPECIAL-USE`, RFC 6154) | `\Junk`, `\Trash`, `\Sent`, `\Drafts`, and their children | `INBOX`, `\Archive`, every other selectable folder | `\All`, `\Flagged` (virtual), `\NoSelect`, `\NonExistent`. Without `SPECIAL-USE`: the pinned names per host, compared after decoding |

- A Gmail message with `SENT` and `INBOX` (sent to oneself) is read (choice 16). Drafts and the provider's system folders are excluded because decision 9 excludes sent mail and drafts are unsent mail (choice 26, to confirm).
- **Discovery that is incomplete** (over 200 folders or depth 8, or a `LIST` that failed) is `partial_folders`, shown and counted: folders not listed are not imported, and nothing is ever deleted because its folder was not listed.
- **IMAP folders that disappear**: a folder missing from one complete `LIST` is deferred; only when missing from two complete `LIST`s at least 24 hours apart (`FolderGoneGrace`) are its occurrences absent. With `MAILBOXID`, a rename is recognised and the occurrences keep their identity.

### Storing

- **One path for every kind.** `mailboxes.Service.Import(ctx, id, occ, msg)` runs ADR 0026's sequence from its step 2.1 under the step's context, with a provider ingress: no SMTP quota is charged and no forwarding confirmation is looked for (the ingress-kind check, D5); DKIM checked and parsed as delivery does, the envelope sender empty, `StoredMeta.Source` (`gmail`, `outlook`, `imap`) and `StoredMeta.Origin` (`live`, `history`) set.
- **The occurrence** is `gmail\0<message id>`, `graph\0<immutable id>` or `imap\0<folder id>\0<uidvalidity>\0<uid>` (the folder id is `MAILBOXID` when offered, else the decoded name), HMAC'd under the key derived from the mailbox key ADR 0026 uses. Its identity row's transitions, each at the version read:

| From | To | By | Effect order |
|---|---|---|---|
| (none) | `pending` | First observation, admitted | Identity row v0 with the fixed facts, then the claim (`seen#` = the occurrence), then the writes, then `msgocc#`, then `visible` |
| `pending` | `visible` | The writes done | The item is written last; `visible` after it |
| `pending` (stale, past `ClaimStale`) | `pending` | A retry | Takes over keeping every fixed fact, as ADR 0026's claim |
| `visible` | `provider_absent` (`gen` + 1) | A journalled deletion with matching `gen` | Row first, then `Reader.Delete` (gone mark, raw, item, `txt#`, `confirm#`, `dec#`), then `msgocc#` |
| `visible` | `user_suppressed` (`gen` + 1) | A person's delete | Suppression block committed, then row, then `Reader.Delete` |
| `provider_absent` | `pending` (`gen` + 1, new message id) | A verified re-entry (the provider shows it in an included folder again) | Only after the item is gone; a new copy, new decisions |
| any | `expired` | `expires_at` passed | Nothing stored again; the row lives a day past `expires_at` |

- **The fixed facts** are set once at `pending` and never changed: `first_observed`; `provider_date` (Gmail `internalDate`, Graph `receivedDateTime`, IMAP `INTERNALDATE`, ignored when absent, before 2000 or more than `ClockSkew` in the future); `expires_at` = the earlier of `provider_date` and `first_observed`, plus the mailbox's retention; `origin`. The claim's `ReceivedAt` and `ExpiresAt` come from them, not from the clock of a retry (`inbox.Writer.Claim` gains fixed facts, D5). An occurrence whose `expires_at` has passed is not admitted, whether it is new, replayed or re-entered (D5, D20).
- **Identifiers.** A `live` message's identifier and `received_at` are from when Permaily first observed it, so ADR 0039's cursors see it as new. A `history` message's are from its `provider_date`, which admission has already checked lies inside the window (never clamped), so the inbox lists history in order. Every identifier is therefore on or after `decisions_since`, the lower bound the mailbox's removal walks its decisions from (D9).
- **`ErrClaimed`** (another worker's younger claim) skips the occurrence for this step; the next step finds it `visible` or takes the stale claim over.
- **Too large**: over 25 MiB (a known size, or the bytes read) is not stored and is counted ("2 messages were too large to copy"). Choice 8.
- **Memory** ("Admission of bytes").

### Admission of bytes

One admission contract covers every peak allocation of mail in the enclave (D13): `internal/admission` (moved out of `smtpd.Server`, unchanged for SMTP) gains a second pool, `ProviderBytes` (192 MiB), from which a provider import reserves its worst case before it allocates anything: Gmail raw 34 MiB encoded + 25 MiB decoded + 25 MiB sealed + 2 MiB parse and text = 86 MiB; Graph `$value` and IMAP literals 25 + 25 + 2 = 52 MiB; a message whose size is unknown reserves the maximum and streams under it. Waiting at most ten seconds ends the step (`busy`). At most two raw imports an enclave. ADR 0026's arithmetic (1,032 MiB under the runtime's 1,536 MiB) becomes 1,224 MiB; history and delta pages (4 MiB each, at most 4 at once) and IMAP sessions (8 × 1 MiB of buffers) are inside the 1,032 MiB's general margin, measured on staging under SMTP and imports at once (T81).

### Deletions

- Absence is acted on only with positive evidence ("Per provider"), never at the end of a poll by elimination. Evidence found in a step is journalled: a journal page (at most 1,000) written, then committed in the sync record with this run's generation; then each deletion applied in order ("Storing", the `provider_absent` transition); then the page cleared by the next commit. A crash between commit and application leaves a committed page the next run applies, each item rechecked against its identity row's `gen` (D3, D4).
- **Moves and continuity.** Gmail's id and Graph's immutable id survive moves: a move between included folders changes only the location. For IMAP, an occurrence that disappears from one folder while exactly one occurrence with the same content digest appears in another included folder in the same step is treated as a move (identity carried, the message and its decisions kept); every other case (two equal copies, a copy into a second folder, a move across steps) is an independent delete and an independent new copy, which loses the old copy's decisions. Choice 20 discloses it (D6).
- **Decisions.** A deletion goes through `Reader.Delete`, which removes the message's cached decisions (`dec#`) with it. Its strict decision in ADR 0031's day blocks is forgotten by the next write of that block, or at the latest by the sweep's pass over gone marks, which writes each block holding a decision of a message gone longer than `SweepGrace` at most once a day (`ForgetEvery`), from a reserve of 400 versions per block added to ADR 0031's bounds. So a mirrored deletion removes the kept decision within a day and SweepGrace, and never while a request that read the message is still running (D9).
- **Receipts** keep naming the message, as they do for a message the person deleted: they record what left, and decision 10 concerns kept decisions, not the record of what left (choice 27).

### Resets

A reset (Gmail 404 on `startHistoryId`, Graph `syncStateNotFound`, `resyncRequired` or 410, IMAP `UIDVALIDITY` changed, a checkpoint that does not match) is add-only until complete (D7):

1. A new baseline captured first (Gmail `getProfile`, a fresh unfiltered delta round for the folder, a new `EXAMINE`), and committed with `state` `resetting`.
2. An enumeration of the scope: Gmail `messages.list` in the window with the label filters, paged to its end; Graph the new delta round to its `deltaLink`; IMAP `UID SEARCH ALL` under one snapshot. Each listed occurrence not stored is admitted as `history` if inside the window, or as `live` if its provider date is after the last committed checkpoint's time (a gap recovered).
3. Only when the enumeration ended with positive completeness (the last page, the `deltaLink`, the tagged `OK`) is every occurrence Permaily holds in that scope and not listed, and not first observed after the new baseline, a candidate; each still needs its per-provider evidence before it is journalled.
4. An enumeration cut by a bound or a step's end resumes from its committed position; it never authorises a deletion until it completes. The person sees "Catching up".

### History import

- **Order and fairness**: Gmail `messages.list` newest first; Graph's snapshot pages as they come (Graph gives no order without its capped filter); IMAP per folder from the highest UID down, folders taken a page each in turn so one large folder does not consume the bound (D20).
- **Bounds a mailbox**: 20,000 messages admitted, 10 GiB, 24 hours of import time (choice 10), counted as examined, eligible, stored and skipped. Reaching one ends the backfill with its coverage recorded in the sync record; the person sees how far back it reached per folder ("Imported the newest 20,000 messages; older messages from the last 90 days weren't copied"). Live sync is unaffected.
- **The backfill is add-only**: it never deletes and never decides absence.

### Quotas and backoff

Budgets are deployment-wide, debited before each call by method and format, and split equally between at most `EnclavesMax` (two) enclaves, each holding its half without coordination (D18). Values from the providers' published guides on 2026-11-27, compiled in, confirmed by the spike for the new project's actual tier:

| Provider | Published limit | Cost (units or requests) | Per enclave, per mailbox | Per enclave, project |
|---|---|---|---|---|
| Gmail | 6,000 units a user a minute; 1,200,000 a project a minute | `getProfile` 1, `history.list` 2, `messages.list` 5, `messages.get` 20 (60 with `raw`) | 2,400 units a minute (40 % of the user's: imports at most 1,800, live first) | 480,000 units a minute |
| Graph | 10,000 requests per app per mailbox in ten minutes; 4 concurrent | 1 a request | 400 a ten minutes, 1 at a time | Per tenant, as documented; the spike confirms |
| IMAP | Per host, unpublished | 1 a command | 2 sessions to a host | 8 sessions |

| Answer | What the poller does |
|---|---|
| 429, Gmail's 403 `rateLimitExceeded` or `userRateLimitExceeded`, Graph's 429/503/504 | `backoff`: the provider's `Retry-After` honoured as given, up to 24 hours (longer is 24 hours and the outcome `long_retry`); without one, 1, 5, 15, 60 minutes then hourly, each with ±20 % jitter. The scope (mailbox, or project for a project-wide answer) and the deadline persisted in the schedule entry, and for a project in a gate every enclave reads |
| 5xx, a timeout, a reset, IMAP `BYE` or `UNAVAILABLE` | The same, scope mailbox |
| Gmail's 403 `dailyLimitExceeded` | Project gate until the next hour; `/status`'s `quota_blocked` |
| 401 | Refresh once; again 401: `needs_reauth` |
| `invalid_grant`, `AUTHENTICATIONFAILED` in two consecutive polls | `needs_reauth` |
| A reset response | "Resets" |
| A bound passed | The step ends `bounds`, counted |

## What changes for the person

### Screens (docs/web.md gains "Mailboxes from a provider")

- **Adding a mailbox** asks first how: "Forward mail to Permaily" (today's flow), "Connect Outlook", "Connect another provider (IMAP)", and "Connect Gmail" only where the Gmail kind is on. The provider choices say, before the person leaves: what Permaily will read ("all your mail except spam, trash, sent and drafts"), that it never changes or sends anything, how far back (0, 30 or 90 days; 30 chosen), how often (every five minutes), that the copy is sealed in the enclave and deleted when they disconnect, that while the legal gate is closed apps and the model cannot use mail read from a provider ("The owner's legal gate"), and, during Google's testing mode, "You'll need to connect Gmail again every 7 days".
- **IMAP**: the provider picked from the closed list, then the username and the app password, with the provider's own page on app passwords linked as text.
- **A provider mailbox's page**: the kind, the display address, the state and its sentence ("States"), how far history reached, messages too large, `partial_folders`, and the buttons "Connect again" (in `needs_reauth`; for IMAP, the reconnect form) and "Disconnect".
- **Disconnect**, with a fresh proof as today, says what will happen and, afterwards, what did ("Revocation").
- **The inbox** is the same; a provider mailbox's delete says "Deletes Permaily's copy; it stays in your Gmail" (`web/src/pages/inbox/`).

### Notices by mail

Through ADR 0041's queue, fixed words, each kind held to its facts (D21):

- "A mailbox was added" (ADR 0026) names the kind in fixed words ("a Gmail mailbox"), never an address or a domain (`internal/AGENTS.md`: nothing a person typed or a provider supplied).
- **New**: "Permaily can't read one of your mailboxes" when a mailbox enters `needs_reauth`: the kind's fixed word, no label, no address, no link. Its event key is `mailbox-needs-reauth/<mailbox>/<grant epoch>`, stored in the sync record's `notice_key` by the commit of the transition; the worker enqueues after the commit, and the repair enqueues any `needs_reauth` whose key it finds; the queue sends each key once (ADR 0041). Not for `provider_off`, `paused` or `backoff`.
- "A mailbox was disconnected" (ADR 0026) adds, for Outlook and IMAP, where to remove Permaily's access, and for Gmail whether Google confirmed.

### The privacy page and terms

Each to be classified and worded by the owner. TODO LEGAL: owner's review.

- What is read: the person's mail at the provider, every folder but spam, trash, sent and drafts, from `history_days` before connecting, every five minutes, read-only; the account's display address.
- What Permaily sends the provider: only what connecting and reading need: the authorization code, the refresh token and, for IMAP, the username and app password, and its read requests. Nothing of the person's from elsewhere (D22).
- What is kept: the sealed copy, as forwarded mail; the refresh token or app password, sealed, deleted at disconnect; a Gmail refresh token kept up to 24 hours after disconnect only to revoke it; identity rows and suppressions for as long as the copies they describe; sealed backups as docs/data.md says of all stored mail.
- Deletions mirrored, within a day for kept decisions; receipts keep naming deleted messages.
- What the storage's untrusted reader sees: that an account has a provider mailbox of a kind, when it is polled and written, how many writes; never content or the provider account.
- Google API Services User Data Policy and Limited Use: "The owner's legal gate".
- Microsoft's terms for applications using Graph (the publisher's terms and privacy URLs on the registration).
- **Google, Microsoft and the IMAP providers are not subprocessors**: they are the person's own email provider, which Permaily reads on their instruction; `/subprocessors/` says so.
- The terms: connecting a mailbox the person is authorised to read; what happens when a provider stops Permaily's access; testing mode.

## The owner's legal gate

TODO LEGAL: owner's review. Google's Limited Use governs every use and transfer of data obtained through `gmail.readonly`, testing mode included, and a determination made after a transfer cannot undo it (D14). Until the owner records a determination in this record (a dated section, with counsel's input as the owner chooses) and a release carries it:

1. **No real Gmail data.** The Gmail kind is off in production images (`mail_providers.google.enabled: false`, compiled; T71). Staging images enable it only for the owner's synthetic test accounts, which hold only mail the owner sent them for the purpose.
2. **Every model and vendor path is closed for provider-sourced content**, for all three kinds, enforced in the image by `inbox.SourcePolicy` (compiled; `closed` for `gmail`, `outlook` and `imap`): a connection's search and read, a webhook's candidate selection and its evaluation, a preview with the model, a rule's model-dependent condition and removal by meaning all treat a provider-sourced message as withheld (`held`, why `source_policy`), recorded in receipts as held, before any model call or any vendor egress; the default rule's protective behaviour is unchanged (nothing leaves undecided). The person still reads, searches and deletes their own copy in the app. Tests T82 to T84.
3. **The spike** uses synthetic or owner-only test mailboxes, never a person's.

The questions the owner must answer, each with what it decides:

1. **Uses.** Which user-facing features may use Gmail data: showing the person their own sealed copy; rules the person writes; the default rule; connections to vendors the person approves; webhooks; previews. Which are "user-facing features prominent in the requesting application's user interface" under Limited Use, and how the app presents each.
2. **Consent.** What the in-product disclosure and consent say before the Google screen, and whether connecting a vendor or turning on a rule needs a further consent naming Gmail data.
3. **Onward transfers to OpenAI.** Whether sending a Gmail message's subject and text to OpenAI (ADR 0028's Decisions API and Responses API) for a rule, the default rule or a connection is permitted, under what processor terms (no training, retention: Zero Data Retention was asked of OpenAI on 2026-11-15 and is not approved; until it is, ordinary retention, docs/data.md), and whether masking or a narrower part is required.
4. **Onward transfers to vendors.** Whether disclosing approved parts of Gmail messages to a vendor the person connects is a transfer Limited Use allows, and what the vendor must be told or bound to.
5. **Retention.** The sealed copy's retention for provider mail (choice 5 assumes forwarding's 7 to 365 days), the 24-hour revocation entry, identity rows and suppressions, receipts that outlive the copy, and sealed backups.
6. **Human access.** Who may ever read Gmail data (nobody in Permaily's design; the policy's exceptions for security, law and the person's consent), and how that is said.
7. **Outlook and IMAP.** Whether the same determination opens the model and vendor paths for Outlook and IMAP mail, or each needs its own (Microsoft's terms for Graph; the IMAP providers' terms).
8. **Verification and CASA timing.** When to request Google's verification and the CASA assessment, which only matter once real users wait.

## The threat model

- **What the host sees**: that the enclave connects to `gmail.googleapis.com`, `graph.microsoft.com` or an IMAP host, when, for how long and how many bytes each way. It cannot open the TLS. It can delay, drop or misroute connections (polls back off; mail waits at the provider); it cannot make the enclave send a credential anywhere else: the policy is checked inside the enclave before connecting and before credentials are attached, and TLS verifies the policy's name against the image's roots.
- **What the storage's reader sees** (D22): the mailbox record's readable fields (its kind, `account_ref` (a root-keyed pseudonym of the provider account, stable for the mailbox's life), `history_days`, `connected_at`, `decisions_since`); the claim's and identity rows' keys (HMACs) and their write times; the schedule's and due minutes' keys and timing; the sizes and timing of writes, and so how often a mailbox changes and when it is deleted. Never the provider account, an address, content, a token or a folder name.
- **What the storage's writer can do**: delete or put back entries (a grant: the mailbox reconnects or reads nothing; a claim: two mailboxes for one provider account, "One provider account, one mailbox"; a folder checkpoint or journal page: ignored by digest, a reset; an identity row: a copy missed or re-checked, never revived or unsuppressed). It cannot roll back the sync record or the suppression blocks (strict), nor make the enclave delete a message without provider evidence.
- **Refresh tokens and app passwords never leave the enclave**: sealed under the mailbox's key (and for 24 hours after a Gmail disconnect under a root-derived key, for revocation), sent only to the provider's token endpoint or the listed IMAP host over the checked egress, never logged (the scrubber drops their shapes, T46), never in an answer.
- **The operator** registered the applications and knows their client secrets; with a secret alone nobody reads a mailbox. The operator can turn a provider off (denial of service, shown). The operator cannot add scopes (compiled requests, a fail-closed allowlist), cannot turn Gmail on in production (compiled), and cannot open the source policy (compiled).
- **A compromised Google or Microsoft account of the person**: whoever holds it reads the mail at the provider already; through Permaily they can put mail in (by receiving it) and delete Permaily's copy (by deleting it at the provider), which mirrored deletions make possible by design (decision 10). Receipts of what already left stay.
- **A hostile or compromised IMAP server** on the list: bounded and preflighted reads, fuzzed parser, a command allowlist on the wire, TLS verified; it can send hostile mail, as any sender can.
- **Google's testing mode**: at most 100 test users added by hand (so Google, and whoever runs that console, know them); refresh tokens that end after seven days.
- **Microsoft's work tenants**: an administrator may need to approve; a tenant's policies may end tokens at any time; publisher verification is needed for many tenants.
- **Abuse from Permaily's address**: polls bounded per host and per enclave; IMAP a closed list; a person cannot make the enclave connect anywhere they choose.
- **Public telemetry**: `/status` publishes counts in buckets (0, 1–9, 10–99, 100–999, 1,000+) and only aggregates per kind, so a small test cohort's single import, revocation or reconnect is not visible as a step (D22).
- docs/threat-model.md has a short section pointing here; pull request 12 fills it in.

## What /status and permaily-verify show

`/status` gains `providers` (`attestation.ProvidersStatus`): `kinds` and for each `enabled` (compiled) and whether its secret is set; `scopes` (the requested sets and the granted allowlists, exactly as compiled); `read_only: true`; `push: false`; `poll_seconds: 300`; `history_days: [0, 30, 90]`; `client_ids`; `egress` (the complete `egress.Policy`: each destination with port, methods and path templates, and the IMAP hosts with port 993 and `tls: "implicit"`); `source_policy` (each kind's model and vendor paths, `closed` until the legal gate); `token_storage: "sealed_per_mailbox"`; `quota_blocked` per kind; and bucketed counts per kind (mailboxes by sync state, polls by outcome, stored, deletions mirrored, too large, revocations by outcome, `egress_refused`). Never an address, an account, a mailbox identifier or a provider's error text.

`permaily-verify` prints the section and fails when the published `egress` differs in any way from its pinned policy (a missing, extra or duplicate destination, a port, a method or a path), a requested or allowed scope is not in its pinned read-only set, `read_only` is false, `push` is true, `poll_seconds` is under 300, `source_policy` is open without its pinned determination, or `google.enabled` is true in a release that predates the determination (T53).

## Choices made for the owner (to confirm)

1. **Client identifiers in the image**, secrets through the operator's route. *Alternative*: both through the route, as sign-in.
2. **Separate Google Cloud project and Microsoft application for mail**, not the sign-in ones.
3. **One provider account, one mailbox of the deployment**, as an honest-storage convenience. *Alternative*: a strict record per provider account, for a guarantee.
4. **A move to spam or trash is a deletion** for Permaily, and the copy goes.
5. **Retention as forwarding's** (7 to 365 days, 90 by default), counted from the earlier of the provider's receipt time and the first observation; history choices only up to the retention.
6. **Every Gmail mailbox reconnects weekly during testing mode**, with a notice each time.
7. **History imports do not notify webhooks**; mail first seen in the live stream does, including delayed mail and a gap recovered by a reset.
8. **Messages over 25 MiB are skipped and counted**, as the MX refuses them.
9. **Concurrency per enclave**: 16 polls, 4 of one API kind, 8 IMAP sessions, 2 to one IMAP host, 1 per account.
10. **Import bounds**: 20,000 messages, 10 GiB, 24 hours a mailbox, coverage shown.
11. **A person's delete in Permaily deletes only Permaily's copy**, and the copy is not imported again while the provider keeps it (up to 64,000 suppressions a mailbox; past it, a delete answers `too_many_deleted_copies`).
12. **The IMAP list**: nine host entries under six brands, five failure domains, each kept only if the spike confirms it.
13. **No fresh proof to connect**, but a live session rechecked at the callback; a fresh proof to disconnect (as today).
14. **What disconnecting does to the copy**: deleted, as forwarding.
15. **`needs_reauth` is told by mail once per grant epoch**, and shown in the app.
16. **Sent mail sent to oneself is read** in Gmail (it carries `INBOX`).
17. **The sync state's authority is a strict record in epochs**, not a sealed entry.
18. **Gmail off in production until the legal gate**; Outlook and IMAP may ship with the model and vendor paths closed.
19. **Every model and vendor path closed for all provider-sourced mail** until the owner's determination, not only Gmail's.
20. **IMAP copies are separate messages**: two copies in two folders are two messages in Permaily, and a move across polls (or of a message with an equal copy) loses the old copy's decisions.
21. **Gmail revocation is tried for 24 hours**, and the same Google account cannot connect again meanwhile.
22. **Two releases**: a compatibility release, then providers once older releases' keys are deleted.
23. **DNS from the host**, untrusted; TLS identity, not IP, is what is verified. *Alternative*: provider DoH, which would add a DoH name to the egress and still not attest location.
24. **Quota split equally between at most two enclaves**, without coordination. *Alternative*: a shared budget record, a strict commit per call.
25. **Counts on `/status` in buckets.**
26. **Drafts and the providers' system folders are excluded** with sent mail (decision 9 names spam, trash and sent).
27. **Receipts outlive a mirrored deletion**, as they outlive a person's delete.

## The spike, before building

What must be proven against the real providers, each with what it decides. It runs with the owner's applications and **synthetic or owner-only test accounts** (never a person's mailbox; Gmail only through test accounts that hold mail the owner sent for the purpose), from a workstation, with no Permaily code in production. A spike in `spikes/providers/` (its own readme, never imported or deployed) is written then.

1. **Google's trip in testing mode**: the consent screen with `gmail.readonly` unverified; the refresh token with `prompt=consent`; the `scope` string returned, ticked and unticked; the seven-day end; revocation's answers (200, 400) and its propagation delay.
2. **Gmail's history**: `history.list` across label changes, archive, spam, trash, delete forever and a move back from trash; how long a `historyId` stays valid (an idle account read after 1, 7 and 14 days: the day-14 reading lands after the spike's engineering week, see "Calendar"); the 404; `getProfile`'s `historyId` as a baseline; units charged.
3. **Gmail's messages**: `format=metadata` with no headers returns `labelIds`, `internalDate` and `sizeEstimate`; `format=raw` of a 24 MiB message; that reading does not mark it read.
4. **Microsoft's trip**: a personal account and a work tenant, consent with and without publisher verification, AADSTS codes for admin approval, the token response's `scope` (resource scopes, `offline_access` present or not), the rotated refresh token, the ID token's `oid`, `tid`, `email` and `preferred_username`, and what a 403 or a conditional-access challenge looks like.
5. **Graph's delta**: an unfiltered first round of a folder above 5,000 messages; immutable ids across moves to another folder, to junk and to deleted items; `@removed` on a move; `GET /me/messages/{id}?$select=parentFolderId` after each; `syncStateNotFound`, `resyncRequired`, 410; `$value` of a large message; throttling headers.
6. **IMAP on each listed host**: login with an app password, `SPECIAL-USE`, `CONDSTORE`, `OBJECTID`, `UIDVALIDITY` stability, `EXAMINE` and `BODY.PEEK[]` leaving `\Seen` unset (seen on the wire), connection limits per account and per address, folder names without `SPECIAL-USE`. A host that fails leaves the list.
7. **go-imap v2**: the pinned version's transcript for read-only select and `CONDSTORE`.

**What the owner must create before the spike**:

- **Google Cloud**: a project for Permaily's mail (`permaily-mail`), separate from sign-in's; the Gmail API enabled; the OAuth consent screen: External, Testing, app name, support email, logo, home page, privacy and terms URLs, authorised domain `permaily.com`, developer contact; scopes `openid`, `email`, `.../auth/gmail.readonly`; test users: only the owner's synthetic test accounts; a Web client with redirect URIs `https://app.permaily.com/api/v1/mailboxes/connect/callback` and `http://localhost:8443/api/v1/mailboxes/connect/callback`.
- **Microsoft Entra**: an application registration for "Accounts in any organizational directory and personal Microsoft accounts"; platform Web with the same redirect URIs; a client secret (24 months at most, its end in docs/operations.md); delegated `openid`, `email`, `offline_access`, `Mail.Read`, no application permissions; publisher verification; terms and privacy URLs.
- **IMAP test accounts** at each listed host, with two-factor on and an app password each.

## Tests

T for this record. Each is a test of the pull request named in brackets ("Implementation pull requests"). Played servers: `gmailtest`, `graphtest` (HTTPS with certificates for the real names under a test CA, HTTP/2, scripted) and `imaptest`. "Two enclaves" means two services over one fake store with a controllable interleaving (`providersynctest.Cut`), and "stopped-worker replay" means a worker paused at a named write and resumed after the other acted.

- **T1** [2] A legacy record without `kind` reads as `forwarding`; a provider record has an empty `rcpt` and no address pointer; `RCPT TO` of any address never finds a provider mailbox.
- **T2** [2] Making a provider mailbox with four mailboxes through A and B at once: one succeeds, the other `too_many_mailboxes`; ten creations in thirty days counted across kinds.
- **T3** [2] One provider account connected through A and B at once: one claim wins; the other `provider_account_in_use`, its reservation fenced.
- **T4** [2] A claim put back after a disconnect, or copied under another `account_ref`: a new connection is refused only while a live record names that account; a stale `provisioning` claim is taken over after fencing.
- **T5** [2] Disconnecting a provider mailbox: after the terminal commit no enclave opens the grant; the sweep deletes every key of T40; the provider account can connect again (Outlook, IMAP at once; Gmail after its revocation entry ends).
- **T6** [2] The account's removal ends a provider mailbox as ADR 0026's M20; a step in flight writes nothing after `StepDeadline` from its reads.
- **T7** [5] The trip: start sets the cookie and the entry; the callback without the cookie, with another browser's, with a used state, after ten minutes, with a state never made, or malformed: nothing made, no entry left.
- **T8** [5] The exchange with the secret and the verifier; an ID token with another audience, nonce, issuer or an old `iat`: refused.
- **T9** [5] Scopes against the allowlist (revised, D10): Google with `email` or `userinfo.email` accepted, without `gmail.readonly` `scope_not_granted`; Microsoft with `Mail.Read` alone, with `https://graph.microsoft.com/Mail.Read`, with `User.Read`, with or without echoed `offline_access`: accepted; with `Mail.ReadWrite`, `Mail.Send` or an unknown scope: refused and never stored.
- **T10** [5] Microsoft's `access_denied` with AADSTS65001 or AADSTS90094: `admin_approval_needed`; any other error `provider_failed`; never the provider's text in the redirect or a log.
- **T11** [5] Connecting again with another provider account: `provider_account_mismatch`, the mailbox still `needs_reauth`, no new epoch.
- **T12** [5] The callback with no session cookie: the sealed session rechecked and used; with a session of another account: refused (revised, D11).
- **T13** [9] Refresh: a rotated refresh token written at the version read before the access token is used; a failed write keeps the old grant; no access token ever written (every write of the fake store checked).
- **T14** [9] A grant of another epoch, or one whose `subject` is not the record's `account_ref`: not used, nothing read.
- **T15** [5] Disconnect writes the revocation entry before the commit; the played Google sees the revoke; a failed or timed-out revoke leaves the entry, the disconnect's answer says "not confirmed".
- **T16** [5] The operator's route: a secret for another client identifier refused; set: offered; removed: not offered, its mailboxes `provider_off`, no notice.
- **T17** [9] One occurrence seen through A and B at once: stored once; the claim's key is the occurrence.
- **T18** [9] Replay (revised, D4): an earlier epoch of the sync record put back is refused by the strict read; a folder checkpoint or journal page put back is ignored by digest and the folder resets add-only; an identity row put back from `provider_absent` to `visible` revives nothing; a person's deleted copy is not stored again with its identity row deleted.
- **T19** [9] Every folder checkpoint deleted: resets, nothing stored twice, deletions only with positive evidence.
- **T20** [9] A step stopped after each write of a message: the next completes it, one message.
- **T21** [9] A step reads the record, then B disconnects: the step's writes end within `StepDeadline`; the sweep's last pass after `SweepGrace` deletes them.
- **T22** [9] Runs: A takes a mailbox; B skips it; A dies; B takes it after `until` plus `ClockSkew`; A, resumed, commits nothing (CAS) and starts no step.
- **T23** [9] Fair shares: 1,000 Gmail mailboxes due and one Outlook mailbox: the Outlook one starts within one pass; one account's five mailboxes one at a time; an import never holds a slot beyond `PollBudget`.
- **T24** [9] Backoff (revised, D18): `Retry-After: 120` waits 120 s; `Retry-After` of six hours waits six hours; of two days, 24 hours and `long_retry`; without it 1, 5, 15, 60 minutes with jitter; success returns to the state before.
- **T25** [9] `needs_reauth`: `invalid_grant`, a second 401, two polls of `AUTHENTICATIONFAILED`; through A and B at once one transition and one queued notice; no poll while in it; connecting again resumes from the committed checkpoints.
- **T26** [9] Paused not polled; closing, blocked, terms not agreed polled.
- **T27** [6, 9] Gmail: history of added, `SPAM`, `TRASH`, archived, deleted, restored from trash: stored, absent, absent, kept, absent, a new copy; the adapter's translation in [6], the effects in [9].
- **T28** [6] Gmail's precedence: `SENT` without `INBOX` excluded; with `INBOX` read; `DRAFT`, `CHAT` excluded; a user label with `SPAM` excluded.
- **T29** [6, 9] Gmail: 404 on `startHistoryId`: a reset, add-only until complete, then deletions with evidence; a message arriving between the old position and the new baseline stored.
- **T30** [7, 9] Graph: a move between included folders read in both orders and across two polls: one message, its decisions kept; a move to `junkemail`: absent after the `parentFolderId` check.
- **T31** [7] Graph: excluded well-known folders and their subtrees never read; 201 folders: `partial_folders`, nothing deleted for the 201st.
- **T32** [7, 9] Graph: `syncStateNotFound`, `resyncRequired`, 410 on a folder: that folder resets; others untouched.
- **T33** [8] IMAP: the command writer refuses everything outside its allowlist before it is sent; a full poll's transcript holds only allowed commands; `\Seen` never set on the played server.
- **T34** [8] IMAP: TLS with a certificate for another name, self-signed, TLS 1.1, or `STARTTLS` asked on 993: refused, no password on the wire.
- **T35** [8] IMAP bounds: a line of 65 KiB, a literal announced at 33 MiB (refused before reading), nesting 17, 10,001 tokens, 1,001 unsolicited responses, a silent command: the session closed, `bounds` or `timeout`, the allocator under the preflight.
- **T36** [8, 9] IMAP moves (revised, D6): one occurrence moved with a single equal-digest arrival in the same step: one message, decisions kept; two equal copies in one folder: two messages, deleting one leaves the other; a copy into a second folder: two messages; a move across two steps: delete and new copy; a `UIDVALIDITY` change: the folder resets.
- **T37** [8, 9] IMAP presence: deletions found by the baseline `UID SEARCH UID` method, with and without `CONDSTORE`, and with `NOMODSEQ`: the same messages absent.
- **T38** [8] IMAP without `SPECIAL-USE`: the pinned names excluded, modified UTF-7 decoded first.
- **T39** [8] Fuzz: the wrapped reader and decoder over arbitrary bytes.
- **T40** [2] Every key a provider mailbox's life writes is deleted by the sweep (with every sync epoch and suppression block); a forwarding mailbox's sweep unchanged.
- **T41** [9] History: 0 days imports nothing before the baseline; 30 and 90 days import within the window, never clamped; a message past its retention not imported; the bounds end the backfill with coverage per folder; live mail stored meanwhile.
- **T42** [9] Identifiers and origin: history by provider date and `Origin: history`; live by first observation and `Origin: live`; every identifier on or after `decisions_since`.
- **T43** [9] Over 25 MiB not stored and counted, by known size and by bytes read when the size lied or was absent.
- **T44** removed (D13): deletions are no longer held to the head of a poll.
- **T45** [11] `cmd/permaily-host`'s `defaultEgress` holds every destination of `egress.Policy`.
- **T46** [9] Nothing of a refresh token, access token, app password, address, username, subject or provider error text reaches a log line or an answer, over a full run of the played servers.
- **T47** [3] Memory: an import reserves its worst case from `ProviderBytes` before allocating; with the pool held, the step ends `busy` within ten seconds, nothing half stored.
- **T48** [9] Quotas (revised, D18): Gmail units debited by method and format; a page of 100 raw gets admitted over the budget's minutes, never past 2,400 units a minute a mailbox an enclave; `dailyLimitExceeded` gates every Gmail mailbox until the next hour.
- **T49** [10] Web: the add screen offers the kinds that are on and configured; Gmail's sentences only where enabled; the history choices that fit the retention.
- **T50** [10] Web: the IMAP form and the reconnect form against the contract; the password never echoed or kept.
- **T51** [10] Web: a provider mailbox's page in every sync state, `partial_folders`, coverage, "Connect again", the disconnect's outcomes for each kind.
- **T52** [10] Web: the callback's fixed reasons with their words; an unknown one as `provider_failed`.
- **T53** [11] `/status`'s `providers` with every key and nothing else; `permaily-verify` fails on each of its failure conditions, one at a time, and on a missing, extra or duplicate egress entry.
- **T54** [11] `TestMailEndToEnd`'s provider half through two dev enclaves and the played servers: connect, import 30 days, a new message stored and a webhook notice not sent (the source policy closed), a deletion mirrored, a rule holding a message, a connection's read withheld with its receipt, disconnect.
- **T55** [5] Connecting starts only for an open, agreed account with a free slot, and checks again at the callback.
- **T56** [9] The repair remakes a deleted schedule entry and due minute within the hour; drops one whose mailbox ended; requeues an unqueued `needs_reauth` notice.
- **T57** [4] The provider transport: HTTP/2 only, a 302 an error, bodies past their bounds refused, 30 seconds a call.
- **T58** [9] A provider whose secret was removed: `provider_off`, no `needs_reauth`, no notice.
- **T59** [9] Notices: "a mailbox was added" names the kind only; "can't read one of your mailboxes" holds no label, address, domain, link or code; its key is the grant epoch's.
- **T60** [5] The IMAP connect: one login tried before anything is made; a failure makes nothing; the password never in the answer, a log or the trip entry. The adapter is a fake satisfying `providers.IMAPLogin` until pull request 8 merges, then the real adapter in pull request 9's composition test.
- **T61** [9] A person's delete of a provider message: the suppression committed first, the copy gone, nothing sent to the provider, not stored again on the next poll or reset.
- **T62** [12] The staging check: the owner's synthetic Google test account, a Microsoft personal and a work account and one IMAP account of each listed host connected to a staging image; history imported; a new message within ten minutes; a deletion and a move mirrored; disconnect and revocation; the counts on `/status`.
- **T63** [9] Each mailbox polled every 300 to 330 seconds over an hour with two enclaves, never twice in one interval.
- **T64** [2] Provisioning stopped after each step (reservation, claim, grant, sync record, record, claim active, schedule): no active mailbox without grant and sync record; no grant left without a mailbox past ten minutes; the provider account free again.
- **T65** [1] Release A keeps every unknown member of a record and a list through a rewrite, byte for byte in JSON value.
- **T66** [1] Release A refuses to pause, disconnect or write a list holding a provider kind (`mailbox_newer`); its delivery and sweep skip it.
- **T67** [1, 2] New create, then A writes the account's list for an unrelated forwarding mailbox: `ref` survives; A's account removal leaves provider data to B's sweep.
- **T68** [12] Rollback B to A on a staging image with provider mailboxes, then forward again: nothing lost or rewritten.
- **T69** [2] Two creators of one provider account with lost write acknowledgements at each step: one mailbox; the other's leftovers swept.
- **T70** [2] Account removal during provisioning: no mailbox appears.
- **T71** [11] A production image refuses `PUT .../google-mail` and does not offer Gmail; a staging image with the flag offers it.
- **T72** [5] Consumed trip replayed after a restart through either enclave: nothing made; concurrent callbacks for one state: one consumer.
- **T73** [5] Revocation: a crash after the disconnect's commit before any try: another enclave revokes; 400 `invalid_token` treated as done; past 24 hours deleted unconfirmed; connecting the same Google account meanwhile `provider_account_busy`.
- **T74** [9] An IMAP session open across a disconnect is closed by its step's deadline.
- **T75** [4] Through a permissive proxy: an off-host, off-port, off-path or off-method request, and a Graph continuation pointing elsewhere, are refused before connecting and before a header is attached; the proxy sees nothing.
- **T76** [9] Stopped-worker replay at each commit and each identity transition with the other enclave taking over: no effect published by the stale run.
- **T77** [9] A reconnect (new grant epoch) while a provider request of the old epoch is in flight: the old worker cannot mark `needs_reauth` or write the grant.
- **T78** [9] A schedule entry deleted and recreated during a run: the run's fence is unaffected (it is in the sync record).
- **T79** [9] An ambiguous sync-record commit (stored, then an error): the next step reads it strictly and continues from it; nothing applied twice.
- **T80** [6, 7, 8, 9] With 0 days, a message arriving between connecting and the first poll is stored as new mail, for each provider.
- **T81** [12] Memory on staging: two raw imports and a full SMTP budget at once stay under the runtime's bound, measured.
- **T82** [3] Source policy: a connection's search and read return a provider-sourced message withheld (`source_policy`), the receipt says held; no model call made (a played Asker that fails when called).
- **T83** [3] Source policy: a webhook's candidate selection skips provider-sourced messages before evaluation; a preview with the model skips them.
- **T84** [3] Source policy: a person's rule with a model condition on a provider-sourced message holds it undecided without asking.
- **T85** [2] `Import` with a provider ingress charges no SMTP quota and reads no forwarding confirmation.
- **T86** [2] Fixed facts: a retry, a replay or a re-entry never changes `expires_at`; a message received at the provider on day 1 and first observed on day 8 with seven-day retention is not admitted; one observed on day 3 expires on day 8.
- **T87** [2] Identity transitions under competing user delete and provider delete or import, at each boundary: before the item, before `visible`, after either row would have expired.
- **T88** [2] A deletion of an IMAP occurrence whose message id is looked up through `msgocc#`; a lost `msgocc#` is rebuilt from the identity rows.
- **T89** [2] Decisions: a mirrored deletion's strict decision is forgotten by the block's next write or by the sweep within a day and `SweepGrace`; not while a request that read the message runs; the reserve of versions holds.
- **T90** [2] Removal of a mailbox with 90 days of history walks decisions from `decisions_since`; a lost record's removal walks from `decisionsFirst` minus 91 days.
- **T91** [9] Gmail: the history head committed only after the last page and its effects; a cut after page 1 resumes from the old head.
- **T92** [7, 9] Graph: a folder of 6,000 messages read completely by the unfiltered first round.
- **T93** [9] A reset cut by a bound deletes nothing; a reset during a reset restarts from a new baseline.
- **T94** [6, 9] Gmail `format=metadata` without `sizeEstimate` or `internalDate`: unknown, not zero; the bytes bound holds.
- **T95** [9] Folder discovery truncated, then complete: nothing deleted while truncated; an IMAP folder missing from one `LIST`, then from a second 24 hours later: absent only after the second.
- **T96** [8] IMAP `UID FETCH` with an empty interval not sent; an out-of-interval UID in a response refused.
- **T97** [9] Webhooks: a vendor connected during an import gets no notice for history; delayed live mail and a gap recovered by a reset notify; a crash after `Mark` and before the arrivals hint is found by the late scan.
- **T98** [9] Due index: an entry inserted behind the scan's minute, a full ready queue, a minute rollover, two-enclave saturation, a dead lease and repair during backoff: each mailbox polled within its interval plus one pass.
- **T99** [9] Quotas with two enclaves: each holds its half; failover of one does not double the other's.
- **T100** [9] A persisted project throttle holds every mailbox of that provider in both enclaves until its deadline.
- **T101** [9] `needs_reauth` committed then the worker dies before enqueue: the repair enqueues it once; replay does not send it twice for the same epoch.
- **T102** [9] Gmail weekly expiry across two enclaves with a lost checkpoint: reconnect, then a 404 history, then a safe reset.
- **T103** [5] IMAP reconnect replaces the app password in a new epoch and keeps the state and checkpoints.
- **T104** [11] `/status` counts are bucketed; a single import, revocation or reconnect in a cohort of five does not change a bucket.
- **T105** [9] Logs at source: an app password typed into the username field, a URL with a token in an error: neither in any log line.
- **T106** [4] Egress policy equality: the verifier's pinned copy and `egress.Policy` compared field by field.
- **T107** [12] Reproducible builds of release B on both architectures with go-imap pinned (`nix build .#eif` equal on x86_64 and arm64).
- **T108** [12] Quota soak on staging: 100 played Gmail mailboxes and 300 Graph and IMAP mailboxes for 24 hours under the budgets, no provider 429 unanswered, one vCPU within budget.
- **T109** [12] A seven-day token expiry and reconnect cycle on the owner's synthetic Gmail test account.
- **T110** [12] Real routes and callbacks on staging for Google and Microsoft with synthetic mail, including a refused scope and an admin-approval error.
- **T111** [2] Suppression blocks past 32: a person's delete answers `too_many_deleted_copies`, nothing deleted.
- **T112** [9] A grant refresh response outside the allowlist: `needs_reauth`, nothing stored.

**Matrix of invariants** (D24): each invariant changed by this record, the lowest layer that has the authority to implement it, and its adversarial tests.

| Invariant | Layer | Tests |
|---|---|---|
| Old releases cannot rewrite provider data | `services/mailboxes` (A's decoders) | T65 to T68 |
| A mailbox is active only when provisioned | `services/mailboxes` | T64, T69, T70 |
| Only the current run and grant epoch publish | `services/providersync` over the strict sync record | T22, T76 to T79 |
| No deletion without positive evidence | adapters translate; `providersync` decides | T27, T29 to T32, T36, T37, T93, T95 |
| An occurrence is stored once, never revived, never past retention | `inbox` writer and `services/mailboxes` identity rows | T17, T18, T86, T87 |
| A person's delete holds | strict suppression blocks | T61, T111, T18 |
| Kept decisions go with mirrored deletions | `inbox` block decisions and the sweep | T89, T90 |
| Egress cannot be widened by the host | `providers/egress` | T75, T106, T53 |
| Provider content reaches no model or vendor before the gate | `inbox.SourcePolicy` at every path | T82 to T84, T54 |
| Memory peaks are admitted before allocation | `admission` | T47, T35, T81 |
| A session ended cannot connect a mailbox | `services/accounts` seam, routes | T12, T72 |

## Implementation pull requests

Thirteen, after a re-review of this revision. Each merges onto a green, buildable `develop`; release A (pull request 1) is deployed, and the keys before it deleted, before release B (pull request 12) is cut.

| # | What | Owns (may edit) | Seams it consumes / produces | Tests | Engineer-weeks |
|---|---|---|---|---|---|
| 0 | **The contract and the seams**: this record as re-reviewed; `internal/contracts/mailboxes.go` (`MailboxKind`, `Mailbox.kind`, `provider`, `sync`, coverage; `ConnectStart`, `ConnectIMAP`, `ReconnectIMAP`, the fixed reasons) and `web/src/lib/api.ts`'s mirror; `internal/providers/providers.go` (`Source`: `Baseline`, `Live`, `Enumerate`, `Evidence`, `Raw`; `Occurrence`, `Page`, `Outcome`, `IMAPLogin`, `Tokens`); `providerstest.Contract`; `mailboxes.Importer` and the identity transitions' types; `providersync`'s `SyncStore` (the sync record's codec and transitions, versioned), `providersynctest` (fake store with `Cut`); `egress.Policy`'s type; `admission.Pool`'s interface; `inbox.SourcePolicy`'s type; `attestation.ProvidersStatus` | `internal/contracts`, `internal/providers` (interfaces, `providerstest`), `internal/services/providersync` (types, fakes only), `internal/admission` (interface only), `internal/inbox` (types only), `pkg/attestation` (type), `web/src/lib/api.ts` | Produces every seam | Compiles; Go and TypeScript shapes compared; the sync record's transitions table-tested | 1 |
| 1 | **Release A, compatibility**: decoders keeping unknown members, read-only for unknown kinds, sweep prefixes; its own release and deployment | `internal/services/mailboxes` (decoders, `settle`, sweep prefixes), docs/operations.md (the fence) | None new | T65 to T67 | 0.5 |
| 2 | **The record and storing**: provisioning, the claim, grant epochs' storage, `Import` with the provider ingress, identity rows and `msgocc#`, suppression blocks, fixed facts in the claim, decisions' lower bound and forgetting, disconnect and removal for provider kinds | `internal/services/mailboxes`, `internal/inbox` (`deliver.go` fixed facts, `read.go` delete hooks, `block_decisions.go` forgetting, `StoredMeta.Source`, `Origin`) | Produces `Importer`, `GrantStore`, `Suppressions`; consumes the store | T1 to T6, T40, T64, T69, T70, T85 to T90, T111 | 2 |
| 3 | **Admission and the source policy**: `smtpd`'s budget moved to `internal/admission` with `ProviderBytes`; `SourcePolicy` enforced in the discloser, connections' search and read, webhook candidate selection, previews and rules | `internal/admission`, `internal/smtpd` (use only), `internal/inbox` (`disclose.go`, `discloser.go`), `internal/services/connections` (`webhook_connections.go`, search, read), `internal/services/webhooks` (candidate filter), `internal/services/rules*` (model paths) | Produces `admission.Pool`, `SourcePolicy` | T47, T82 to T84 | 1 |
| 4 | **The egress**: `internal/providers/egress` (policy table, HTTP transport, IMAP dialer), `internal/enclave` (the vsock dial reused), `defaultEgress` | `internal/providers/egress`, `internal/enclave`, `cmd/permaily-host` | Produces `egress.Policy`, `egress.HTTP`, `egress.IMAP` | T57, T75, T106 | 1 |
| 5 | **OAuth as a client, and the routes**: the trip, the session seam, the exchange, scopes, revocation entries and revoker, the operator's route, the IMAP connect and reconnect routes | `internal/providers/oauth`, `internal/httpapi/mailboxes_connect.go`, `internal/httpapi/crosssite.go`, `internal/httpapi/mailboxes.go` (mount), `internal/services/accounts` (export `sessionOpen` as a seam), `internal/idp` (export the ID-token check), `scripts/provider.sh`, `cmd/permaily-enclave/main.go` (admin mount only) | Consumes `GrantStore`, `egress.HTTP`, `IMAPLogin` (fake); produces `Tokens` | T7 to T12, T15, T16, T55, T60, T72, T73, T103 | 1.5 |
| 6 | **Gmail's adapter** and `gmailtest` | `internal/providers/gmail` | `Source`, `egress.HTTP` | T27, T28, T29, T80, T94 (translation), the contract | 1 |
| 7 | **Graph's adapter** and `graphtest` | `internal/providers/graph` | `Source`, `egress.HTTP` | T30 to T32, T80, T92 (translation), the contract | 1 |
| 8 | **IMAP's adapter**: the closed list, the wrapped connection, the command writer, go-imap pinned, `imaptest`, the fuzz group | `internal/providers/imap`, `scripts/fuzz.sh`, `go.mod`, `go.sum`, `nix/go.nix` (`vendorHash`), `scripts/third-party-notices.ts` and the image inventory | `Source`, `IMAPLogin`, `egress.IMAP` | T33 to T39, T80, T96, the contract | 1.5 |
| 9 | **The sync engine**: due index, runs, steps, commits, journal, resets, backfill, quotas, backoff, repair, notices through the queue and their templates | `internal/services/providersync`, `internal/services/notices` (kinds), `packages/mail` (the two templates) and `internal/domain/mail/templates` (built) | Consumes `Importer`, `Suppressions`, `Source`, `Tokens`, `admission.Pool`, the notices queue | T13, T14, T17 to T27, T29, T30, T32, T36, T37, T41 to T43, T46, T48, T56, T58, T59, T61, T63, T74, T76 to T80, T91 to T93, T95, T97 to T102, T105, T112 | 3 |
| 10 | **Web**: adding by kind, the trip's hand-off, the IMAP and reconnect forms, a provider mailbox's page and states, disconnect outcomes, the inbox's delete words; docs/web.md | `web/src/pages/mailboxes/`, `web/src/pages/inbox/`, `web/src/lib/`, docs/web.md | The contract | T49 to T52 | 1 |
| 11 | **The image's composition**: `deploy/enclave.json`'s `mail_providers` (Gmail `enabled: false`), `cmd/permaily-enclave/providers.go`, `/status`'s `providers`, `cmd/permaily-verify` and its pinned policy, `TestMailEndToEnd`'s provider half | `cmd/permaily-enclave`, `cmd/permaily-verify`, `deploy/enclave.json`, `pkg/attestation` (check) | Composes everything | T45, T53, T54, T71, T104 | 1 |
| 12 | **Release B and the documents**: docs/threat-model.md, docs/data.md (inventory rows: grant, sync record, checkpoints, journal, identity rows, suppressions, claim, schedule, revocation entry, with lifetimes and backups), docs/legal.md, docs/operations.md (secrets, Microsoft's rotation, Google's test users, the fence), docs/verification.md, docs/acceptance.md (the IMAP release matrix), the privacy page and terms for the owner's review (`site/`), the staging checks, the release | docs, `site/` (legal pages) | None | T62, T68, T81, T107 to T110 | 1 |

### Calendar

One engineer-week is five working days of one person. Engineer-weeks total 16.5 (the table) plus the spike's 1, about 17.5. The calendar, with dependencies:

| Weeks from the re-review's approval | Work | Depends on | People |
|---|---|---|---|
| 0 | The owner's registrations (Google project, Entra application, IMAP test accounts) | The owner; publisher verification may take weeks and does not block the spike for personal accounts | Owner |
| 0–1 | Pull request 0; pull request 1 (release A) cut and deployed; old keys deleted (needs `tofu apply`, the owner) | Re-review | 1 |
| 1–2 | The spike's engineering week; the day-14 Gmail history observation lands in week 3 | Registrations | 1 |
| 1–3 | Pull requests 2, 3, 4 in parallel | 0 | 3 |
| 2–4 | Pull request 5 | 2, 4 | 1 |
| 3–5 | Pull requests 6, 7, 8 in parallel, each merging only after the spike's item for it | 0, 4, the spike | 3 |
| 3–6 | Pull request 9, the critical path (3 weeks), integrating 6 to 8 as they merge | 2, 3, 5 | 1 |
| 4–6 | Pull request 10 | 0, 5's routes | 1 |
| 6–7 | Pull request 11 | 2 to 10 | 1 |
| 7–8 | Pull request 12, staging checks (T107 to T110, the 24-hour soak, the seven-day token cycle starting in week 7 and ending in week 8) | 11 | 1 |

With three people from week 1, about eight weeks elapsed; with one person, the sum, about eighteen weeks. The legal gate is outside the calendar: Outlook and IMAP can ship with the model and vendor paths closed; Gmail waits for the owner.

## Risks

- **The legal gate may not open**, or may open only for some uses: Gmail stays off, or provider mail stays outside the model and vendors, which makes provider mailboxes readable in the app only.
- **Google's seven-day refresh tokens in testing mode** make every Gmail mailbox reconnect weekly.
- **Microsoft publisher verification** may take time; without it many work accounts cannot consent.
- **IMAP providers change** their app-password rules and limits; Permaily's single address may be throttled.
- **go-imap v2 is beta**: the wrapper's preflight and the command writer hold what it may do wrong.
- **Quotas are published, not contracted**: Google and Microsoft may lower them; the budgets keep Permaily under them at the cost of delay.
- **Strict commits cost**: about 288 a day for every provider mailbox plus one per busy step, so a few hundred mailboxes are about 100,000 S3 writes a day; measured on staging (T108).
- **Moves in IMAP lose decisions** in the cases choice 20 lists.
- **Mirrored deletions are a way to remove evidence** from Permaily's copy; receipts stay.
- **Five minutes is not instant.**

## What remains to verify

- Every provider fact marked as documented (lifetimes, quotas and costs, the `format=metadata` fields, folder ids, `CONDSTORE` and `OBJECTID`, Graph's unfiltered first round and its continuation lifetime, `historyId`'s life), in the spike.
- The memory arithmetic under SMTP and imports at once (T81).
- That 100 Gmail mailboxes and a few hundred Outlook and IMAP mailboxes fit the enclave's one vCPU and the strict commit rate (T108).

## Options considered

- **Push instead of polling**: declined by the owner (decision 5).
- **The cursor as a sealed entry written at the version read** (this record's first version): rejected after the review (D4): it is replayable, and it held destructive authority.
- **A strict record for every write of the sync state, one record forever**: rejected; it passes ADR 0016's bound in weeks. Epochs of 1,000 versions keep it bounded.
- **A lease outside the sync record** (ADR 0039's schedule lease): rejected (D3); it fences no other write.
- **Applying deletions at the end of a complete poll by elimination** (the first version): rejected (D8, D13); it confused moves, needed a pending set that could not drain, and treated missing reports as evidence.
- **IMAP identity by content digest** (the first version): rejected (D6); digest names bytes, not occurrences.
- **Backdating identifiers clamped into the window** (the first version): rejected (D9, D20); out-of-window mail was silently admitted and decisions fell before the removal's lower bound.
- **Graph's delta with `$filter=receivedDateTime ge`**: rejected (D7); it caps at 5,000 messages.
- **The host's `CONNECT` allowlist as the egress boundary** (the first version): rejected (D12); the host is untrusted.
- **A strict record per provider account for uniqueness**: not taken (choice 3); the claim is a convenience under honest storage, stated as such.
- **One release enabling providers directly**: rejected (D2); an old release's rewrite would drop provider fields.
- **The grant under the root secret**: rejected; a grant must die with its mailbox's key.
- **The vendor egress for providers**: rejected; it reaches any public name.
- **Arbitrary IMAP servers**: declined by the owner (decision 7).
- **Storing only references and reading the provider on demand**: declined by the owner (decision 3).

## Changed after the design review (2026-11-27)

The adversarial review of this design at `f36c9a9` (Codex) found 14 P1, 10 P2 and 2 P3 and concluded "needs a redesign". Each finding was checked against the code and the documents it cites; all 26 are accepted, four with a different fix than the one proposed. The record above states the design as revised; this section says, by number, what changed and where. Owner choices changed: 3, 5, 7, 11, 12 and 13 (reworded); 17 (now a strict record); 18 to 27 (new). Tests: T9, T12, T18, T24, T36 and T48 revised; T44 removed; T65 to T112 added. Pull requests: nine became thirteen.

1. **D1 · A partially created mailbox could reserve a provider account (P1). Accepted.** Confirmed: ADR 0026's settler fences only `stNone` reservations and finishes leaving records (`lifecycle.go`, `settle`), so an active record without a grant would occupy a slot forever. Provisioning now writes the grant and the sync record before the mailbox's record, so `CreateNew` of the record publishes a mailbox that can poll; the claim has a nonce, a `provisioning` state and an expiry, and is taken over only after ADR 0026's tombstone fences the abandoned identifier. Uniqueness is narrowed to an honest-storage convenience, stated with its cost ("Provisioning", "One provider account, one mailbox"; choice 3; T4, T64, T69, T70).
2. **D2 · Mixed-version rollout could erase provider fields (P1). Accepted, following ADR 0026's precedent.** Confirmed: `mailboxDoc` and `listDoc` drop unknown members on rewrite (`service.go`). Production runs one enclave at a time (`permaily-enclave.service` terminates all first; ADR 0024 "No deployment runs two enclaves yet"), so the mixed case is a rollback or a future second instance. As ADR 0026 did for mailboxes ("Older releases"), the guarantee rests on deleting older releases' KMS keys; one release more is added because old releases would rewrite, not merely ignore: release A keeps unknown members and refuses to write unknown kinds; B ships after keys before A are deleted; rollback to A is safe ("Rolling out with two enclaves and a rollback"; choice 22; T65 to T68; pull request 1).
3. **D3 · The lease did not fence writes (P1). Accepted.** Confirmed: `PutEntry` conditions only on its own item (`aws.go`), and the webhook lease is a separate entry. The run's fence and the grant epoch are now in the strict sync record; every cursor, state change, grant epoch and deletion intent is published only by a commit at the version read; and in time, a poll is cut into steps each bounded by `StepDeadline` (equal to `IngestDeadline`) from its own strict reads, started only while the lease leaves room, so `SweepGrace` holds and no step outlives the run ("The poller", "Who decides what", "Tokens"; T22, T76 to T79).
4. **D4 · A replayed cursor could cause destructive changes (P1). Accepted.** The sync record is strict and in epochs of 1,000 versions (ADR 0030's epochs), so it cannot be put back; folder checkpoints and journal pages are entries anchored by a digest in it; identity rows carry a generation that every destructive transition checks; suppressions are strict blocks; completed imports and grant epochs are in the record. The cap's explanation is corrected: 10,000 is the strict read's bound on listed objects, which a record written every five minutes reaches in about 35 days; epochs keep each record at one listing page ("Records and layout", "Who decides what"; T18, T76).
5. **D5 · Import and delete could resurrect mail or reuse identity (P1). Accepted.** Confirmed: the claim fixes receive time and expiry from the clock at claim (`deliver.go`), `Reader.Delete` writes its gone mark then deletes, and nothing mapped an inbox identifier back to a provider identity. Identity rows now have versioned states and fixed facts; expiry is a fact of the occurrence (the earlier of the provider's receipt time and first observation, plus retention), which `Claim` takes rather than computing; a reverse lookup `msgocc#` exists; the effect order of each transition is tabled; `ErrClaimed` is skipped and retried; the provider ingress skips SMTP quota and forwarding confirmations ("Storing"; T85 to T88).
6. **D6 · An IMAP digest conflated messages (P1). Accepted.** IMAP's identity is now the occurrence (folder id, `UIDVALIDITY`, UID; `MAILBOXID` when offered); the digest only suggests a move when exactly one disappearance and one arrival share it in one step; every other case is an independent copy, disclosed in choice 20 ("Deletions", T36).
7. **D7 · Bootstrap and reset gaps (P1). Accepted.** Confirmed against Google's and Microsoft's documentation (Graph's `$filter` caps delta at 5,000 and orders nothing without `$orderby`, read on 2026-11-27). A baseline is captured at provisioning before any enumeration; Graph's first round is unfiltered; Gmail's head is committed only after the last page; resets capture a new baseline first and are add-only until their enumeration completes ("Bootstrap", "Per provider", "Resets"; T29, T80, T91 to T93).
8. **D8 · Moves mistaken for deletions (P1). Accepted.** Absence now needs positive evidence per provider (Gmail's current labels or 404; Graph's `parentFolderId` for the immutable id or 404; IMAP's complete presence check); one precedence table per provider with exclusion winning; incomplete discovery never deletes; IMAP folders missing are deferred 24 hours. Owner decision 9 is applied to drafts and system folders as choice 26, to confirm ("Folders", "Per provider"; T28, T30, T31, T95).
9. **D9 · Strict decisions left behind; backdated mail leaked blocks (P1). Accepted.** Confirmed: the decision day comes from the message identifier (`rule_decisions.go`) and the removal walks from creation minus a day (`decisions.go`). Identifiers are never before `decisions_since` (a fixed field of the record), history is admitted only inside the window and never clamped, and a lost record's removal walks from `decisionsFirst` minus 91 days. A mirrored deletion's strict decision is forgotten by its block's next write or by the sweep within a day, after `SweepGrace`, from a reserve of versions; receipts survive as for a person's delete (choice 27) ("Deletions"; T89, T90).
10. **D10 · Scope checks rejected normal grants (P1). Accepted.** Confirmed: `internal/idp` parses no refresh response and its Microsoft verifier falls back to `sub`. Requested scopes, canonicalisation and a fail-closed allowlist per provider are tabled and applied at exchange and every refresh; refresh binds the Graph resource; `oid` and `tid` are required; the display address is optional and untrusted; the spike establishes 403 and conditional access ("Scopes"; T9, T112).
11. **D11 · The callback outlived the session (P1). Accepted.** Confirmed: the existing provider link seals the session and rechecks it (`accounts/providers.go`, `linkSignedIn`). The trip now seals the session and its sign-out generation, purpose and mailbox, and rechecks session, account, terms, slot and ownership at the effect, through either enclave; the row is consumed at the version read ("The trip"; T12, T72).
12. **D12 · The host's CONNECT defaults are not an egress boundary (P1). Accepted, as the brief required.** Confirmed: `-egress-allow` is a host flag (`cmd/permaily-host`), and `EgressClient` attempts HTTP/2 with fallback and follows redirects. The policy (host, port, method, path template) is compiled into the image and enforced by the enclave's own transport and IMAP dialer before connecting and before a credential is attached, including every continuation; `/status` publishes it whole and `permaily-verify` compares it exactly; DNS stays the host's, and only TLS identity is claimed (choice 23) ("Egress"; T75, T106, T53).
13. **D13 · State and memory bounds could stop progress (P1). Accepted.** Confirmed: an entry is one DynamoDB item; the SMTP budget is private to `smtpd.Server`. The single cursor is replaced by a bounded record (32 KiB), per-folder checkpoint entries (8 KiB) and journal pages (1,000); deletions no longer wait for the head of every folder, so there is no pending set to overflow; `internal/admission` holds one contract for SMTP and a provider pool, reserved at worst case before allocation; the IMAP wrapper preflights literals, nesting, tokens, ranges and unsolicited responses ("Admission of bytes", "The client and its bounds"; T35, T47, T81; T44 removed).
14. **D14 · Gmail data to OpenAI and vendors had no gate (P1). Accepted as the reviewer proposed.** No real Gmail data, testing mode included, until the owner records a Limited Use determination; Gmail is off in production images (compiled) until then; every model and vendor path is closed for all provider-sourced content by a compiled `inbox.SourcePolicy`, tested at every path; the spike uses synthetic or owner-only accounts; the owner's questions are listed ("The owner's legal gate"; choices 18, 19; T54, T71, T82 to T84).
15. **D15 · Revocation could fail silently or hit a new connection (P2). Accepted.** Local termination and Google's confirmation are separate outcomes; a revocation entry is retried for 24 hours from every enclave and fences reconnection of that account meanwhile; the words say only what happened; IMAP sessions end with their step; the mail project is separate from sign-in's ("Revocation"; choice 21; T15, T73, T74).
16. **D16 · Due scanning lost late work (P2). Accepted.** Confirmed: `webhooks.scanDue` rescans the current minute and back to `scanBack`. The poller now specifies that algorithm, next-due-before-delete, saturation fairness, repair from authoritative state (including `needs_reauth` notices and swept schedule rows) and says which limits are per enclave ("The poller"; T56, T98).
17. **D17 · Historical imports could reach webhooks (P2). Accepted.** Confirmed: `Connections.Pending` rescans an overlap window without an origin filter. `StoredMeta.Origin` is persisted and webhook candidate selection filters `history` (and, while the gate is closed, all provider mail); live, delayed and gap-recovered mail are defined as arrivals (choice 7; T97).
18. **D18 · Gmail quota math was wrong (P2). Accepted.** Checked against Google's quota guide on 2026-11-27: 6,000 units a user a minute, `messages.get` 20 (60 raw). Budgets are debited by method and format, split between two enclaves, live before import; `Retry-After` is honoured up to 24 hours; throttle scope and deadline are persisted; fallback has jitter ("Quotas and backoff"; T24, T48, T99, T100).
19. **D19 · The IMAP command subset did not establish correctness (P2). Accepted.** Explicit UID intervals from the `EXAMINE` snapshot with out-of-interval responses refused; a complete presence check as the baseline method, `CONDSTORE` an optimisation and `QRESYNC` not used; a wire-level command allowlist with transcript tests; the library version pinned with evidence ("Per provider", "The connection"; T33, T37, T96).
20. **D20 · Source timestamps could not serve every promise (P2). Accepted.** Observation order (origin), display time (provider date) and retention facts are separate; classification is by stream, not date; out-of-window mail is refused, not clamped; metadata fields are used only as demonstrated, absent sizes are unknown; IMAP folders are interleaved; coverage is reported per folder ("Storing", "History import"; T41, T42, T86, T94).
21. **D21 · Weekly reconnection and notices omitted recovery (P2). Accepted.** Confirmed against `internal/AGENTS.md` (no address in notices) and ADR 0041's queue. The notice goes through the queue with the key `mailbox-needs-reauth/<mailbox>/<grant epoch>`, recorded by the transition's commit and requeued by the repair; no provider domain is interpolated; an IMAP reconnect route exists; `paused`, `provider_off`, `needs_reauth` and `backoff` are distinct ("Notices by mail", "States"; T59, T101 to T103).
22. **D22 · Privacy copy and telemetry omitted metadata (P2). Accepted.** The threat model says what the storage's reader sees; docs/data.md gains inventory rows with lifetimes (pull request 12); counts on `/status` are bucketed; the privacy text says what Permaily does send the provider; the claim's key is a root-keyed HMAC ("The threat model", "The privacy page and terms"; choice 25; T104, T105).
23. **D23 · PR ownership did not cover files or contracts (P2). Accepted.** Confirmed: the routes mount in `internal/httpapi/mailboxes.go`, the admin providers mount in `cmd/permaily-enclave/main.go`, and `nix/go.nix` pins `vendorHash`. The table now assigns every file named here to one pull request, including the compatibility release, `go.sum`, `nix/go.nix`, notices and their templates, SMTP admission, webhook filtering, the inbox pages and the composition; seams and fakes land first; T60 uses a fake `IMAPLogin` until the adapter merges; protocol tests live in adapters and effect tests in pull request 9 ("Implementation pull requests").
24. **D24 · The test matrix missed critical cuts (P2). Accepted.** A matrix maps each invariant to its layer and adversarial tests; two-enclave cuts and stopped-worker replay are a fake store's feature (`providersynctest.Cut`); staged gates add reproducible builds, a quota soak, a token cycle and real callbacks with synthetic mail (T65 to T112; "Matrix of invariants").
25. **D25 · The estimate needed a calendar (P3). Accepted.** Engineer-weeks (about 17.5) and elapsed weeks (about eight with three people) are separate, with dependencies, the owner's registrations, the spike's day-14 observation and the seven-day token cycle placed ("Calendar").
26. **D26 · The IMAP list counted brands, not failure domains (P3). Accepted.** The table says nine host entries under six brands and five failure domains, and distinguishes required from offered app passwords; the release matrix lists verified prerequisites per host ("The closed list").

## What it rests on

ADR 0016 (strict anchored records; the strict read's 10,000 objects), 0017 (the providers' applications and their sealed secrets, the sign-in trip, ID-token checks, sessions), 0018 (amended), 0024 (two enclaves, `ClockSkew`), 0026 (the record, the list, the claim, `IngestDeadline` and `SweepGrace`, disconnecting, the sweep, older releases), 0028 and 0031 (decisions kept per message; amended: forgotten after a mirrored deletion), 0029 (secrets only to the attested key), 0030 (the egress, epochs of a strict record), 0039 (the due index, fair shares, the repair, `inbox.Arrivals`), 0040 (rules, unchanged), 0041 (the queue of notices).
