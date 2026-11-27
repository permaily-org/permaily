# 0042 Reaching mail through providers: how (Gmail's API, Microsoft Graph and IMAP)

Status: Proposed, not built. Designed on 2026-11-27 for F3's first step (docs/plan.md, "F3: order of work") within the owner's twelve decisions of 2026-11-27 ("The owner's decisions"), which this record does not reopen. Amends ADR 0018 (mailboxes are no longer forwarding only) and ADR 0026 (the mailbox's record gains a kind, a provider mailbox has no address, its partition gains entries the sweep deletes); builds on ADR 0016 (strict anchored records), 0017 (the providers' applications, the sign-in trip), 0024 (two enclaves), 0029 (secrets sent only to the attested key), 0030 (the egress), 0031 and 0040 (rules and decisions on stored mail, unchanged) and 0039 (the due index, the lease and fair shares, reused for the poller). What the owner did not decide is decided here, each time the choice that fails closed and is simplest to build and test, and listed in "Choices made for the owner (to confirm)". Its tests are T1 to T64; its pull requests are in "Implementation pull requests". A design review comes before any of them; the spike ("The spike, before building") comes before pull requests 3 to 5 merge.
Date: 2026-11-27

## Context

Today a mailbox receives mail only by forwarding (ADR 0018, how in ADR 0026): the person tells Gmail or another provider to forward to a random address at `inbox.permaily.com`, and the MX inside the enclave seals and stores what arrives. That has three costs the owner wants gone in F3: forwarding brings nothing from before it started; Outlook.com and many work tenants forbid or hide automatic forwarding to outside addresses; and forwarding is a set-up of several steps at the provider that many people do not finish.

The owner decided on 2026-11-27 that a mailbox may instead read the person's mail from the provider: Gmail through its API, Outlook through Microsoft Graph, and a closed list of IMAP providers; the person chooses forwarding or a provider for each mailbox; what is read is stored as a sealed copy exactly as forwarded mail is, so rules, receipts, matches and decisions do not change. This record says how, exactly enough that the record and the sync engine, the three provider clients, the routes and the web app can be built at the same time against seams committed first.

It is the opposite direction to ADR 0030. There Permaily is an authorization server and vendors are its clients; here Permaily is an OAuth client of Google and Microsoft, holding a refresh token that reads a person's mailbox. That token, and an IMAP app password, are the most powerful secrets the enclave will ever hold for a person: whoever has one reads the whole mailbox at the provider, also what Permaily never stored. The design is shaped by keeping them inside the enclave, sealed under the mailbox's own key, gone with it.

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

- **A mailbox has a kind**: `forwarding` (every mailbox so far, and the default when the record has none), `gmail`, `outlook` or `imap`, fixed when it is made and written in its strict record (ADR 0026). A provider mailbox has no address at `inbox.permaily.com`, no pointer from one and never appears at `RCPT TO`; it counts against the five mailboxes and the ten creations in thirty days like any other. A provider account (a Google account, a Microsoft account, an IMAP login at one host) is connected to at most one mailbox of the deployment at a time, by a create-only pointer named by an HMAC of its stable identifier.
- **Connecting is an OAuth trip the web app starts and the enclave finishes**: authorization code with PKCE (S256), a state and a nonce, through the browser, back to `https://app.permaily.com/api/v1/mailboxes/connect/callback`, for this browser alone (a `SameSite=Lax` cookie as ADR 0017's sign-in trip has). The enclave exchanges the code itself, checks that exactly the read-only scopes were granted, reads who the account is, and only then makes the mailbox, so nothing half made exists. An IMAP mailbox is made from the person's host (picked from the list), username and app password after one successful login.
- **Client identifiers are in the image** (`deploy/enclave.json`'s `mail_providers`), separate applications from those of signing in; **client secrets are given by the operator** over the attested connection (`scripts/provider.sh`, ADR 0029), sealed under the root secret as the sign-in applications' are.
- **A grant is sealed under the mailbox's data key** (`sk=grant` in its partition): the refresh token (or the IMAP username and app password), the scopes granted, when. It is never in a log, an answer or a record anyone else reads; an access token lives only in the memory of the enclave that holds the mailbox's lease. Disconnecting ends the record first (no key, so no grant opens again), then revokes the token at Google with the copy read before the commit; Microsoft and IMAP have no revocation a client can call, and the person is told where to remove the access.
- **The poller** reuses ADR 0039's pattern: a due index by minute, a sealed schedule entry per mailbox with a lease of two minutes renewed by every page, fair shares per provider and per account, bounded concurrency; every enclave runs it, one holds a mailbox at a time. Each mailbox is polled every five minutes; a poll reads changes from a cursor (Gmail `historyId`, Graph's `deltaLink` per folder, IMAP `UIDVALIDITY`, `UIDNEXT` and `HIGHESTMODSEQ` per folder), stores new messages and mirrors deletions.
- **A message is stored once** through the same claim and order of writes as a forwarded message (ADR 0026, "The claim"), named by a key of the provider's stable identity of the message (Gmail's message id; Graph's immutable id; for IMAP, which has no identifier that survives a move, the SHA-256 of the raw message), so two enclaves, a retry, a cursor put back or a full resync never store it twice. Everything after the store (the search entry, the inbox, matches, rules, decisions, receipts, webhooks' arrivals) is unchanged.
- **The cursor is a sealed entry written at the version read, not a strict record**: a commit every five minutes per mailbox would pass ADR 0016's 10,000 commits a record in about five weeks, and a cursor put back costs only work, because storing is idempotent and a deletion seen twice deletes nothing more. What going back would matter for (the kind, the provider account, the history window, the state, the key) is in the strict record.
- **History** (0, 30 or 90 days) is imported newest first, bounded at 20,000 messages, 10 GiB and 24 hours a mailbox, then the mailbox syncs. Messages over 25 MiB (the MX's own bound) are skipped and counted.
- **Folders**: every folder but spam, trash, sent, drafts and the provider's own system folders, per provider ("Folders"). A move out of an included folder into spam or trash is a deletion; a move between included folders is not.
- **Deletions are mirrored** after a complete poll, never in its middle, so a move between two included folders that a poll sees as a removal before an addition does not delete and store again. A deleted message goes through the inbox's existing delete, which removes its kept decisions; receipts keep naming it, as they do for a message the person deleted.
- **Egress**: `gmail.googleapis.com:443`, `graph.microsoft.com:443` and each IMAP host on `:993` are added to the allowlisted proxy (vsock 8000), never to the vendor egress (8004); the token endpoints are already there. `/status` gains `providers`: the hosts, the scopes, the interval, the history choices, `push: false`, `read_only: true`, the client identifiers and the counts; `permaily-verify` fails when a scope is not read-only or a host is not in its pinned list.
- **A grant that stops working** (revoked, expired, a changed password, Google's testing mode ending refresh tokens after seven days) puts the mailbox in `needs_reauth`: it keeps what it holds, stops polling, the app says so with a button to connect again, and one notice by mail is sent per transition.

## The provider mailbox in the record

### Records and layout

What ADR 0026's table gains or changes. "Sealed" and "signed" mean what they mean there.

| What | Where | Treatment | Written how |
|---|---|---|---|
| A mailbox | Record `mailbox-<id>`, as ADR 0026 | Readable and signed, as today, plus `kind` (`forwarding` when absent, `gmail`, `outlook`, `imap`), `account_ref` (the hex HMAC of the provider account's stable identifier, empty for forwarding), `history_days` (0, 30 or 90), `connected_at` (when the grant was first stored, the boundary between history and new mail). `rcpt` is empty for a provider mailbox. Sealed inside `sealed` as today: for a provider mailbox, the provider account's address as the provider gave it, and for IMAP the host's identifier from the closed list | `store.Anchored`, `Strict`, `OnlyNewest`, made with `CreateNew`; `kind`, `account_ref`, `history_days` and `connected_at` are never changed after it |
| A provider account's pointer | Entry `pk=provider-account#<account_ref>`, `sk=mailbox` | Signed with where it is stored: `{mailbox, write}` | Version 0 when the mailbox is made; deleted as read at disconnect, found from the record (or the list's new `ref`, below) when the record cannot be read. Proves nothing: the record must name the same `account_ref` |
| An account's mailboxes | Record `mailboxes-<account id>`, as ADR 0026 | Each entry gains `ref`, the `account_ref` of a provider mailbox (empty for forwarding), for the same reason the list holds `rcpt` | As today |
| A grant | Entry `pk=mailbox#<id>`, `sk=grant` | Sealed under the mailbox's data key (`mailbox-grant`, bound to the mailbox): `{v, provider, refresh_token \| imap: {host, username, app_password}, scopes, subject, granted_at, epoch}` | Version 0 when the mailbox is made; at the version read when the person connects again (`epoch` + 1) or the provider rotates the refresh token |
| A mailbox's sync cursor | Entry `pk=mailbox#<id>`, `sk=cursor` | Sealed under the mailbox's data key (`mailbox-cursor`): Gmail `{history_id}`; Graph `{folders: [{id, delta_link}]}`; IMAP `{folders: [{name, uidvalidity, uidnext, highestmodseq}]}`; the import's progress (`{phase, page_token \| per-folder resume, imported, skipped, bytes, began_at}`); the deletions waiting for the poll's end | At the version read, after every page |
| A provider message's location | Entry `pk=mailbox#<id>`, `sk=pid#<hex HMAC of the stable identity>` | Sealed (`mailbox-pid`): `{message, stable, locations}`, where `locations` is, for IMAP only, the `(folder, uidvalidity, uid)` where the message is seen (at most 16), and `deleted_by_person` when the person deleted it in Permaily | Version 0 with the message's claim; at the version read as locations change; expires with the message |
| The schedule of a mailbox | Entry `pk=providers#state#<shard>` (16 shards by a hash of the mailbox), `sk=<mailbox id>` | Sealed under a key derived from the root secret (`PurposeSeal`, as ADR 0039's queue): `{sync: importing, syncing, needs_reauth, backoff; next_due, lease: {holder, until}, backoff_until, failures, last_ok, last_outcome, counts}` | Made with the mailbox, at the version read after; remade by the hourly repair from the records |
| A due minute | Entry `pk=providers#due#<minute>`, `sk=<mailbox id>` | Signed: nothing but its key | Made when a mailbox becomes due; expires 72 hours after its minute |

- **Why the grant is in the mailbox's partition, under its key.** Disconnecting removes the key from the record (ADR 0026, "What disconnecting does to the key"); from that commit on no enclave opens the grant again, and the sweep deletes the entry with the partition. A grant put back after a disconnect opens to nothing. Within a live mailbox, a grant put back to an earlier one (an older refresh token of the same account) reads the same mailbox or fails and asks the person to connect again: it never reads another account, because the subject in the grant must equal the record's `account_ref` (T14).
- **Why the schedule is sealed under the root secret and not the mailbox's key**: the scheduler must read it to decide what is due without unwrapping every mailbox's key. It holds times, counts and states, nothing of the mail and no secret.
- **The sweep's prefixes** (`partitionPrefixes`, ADR 0026's sweep) gain `grant`, `cursor` and `pid#`; a test lists every key a provider mailbox's life writes and checks the sweep deletes them (T40).
- **Commits.** A provider mailbox's record takes the same commits as a forwarding one (made, `receiving_since` once, pauses, disconnect, removal): connecting again, a token's rotation, the cursor and the import take none. ADR 0026's 82 years hold.

### Kinds and the five

- **Making** (`POST /api/v1/mailboxes` for forwarding as today; a provider mailbox only through the connect flow below) runs ADR 0026's steps 1 to 7 with these differences: no address is drawn and no `rcpt` pointer written; instead the provider account's pointer is written at version 0 before `CreateNew` (`ErrExists`: the account is connected to another mailbox, answered `provider_account_in_use`, the reservation given up as ADR 0026 gives up a colliding address); the grant, the cursor (empty) and the schedule entry are written after `CreateNew` and before the answer; the notice "a mailbox was added" names the kind.
- **The limits** are ADR 0026's: five mailboxes that count, ten creations in thirty days, whatever their kind. Connecting again (`needs_reauth` to `syncing`) is not a creation.
- **One provider account, one mailbox of the deployment.** Connecting a Google account that another mailbox (of this account or another) already reads is refused while that mailbox lives. Choice 3.

### States

The record's `state` keeps ADR 0026's machine (`active`, `disconnecting`, `cancelled`, gone; lost and forked unreadable); a provider mailbox adds a sync state, which lives in the schedule entry, because going back in it costs nothing: a mailbox put back to `syncing` from `needs_reauth` fails its next poll and goes back; one put back to `importing` imports again, which stores nothing twice.

| Shown | Where it is read | Meaning |
|---|---|---|
| `connecting` | A sealed trip entry (`pending#mailconnect#<hash of state>`, ten minutes), not a mailbox | The person is at Google or Microsoft; nothing is made |
| `importing` | The schedule entry | History of `history_days` is being read, newest first; new mail is read meanwhile too |
| `syncing` | The schedule entry | Polled every five minutes |
| `backoff` | The schedule entry | The provider answered 429 or 5xx, or did not answer; polled again at `backoff_until` |
| `needs_reauth` | The schedule entry | The grant does not work (below); not polled; keeps what it holds |
| `paused` | The record's `paused_at`, as ADR 0026 | Paused by the operator: not polled |
| `disconnecting`, `unreadable` | The record, as ADR 0026 | As ADR 0026 |

| From | To | When | Write |
|---|---|---|---|
| (trip) | `importing` (`history_days` > 0) or `syncing` | The mailbox is made | The schedule entry at version 0 |
| `importing` | `syncing` | The import read its window, or reached a bound | At the version read |
| `importing`, `syncing` | `backoff` | 429, 5xx, a timeout, a connection refused | At the version read: `backoff_until` from the provider's `Retry-After` (at most an hour) or 1, 5, 15, 60 minutes, then every hour |
| `backoff` | the state before | `backoff_until` passed and the next poll succeeded | At the version read |
| `importing`, `syncing`, `backoff` | `needs_reauth` | Google or Microsoft answers `invalid_grant` to a refresh, a 401 survives one refresh, the scopes granted are no longer the read-only set, or IMAP answers `AUTHENTICATIONFAILED` twice in a row | At the version read; the notice "can't read a mailbox" sent by the call that committed it, once |
| `needs_reauth` | `syncing` (or `importing` when the import had not ended) | The person connected again with the same provider account | The grant at the version read (`epoch` + 1), then the schedule |

**What the person sees.** "Importing: 1,240 of the last 30 days' messages" (no total is promised: a provider does not say it cheaply); "Up to date, checked 3 minutes ago"; "Checking again in 15 minutes: Gmail asked us to slow down"; "Permaily can't read this mailbox any more. Connect it again"; with the provider account's address and the kind's logo.

**Accounts closing, blocked, not agreed, removed**: as ADR 0026 says for forwarding, polling goes on while forwarded mail would go on arriving (closing, blocked, terms not agreed), and stops at the commit that ends the mailbox (removal). An operator's pause stops polling, as it stops `RCPT TO`.

## Connecting with Google and Microsoft

### The applications

- **Separate from signing in.** Sign-in's applications (ADR 0017) ask for `openid email`; the mail applications ask for read access to mail. Kept apart, Google's verification of a restricted scope (and its yearly CASA assessment) never touches sign-in, a consent screen never asks for both, and turning mail off (removing a secret) leaves sign-in alone.
- **Client identifiers in the image**: `deploy/enclave.json` gains `mail_providers: {google: {client_id}, microsoft: {client_id}}` (and the same under `staging`). The image says which application people are sent to, `/status` shows it and `permaily-verify` prints it, and only a release changes it. Choice 1.
- **Client secrets from the operator**, never in the image: `PUT /v1/admin/providers/{google-mail|microsoft-mail}` with the admin token, sent by `scripts/provider.sh set google-mail <client-id>` only to the attested key (ADR 0029), refused when the identifier is not the image's; kept sealed under the root secret in the providers' applications' record (ADR 0017). A provider whose secret is not set is off: the connect screen does not offer it, and existing mailboxes of it go to `backoff` with the outcome `provider_off` (not `needs_reauth`: nothing the person can do helps). Rotating a secret (Microsoft's expire within 24 months) is setting it again; tokens issued under the old one keep working.

### The trip

1. `POST /api/v1/mailboxes/connect/start {provider: gmail|outlook, history_days, label}` by an open, agreed account with a free slot (checked here and again at the end). The enclave draws a state (32 bytes), a PKCE verifier (S256) and a nonce, keeps them with the account, the provider, `history_days` and the label in a sealed entry `pending#mailconnect#<SHA-256 of state>` for ten minutes, sets `__Host-permaily-mailconnect` (`HttpOnly`, `Secure`, `SameSite=Lax`, ten minutes, holding a random value whose hash the entry holds) and answers the provider's address. No fresh proof: making a forwarding mailbox needs none either, and the trip only adds a mailbox to the caller's own account.
2. The provider's address:
   - Google: `https://accounts.google.com/o/oauth2/v2/auth` with `response_type=code`, `client_id`, `redirect_uri`, `scope=openid email https://www.googleapis.com/auth/gmail.readonly`, `access_type=offline`, `prompt=consent` (so a refresh token is always issued), `include_granted_scopes=false`, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`.
   - Microsoft: `https://login.microsoftonline.com/common/oauth2/v2.0/authorize` with `response_type=code`, `response_mode=query`, `client_id`, `redirect_uri`, `scope=openid email offline_access https://graph.microsoft.com/Mail.Read`, `prompt=select_account`, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`.
3. The provider sends the browser to `GET /api/v1/mailboxes/connect/callback?code&state` (or `error`), which `internal/httpapi/crosssite.go` lists as one more cross-site arrival. The entry is found by the state's hash and used once (deleted as read); the cookie must match; the account must be the session's when a session cookie came, and the trip's otherwise (the session cookie is `Strict` and does not come back from Google: the trip remembers the account, as linking a provider does in ADR 0017).
4. The enclave exchanges the code at the token endpoint (`oauth2.googleapis.com/token`, `login.microsoftonline.com/common/oauth2/v2.0/token`) with the client secret and the verifier, through the allowlisted proxy, reading at most 16 KiB of the answer.
5. Checks, each a refusal with a fixed reason: an ID token signed by the provider's keys, with this client as audience, the nonce, and a fresh `iat` (`internal/idp`'s verification, reused); a refresh token present; **the scopes granted exactly** `openid email gmail.readonly` (Google lets a person untick a scope: `scope_not_granted`, "Permaily needs permission to read your mail; nothing was connected") or including `Mail.Read` and `offline_access` and no scope that writes or sends (`Mail.ReadWrite`, `Mail.Send`, any `.ReadWrite`): such a token is never stored; the stable subject (Google `sub`; Microsoft `tid` and `oid`) and the address (`email`, or Microsoft's `preferred_username`) read from the ID token.
6. The mailbox is made ("Kinds and the five") with the grant. The browser goes to `/mailboxes/<id>` with nothing in the address but the identifier. Every failure goes to `/mailboxes#connect_error=<reason>` with one of fixed reasons (`cancelled`, `admin_approval_needed` for Microsoft's AADSTS65001 and AADSTS90094, `scope_not_granted`, `provider_account_in_use`, `too_many_mailboxes`, `provider_failed`), never a word of the provider's.

**Connecting again** (`needs_reauth`): the same trip with `mailbox` instead of `history_days`, which must come back with the same subject (`provider_account_mismatch` otherwise: a person who signs in to another Google account at Google's page is refused, the mailbox keeps waiting); the grant is replaced at the version read.

### Tokens

- **Refresh** when the access token held in memory is missing or within five minutes of its end, only by the enclave holding the mailbox's lease, so two enclaves do not race one refresh token. A refresh token in the answer (Microsoft rotates it on each use; Google may) replaces the grant's at the version read before the access token is used; a write that fails leaves the old one, which both providers keep valid for its lifetime, so nothing is lost. Access tokens are never written anywhere.
- **Lifetimes**, as the providers document them (verified in the spike): Google's refresh token lives until revoked, unused for six months, or the password changes; **in testing mode, seven days**, so every Gmail mailbox goes to `needs_reauth` weekly until Google's verification (choice 6 and "Risks"). Microsoft's lives 90 days, renewed by each use, so a polled mailbox never reaches it; it ends at a password reset, a revocation by the person or their administrator, or a conditional-access policy.
- **Revocation at disconnect.** The grant is read and unsealed before the record's terminal commit (ADR 0026, "Disconnecting", step 3); after the commit, the enclave posts the refresh token to `https://oauth2.googleapis.com/revoke` (best effort, five seconds, once; its outcome only counted). Microsoft offers a client no call that revokes a delegated refresh token without more permission; the disconnect's words tell the person where to remove Permaily ("account.live.com/consent/Manage" for personal accounts, "myapps.microsoft.com" for work accounts). Either way the token was sealed under a key the commit took away.

## IMAP

### The closed list

In the image (`internal/providers/imap/hosts.go`), each with its documented support as read on 2026-11-27, every cell to be confirmed by the spike before it is listed in a release:

| Identifier | Provider | Host (port 993, implicit TLS) | App password | `CONDSTORE` / `QRESYNC` (to verify) | Notes |
|---|---|---|---|---|---|
| `yahoo` | Yahoo Mail | `imap.mail.yahoo.com` | Required for any third-party client | Expected / not expected | Username is the full address |
| `aol` | AOL Mail | `imap.aol.com` | Required | Expected / not expected | Yahoo's infrastructure |
| `icloud` | iCloud Mail | `imap.mail.me.com` | App-specific password, required (two-factor accounts) | Expected / unknown | Username: the address or its local part |
| `fastmail` | Fastmail | `imap.fastmail.com` | Required (app passwords) | Yes / yes | Cyrus; `SPECIAL-USE` |
| `gmx-com` | GMX (gmx.com) | `imap.gmx.com` | IMAP must be turned on in settings; app password when two-factor is on | Unknown / unknown | |
| `gmx-net` | GMX (gmx.net, gmx.de) | `imap.gmx.net` | As above | Unknown / unknown | |
| `zoho` | Zoho Mail (personal, US) | `imap.zoho.com` | App-specific password when two-factor is on; IMAP access turned on | Unknown / unknown | |
| `zoho-eu` | Zoho Mail (EU) | `imap.zoho.eu` | As above | Unknown / unknown | |
| `zoho-pro` | Zoho Mail (organisations) | `imappro.zoho.com` | As above | Unknown / unknown | |

Not listed: Gmail and Outlook over IMAP (they have their API kinds, and Microsoft turned basic authentication off), Proton (needs its Bridge on the person's computer), and any host the person types. A provider leaves the list only by a release; its mailboxes then go to `backoff` with `provider_off` and can be disconnected.

### The connection

- **TLS** to the host on 993 through the allowlisted proxy (`CONNECT imap.mail.yahoo.com:993`), TLS 1.2 or later, the host's name as SNI, verified against the image's roots (WebPKI only: no pinning, no exception, no `InsecureSkipVerify`, no `STARTTLS`, no plain text, no other port).
- **Authentication**: `AUTHENTICATE PLAIN` (or `LOGIN` when the server offers no `AUTH=PLAIN`) with the username and the app password, after the TLS handshake and never before. `AUTHENTICATIONFAILED` once: one more try at the next poll; twice in a row: `needs_reauth`. `LOGINDISABLED` or no way in: `needs_reauth` with the words "turn IMAP access on at <provider>".
- **Read-only, by protocol**: every folder opened with `EXAMINE` (never `SELECT`), every body fetched with `BODY.PEEK[]` (never `BODY[]`, which sets `\Seen`), no `STORE`, `COPY`, `MOVE`, `EXPUNGE`, `APPEND`, `CREATE`, `DELETE`, `RENAME`, `SUBSCRIBE` or `SETMETADATA` command exists in the client's code (a test reads the client's source for them and the played server refuses them, T33).
- **One connection per mailbox at a time**, held only during a poll, closed at its end; at most eight IMAP connections an enclave and two to one host (choice 9), so Permaily's one address is not throttled or blocked by a provider for opening many.
- **The app password and username** are sealed in the grant; typed once in the connect screen (`POST /api/v1/mailboxes/connect/imap {host, username, app_password, history_days, label}`, the body read strictly, at most 1 KiB, the password 8 to 256 printable characters), tried once before anything is made (a failed login makes nothing and answers `imap_login_failed` with the host's sign-in help link from the list), never shown or answered back.

### The client and its bounds

- **Library**: `github.com/emersion/go-imap/v2` (pinned; it is beta, which is why its parser is fuzzed here and its use is wrapped), over a connection wrapped by `internal/providers/imap` that bounds what any answer can make the enclave hold, whatever the library does: a line 64 KiB; one command's answer, literals included, 32 MiB (a message's raw bytes are read from its literal under 25 MiB plus one byte, then the message is skipped as too large); 200 folders; 200,000 UIDs in one `UID SEARCH` or `VANISHED`; each command 60 seconds; a poll 4 minutes. Over any bound the connection is closed and the poll ends with the outcome `bounds` (counted, the mailbox in `backoff`).
- **Fuzz targets** (`scripts/fuzz.sh`, a group `imap`): the wrapped reader with go-imap's decoder over arbitrary server bytes (no panic, no allocation past the bounds), the `LIST`, `FETCH`, `VANISHED` and `ESEARCH` answers, folder names in modified UTF-7.
- **A played server** for tests: go-imap v2's in-memory server (`imapserver` with `imapmemserver`) behind TLS with a test CA, scripted to misbehave (huge literals, endless lines, a `UIDVALIDITY` change, a folder that disappears, `BYE` mid-fetch).

## Egress

| Destination | Port | For | Already allowed |
|---|---|---|---|
| `oauth2.googleapis.com` | 443 | Google's token endpoint and revocation | Yes (ADR 0017) |
| `www.googleapis.com` | 443 | The keys Google's ID tokens are signed with | Yes |
| `gmail.googleapis.com` | 443 | The Gmail API | **New** |
| `login.microsoftonline.com` | 443 | Microsoft's token endpoint and keys | Yes |
| `graph.microsoft.com` | 443 | Microsoft Graph | **New** |
| Each host of the closed list | 993 | IMAP | **New** (nine hosts) |

- **Over the allowlisted proxy, vsock 8000**, not the vendor egress (8004): the list is closed and fixed by the image, which is what the owner asked of the egress. `cmd/permaily-host`'s `defaultEgress` gains them; as for the sign-in providers, a test of `cmd/permaily-enclave` fails when `defaultEgress` and the hosts the provider clients declare (`gmail.Hosts()`, `graph.Hosts()`, `imap.Hosts()`) differ (T45).
- **DNS**: the host resolves the name of a `CONNECT`, as it does for every allowlisted destination; DNS over HTTPS (`cloudflare-dns.com`) stays for DKIM. A host that answers the wrong address reaches a server that cannot prove the name (TLS fails, outcome `refused`): it can deny service, as it can by dropping the connection, and never read or alter what passes.
- **HTTP**: the Gmail and Graph clients use HTTP/2 only (as ADR 0028's model client, so no peer bytes reach a log), no redirect followed, no cookie, `Accept-Encoding: identity`, each call 30 seconds, an answer's body bounded (`history.list` and a Graph page 4 MiB, a raw message 34 MiB of base64url for Gmail's `format=raw`, 25 MiB plus one byte for Graph's `$value`).

## Sync

### The poller

`internal/services/providersync`, run by every enclave, on ADR 0039's pattern:

- **Due index**: every five seconds each enclave lists the due minutes up to now (`providers#due#<minute>`), from the newest it finished, ten minutes back after a start; an hourly repair walks every mailbox record of a provider kind (the mailboxes' index, each record read strictly) and remakes a missing schedule entry or due minute, so a deleted entry delays a mailbox by at most an hour.
- **Lease**: a mailbox due is read; skipped while another enclave's lease lives; leased at the version read for two minutes (`LeaseFor`), renewed at the version read after every page; a renewal refused means another enclave took it (the clock of this one ran slow): the poll stops before its next write. An enclave that dies leaves a lease that ends in two minutes, and the other enclave polls from the last cursor written.
- **Fair shares**: due mailboxes go into bounded ready queues, one per provider kind and within it one per account, taken in turn (round robin), at most 1,024 ready, the rest staying due. Each enclave polls at most 16 mailboxes at once, 4 of one provider kind's API, 8 IMAP connections and 2 to one IMAP host, 1 mailbox of an account (choice 9). A poll of a mailbox with a long import holds a slot for at most four minutes (`PollBudget`), then writes its progress and is due again the next minute, so imports never starve polls of other mailboxes.
- **Interval**: a mailbox polled at `t` is due again at `t` + 5 minutes (`PollEvery`), plus a jitter of up to 30 seconds from its identifier so polls spread; `backoff_until` when later.
- **What a poll does**: reads the record strictly (not `active`, paused, unreadable, or its list not naming it: stop, as delivery does), unwraps the key, opens the grant and the cursor, refreshes the token if needed, reads changes a page at a time ("Per provider"), stores each new message ("Storing"), keeps deletions waiting, writes the cursor at the version read after each page, and once the provider says it is at the head (no more pages for every folder), applies the deletions waiting ("Deletions") and writes the cursor and `last_ok`.

### Per provider

- **Gmail.** `users.history.list?startHistoryId=<cursor>&historyTypes=messageAdded&historyTypes=messageDeleted&historyTypes=labelAdded&historyTypes=labelRemoved&maxResults=500`, page by page; each message added, or whose labels change, is looked at by `users.messages.get?format=minimal` (labels, `internalDate`, `sizeEstimate`) and, when it is in an included folder ("Folders") and not stored, `format=raw`. A message deleted, or given `SPAM` or `TRASH`, or left with no included label, is a deletion waiting. The cursor is the `historyId` the last page returns. `404` on `startHistoryId` (Google keeps history for a limited time, at least about a week as documented) starts a reconcile.
- **Graph.** The folders by `GET /me/mailFolders?includeHiddenFolders=false` and `childFolders` (at most 200, depth 8), excluded by well-known name ("Folders"); for each included folder `GET /me/mailFolders/{id}/messages/delta?$select=id,receivedDateTime,parentFolderId` with `Prefer: IdType="ImmutableId", odata.maxpagesize=100`, following `@odata.nextLink` to its `@odata.deltaLink`, which is the folder's cursor; each new message read with `GET /me/messages/{id}/$value`. `@removed` in a folder is a deletion waiting, unless the same immutable id is seen added in another included folder in the same poll (a move). `410` (`syncStateNotFound`, `resyncRequired`) starts a reconcile of that folder; a folder gone drops its cursor and its messages become deletions waiting, unless seen elsewhere.
- **IMAP.** `LIST` with `SPECIAL-USE` (`RETURN (SPECIAL-USE)` when `LIST-EXTENDED` is offered), at most 200 folders; for each included folder `EXAMINE` (with `(CONDSTORE)` when offered, `(QRESYNC (<uidvalidity> <highestmodseq>))` when `QRESYNC` is offered and enabled); a `UIDVALIDITY` that changed starts a reconcile of the folder. New messages are `UID FETCH <uidnext>:* (UID INTERNALDATE RFC822.SIZE)`, then `BODY.PEEK[]` each. Deletions: with `QRESYNC`, the `VANISHED (EARLIER)` answer; without it, `UID SEARCH ALL` compared with the locations kept (`pid#` `locations`) for that folder (bounded at 200,000 UIDs a folder; past it the folder's deletions are not mirrored and the poll says so, outcome `bounds`).
- **Reconcile** (any provider, a folder or the whole mailbox): list every message identifier in the window (`messages.list?q=after:<epoch>` with the label filters; a fresh delta of the folder; `UID SEARCH SINCE`), store what is missing, and make every message Permaily holds in that scope and did not see a deletion waiting. The window is the mailbox's: from `connected_at` minus `history_days`, and never past the retention (below). Reconciles are bounded like an import and counted.

### Folders

| Kind | Read | Not read |
|---|---|---|
| Gmail (labels) | A message with `INBOX`, `IMPORTANT`, `STARRED`, `CATEGORY_*`, any user label, or no label at all (archived) | A message with `SPAM`, `TRASH`, `DRAFT` or `CHAT`; a message with `SENT` and without `INBOX` (a message sent to oneself, which carries both, is read) |
| Graph (well-known names) | `inbox`, `archive`, every folder the person made, and their children | `junkemail`, `deleteditems` and its children, `sentitems`, `drafts`, `outbox`, `conversationhistory`, `syncissues` and its children, `clutter`, `recoverableitemsdeletions`, search folders |
| IMAP (`SPECIAL-USE`, RFC 6154) | `INBOX`, `\Archive`, every other folder | `\Junk`, `\Trash`, `\Sent`, `\Drafts`, `\All` and `\Flagged` (virtual folders that repeat others), `\NoSelect` and `\NonExistent` folders. A server without `SPECIAL-USE`: the provider's folder names pinned in the list (`Bulk Mail`, `Spam`, `Junk`, `Trash`, `Deleted Messages`, `Sent`, `Sent Messages`, `Draft`, `Drafts`), compared after decoding |

A message in an included and an excluded folder at once (Gmail labels; IMAP copies) is read: being anywhere the person keeps mail is enough. A message moved from an included folder into spam or trash is a deletion (decision 10 says deleted; the person moving it to spam says the same thing for Permaily's purposes; choice 4).

### Storing

- **One path for every kind.** `mailboxes.Service.Import(ctx, id, msg)` runs ADR 0026's sequence for one mailbox from its step 2.1 (read the record and the list again; unwrap the key; the settings), then the claim, the writes in their order, the mark, the arrivals hint (ADR 0039's `inbox.Arrivals`) and `receiving_since` on the first message, under a context of `mailboxes.IngestDeadline` (one minute) from that read, so ADR 0026's `SweepGrace` covers an import running across a disconnect exactly as it covers a delivery (T21). DKIM is checked and the message parsed exactly as delivery does, the envelope sender empty (the provider gave none; `StoredMeta.Source` says `gmail`, `outlook` or `imap`, so the app and the query's variables can tell).
- **The claim's key** is `seen#` with an HMAC, under the key derived from the mailbox key that ADR 0026 uses, of a domain-separated string: `gmail\0<message id>`, `graph\0<immutable id>`, `imap\0<SHA-256 of the raw message>`. A provider claim's entry lives until the message expires, not seven days, so a reconcile or a cursor put back months later finds the message stored and stores nothing (T18). The `pid#` entry is written in the same step as the search entry.
- **Identifiers and times.** A message dated (by the provider's `internalDate`, `receivedDateTime` or `INTERNALDATE`) before `connected_at` is history: its identifier and `received_at` are made from that date (clamped to the window), so the inbox lists history in order, and it does not call `inbox.Arrivals` (a vendor is told of new mail, not of an import; choice 7). A message dated at or after `connected_at` is new mail: identifier and `received_at` from when Permaily stores it, as for forwarded mail, so ADR 0039's cursor sees it as new.
- **Retention and expiry**: the mailbox's retention (ADR 0026: 90 days by default, 7 to 365) counted from `received_at`. A history message whose `received_at` plus the retention is already past is not imported; the connect screen offers only the history choices that fit the retention, and the retention choices offered for a provider mailbox are those at least as long as its `history_days` (choice 5). A message deleted by retention while still at the provider is not imported again: its claim and `pid#` entry live until the same expiry, and no reconcile window reaches before it.
- **Too large**: a message over 25 MiB (`sizeEstimate`, Graph's size, `RFC822.SIZE`, or the bytes read) is not stored; the schedule entry counts it, and the mailbox page says "2 messages were too large to copy (over 25 MiB)". Choice 8.
- **Memory**: an import takes its bytes from the SMTP session's budget of bytes held at once (ADR 0026, "Admission": 128 MiB an enclave), waiting at most ten seconds and then ending the poll (`busy`, due again in a minute), so ADR 0026's arithmetic of 1,032 MiB under the 1,536 MiB the runtime is held to is unchanged. At most two imports of a raw message at once an enclave.
- **A person's delete** in Permaily deletes Permaily's copy only (never anything at the provider) and marks the `pid#` entry `deleted_by_person`, so the poller does not store it again while the provider keeps it; the inbox's words for a provider mailbox say so ("Deletes Permaily's copy; it stays in your Gmail").

### Deletions

- Deletions seen during a poll are kept in the cursor (at most 10,000; past it the poll ends early and the next goes on) and applied only once the poll reached the head of every folder, after removing any identity that was also seen added or present in an included folder in the same poll. For IMAP the identity is the raw message's digest, so a move between folders (`EXPUNGE` in one, a new UID in the other) is the same message, whose `locations` change.
- Applying one is the inbox's existing delete (`DeleteMessage`: the raw object, then the item, after the entries of its kept decisions, ADR 0028 and 0031), then the claim and the `pid#` entry. Receipts that name the message keep naming it (they say what left, as for a message the person deleted); a webhook notice in flight finds `not_found` when the vendor fetches; a kept decision goes with the message, as decision 10 says.
- A message deleted at the provider during a reconcile that a bound cut short is not deleted: only a scope listed whole makes deletions.

### History import

- Newest first, a page at a time, in each provider's own order (`messages.list` is newest first; Graph's initial delta is filtered with `receivedDateTime ge <window start>` and its pages are stored as they come; IMAP reads UIDs from the highest down per folder, folders in turn). The window is `connected_at` minus `history_days`.
- Bounds a mailbox: 20,000 messages, 10 GiB, 24 hours from the mailbox's making (choice 10). Reaching one ends the import ("Imported the newest 20,000 messages of the last 90 days"). New mail is polled meanwhile in the same poll, first.
- Quota (as documented on 2026-11-27, confirmed by the spike): Gmail counts 5 units a `messages.get` and 2 a `history.list` against 15,000 units a user a minute and a project's daily limit; an import page of 100 raw messages is 500 units, so the import is held to 2,500 units a user a minute (a fifth) and to the project's share below. Graph allows a mailbox 10,000 requests in ten minutes and four at once per application; the import makes one at a time per mailbox and at most 600 a minute.

### Quotas and backoff

| Answer | What the poller does |
|---|---|
| 429, Gmail's 403 `rateLimitExceeded` or `userRateLimitExceeded`, Graph's 503 or 504 with `Retry-After` | `backoff` until `Retry-After` (at most an hour) or the backoff's next step (1, 5, 15, 60 minutes, then hourly); counted by provider |
| 5xx, a timeout, a reset, IMAP `BYE` or `UNAVAILABLE` | The same |
| Gmail's 403 `dailyLimitExceeded` (the project's) | Every Gmail mailbox of the enclave waits until the next hour; `/status` counts it, the operator's alarm (`scripts/verify-cron.sh` already warns on `/status`) |
| 401 | Refresh once; again 401: `needs_reauth` |
| `invalid_grant` at refresh, `AUTHENTICATIONFAILED` twice | `needs_reauth` |
| 404 on `startHistoryId`, Graph 410, `UIDVALIDITY` changed | Reconcile |
| A bound passed | `backoff` with `bounds`, counted |

A project-wide budget keeps one project's quota safe from one enclave's bug: at most 600 Gmail calls a minute an enclave, 600 Graph calls a minute an enclave (compiled in).

## What changes for the person

### Screens (docs/web.md gains "Mailboxes from a provider")

- **Adding a mailbox** asks first how: "Forward mail to Permaily" (today's flow) or "Connect Gmail", "Connect Outlook", "Connect another provider (IMAP)". The provider choices say, before the person leaves: what Permaily will read ("all your mail except spam, trash, sent and drafts"), that it never changes or sends anything, how far back (0, 30 or 90 days; 30 chosen), how often (every five minutes), that the copy is sealed in the enclave and deleted when they disconnect, and, during Google's testing mode, "While Permaily is in testing with Google, you'll need to connect again every 7 days".
- **IMAP**: the provider picked from the closed list (logos, names), then the username and the app password, with the provider's own page on making an app password linked as text and its steps in plain words.
- **A provider mailbox's page**: the kind, the provider account's address, the state ("Importing", "Up to date, checked 3 minutes ago", "Waiting: Gmail asked us to slow down", "Can't read this mailbox: connect again"), when history goes back to, messages too large, and the buttons "Connect again" (in `needs_reauth`) and "Disconnect".
- **Disconnect**, with a fresh proof as today, says: Permaily's copy is deleted; nothing in the mailbox at the provider changes; for Gmail, Permaily's access is revoked; for Outlook, "remove Permaily at account.live.com/consent/Manage (or ask your administrator)"; for IMAP, "delete the app password at <provider>".
- **The inbox** is the same, with a provider mailbox's delete saying it deletes Permaily's copy only.

### Notices by mail

- "A mailbox was added" (ADR 0026) names the kind and the provider account's domain, never the address.
- **New**: "Permaily can't read one of your mailboxes", when a mailbox enters `needs_reauth`: fixed words, the kind, the mailbox's label is not repeated (typed by the person; ADR 0017's rule), no link, sent once per transition by the call that committed it. During Google's testing mode this comes every week for every Gmail mailbox; the words say why.
- "A mailbox was disconnected" (ADR 0026) adds, for Outlook and IMAP, where to remove Permaily's access.

### The privacy page and terms

Each to be classified and worded by the owner. TODO LEGAL: owner's review.

- What is read: the person's mail at the provider, every folder but spam, trash, sent and drafts, from `history_days` before connecting, every five minutes, read-only; the address of the provider account.
- What is kept: the sealed copy, as forwarded mail; the refresh token or the app password, sealed, deleted at disconnect; Google's token revoked at disconnect.
- Deletions mirrored: a message deleted at the provider is deleted here, with the decisions kept about it; receipts keep naming it.
- Google API Services User Data Policy: the privacy page carries Google's required statement that Permaily's use and transfer of information received from Google APIs adheres to it, including the Limited Use requirements; whether sending a Gmail message's subject and text to OpenAI for a rule or a connection (ADR 0028) and disclosing approved parts to a vendor the person connects are "user-facing features" the person asked for under Limited Use is the owner's to settle with counsel before Google's verification. TODO LEGAL: owner's review.
- Microsoft's terms for applications using Graph (the publisher's terms and privacy URLs on the application registration).
- **Google, Microsoft and the IMAP providers are not subprocessors**: the page says "the provider you connect is your own email provider; Permaily reads from it on your instruction, with your permission, and does not send it anything of yours". `/subprocessors/` gains a sentence saying so, as it says of vendors (docs/data.md, "Subprocessors").
- The terms: connecting a mailbox the person is authorised to read; what happens when a provider stops Permaily's access; testing mode. TODO LEGAL: owner's review.

## The threat model

- **What the host sees**: that the enclave connects to `gmail.googleapis.com`, `graph.microsoft.com` or an IMAP host, when, for how long and how many bytes each way: so which provider kinds are in use, roughly how many mailboxes, when they poll, and sizes that hint at a message's size. It cannot open the TLS, and it cannot tell which person a connection serves beyond timing. It can delay or drop connections (polls back off; mail waits at the provider, nothing is lost since the provider keeps it). It cannot point the enclave at another server: TLS verifies the provider's name against the image's roots.
- **Refresh tokens and app passwords never leave the enclave**: sealed under the mailbox's key, unsealed only inside the enclave, sent only to the provider's token endpoint or IMAP host over TLS, never logged (the scrubber learns them: a refresh token's and an app password's shape are dropped from any log line, T46), never in an answer. Whoever writes the table can delete a grant (the mailbox needs reconnecting) or put back an earlier one (the same account, or nothing after a disconnect).
- **The operator** registered the applications and knows their client secrets; with a secret alone nobody reads a mailbox (a refresh token is needed, and only the enclave has those). The operator can turn a provider off (removing the secret) and can, in Google's or Microsoft's console, suspend the application: denial of service, which the mailbox's state shows. The operator cannot add scopes: the scopes requested are in the enclave's code, and a token with any other scope is refused (T9).
- **A compromised Google or Microsoft account of the person**: whoever holds it reads the mail at the provider already; through Permaily they can put mail in (by sending it to the account), as anyone can, and delete mail from Permaily's copy (by deleting it at the provider), which mirrored deletions make possible by design (decision 10), so a vendor's later fetch does not find it. Receipts of what already left stay.
- **A hostile or compromised IMAP server** on the list: bounded reads, fuzzed parser, no command that writes, TLS verified; what it can do is send hostile mail, as any sender can, and the parsing and the HTML frame are ADR 0026's.
- **Google's testing mode**: at most 100 test users, each added by hand by the owner in Google's console (so Google, and whoever runs that console, know those addresses as test users); refresh tokens that end after seven days. Leaving it needs Google's verification of a restricted scope and a CASA assessment (yearly), with costs and an assessor seeing the deployment's security evidence; the owner decides when.
- **Microsoft's work tenants**: an administrator may need to approve; a tenant's policies may end tokens at any time (`needs_reauth`). Publisher verification is needed for users in many tenants to consent at all.
- **Abuse from Permaily's address**: polls are bounded per host and per enclave; IMAP is a closed list; a person cannot make the enclave connect anywhere they choose.
- docs/threat-model.md gains a short section pointing here with the pull request that builds it (pull request 8 fills it in).

## What /status and permaily-verify show

`/status` gains `providers` (`attestation.ProvidersStatus`, committed with the seams): `kinds: ["gmail", "outlook", "imap"]`; `scopes: {gmail: ["openid", "email", "https://www.googleapis.com/auth/gmail.readonly"], outlook: ["openid", "email", "offline_access", "https://graph.microsoft.com/Mail.Read"]}`; `read_only: true`; `push: false`; `poll_seconds: 300`; `history_days: [0, 30, 90]`; `client_ids: {google, microsoft}` (from the image) and whether each secret is set; `hosts` (every host and port the provider clients dial); `imap: [{id, host, port: 993, tls: "implicit"}]`; `token_storage: "sealed_per_mailbox"`; and this enclave's counts: mailboxes by kind and sync state, polls by outcome, messages stored, deletions mirrored, too large, revocations by outcome. Never an address, an account, a mailbox identifier or a host's error text.

`permaily-verify` prints the section and fails when a scope is not in its pinned read-only set, `read_only` is false, `push` is true, `poll_seconds` is under 300, an IMAP entry's port is not 993 or its TLS not implicit, or a host is not in its pinned list (pull request 8).

## Choices made for the owner (to confirm)

1. **Client identifiers in the image**, secrets through the operator's route. The alternative (both through the route, as sign-in) keeps a release out of changing the application, and lets the operator point people at another application unseen.
2. **Separate Google and Microsoft applications for mail**, not the sign-in ones.
3. **One provider account, one mailbox of the deployment** (refused while another mailbox reads it). The alternative allows two Permaily accounts to read one Gmail account, doubling quota and blurring whose mail it is.
4. **A move to spam or trash is a deletion** for Permaily, and the copy goes.
5. **Retention as forwarding's** (7 to 365 days, 90 by default), counted from the provider's date for history; history choices only up to the retention, retention choices only from the history. Not asked yet by the owner ("retention of provider mail vs forwarding").
6. **Every Gmail mailbox reconnects weekly during testing mode**, with a notice each time; nothing works around Google's seven days.
7. **History imports do not notify webhooks**; mail after connecting does.
8. **Messages over 25 MiB are skipped and counted**, as the MX refuses them.
9. **Concurrency**: 16 polls an enclave, 4 of one API kind, 8 IMAP connections, 2 to one IMAP host, 1 per account.
10. **Import bounds**: 20,000 messages, 10 GiB, 24 hours a mailbox.
11. **A person's delete in Permaily deletes only Permaily's copy**, and the message is not imported again.
12. **The IMAP list** above, nine hosts of six providers, each kept only if the spike confirms it.
13. **No fresh proof to connect**; a fresh proof to disconnect (as today).
14. **What disconnecting does to the copy**: deleted, as forwarding (not asked yet; assumed in the owner's notes).
15. **`needs_reauth` is told by mail once per transition**, and shown in the app.
16. **Sent mail sent to oneself is read** in Gmail (it carries `INBOX`).
17. **The cursor is a sealed entry, not a strict record** (the brief asked whether it should be strict; "Decision in short" says why not).

## The spike, before building

What must be proven against the real providers, each with what it decides. The spike runs with the owner's applications and test accounts the owner makes for it (never a person's mailbox), from a workstation, with no Permaily code in production. A spike in `spikes/providers/` (its own readme, never imported or deployed) is written then; none is in this pull request, because what it would prove without credentials (the sync engine against played servers) is what pull requests 3 to 6 test anyway.

1. **Google's trip in testing mode**: the consent screen with `gmail.readonly` unverified, a test user consenting, the refresh token issued with `prompt=consent`, the granted `scope` string when the person unticks Gmail, the refresh token ending after seven days (or an earlier date if Google changed it), revocation.
2. **Gmail's history**: `history.list` across label changes, archive, spam, trash, delete forever and a move back from trash; how long a `historyId` stays valid (keep a test account idle and read it after 1, 7 and 14 days); the `404`; quota units charged as documented.
3. **Gmail's raw**: `format=raw` of a 24 MiB message; that `messages.get` does not mark it read.
4. **Microsoft's trip**: a personal account and a work tenant (the owner's or a developer tenant), user consent with and without publisher verification, the AADSTS codes for "admin approval needed", the rotated refresh token, the ID token's `email` and `preferred_username` for both.
5. **Graph's delta on every folder**: immutable ids across a move between folders, to junk and to deleted items, permanent deletion; `$filter=receivedDateTime ge` on the first delta; the `deltaLink`'s lifetime and `410`; folder deltas; `$value` of a large message; that reading does not mark read; throttling headers.
6. **IMAP on each listed provider**: login with an app password, `SPECIAL-USE`, `CONDSTORE`, `QRESYNC`, `UIDVALIDITY` stability, `EXAMINE` and `BODY.PEEK[]` leaving `\Seen` unset, connection limits per account and per address, folder names when `SPECIAL-USE` is missing. A provider that fails leaves the list.
7. **Two enclaves**: none needed against the providers (pull request 6's tests cover it with played servers).

**What the owner must create before the spike**:

- **Google Cloud**: a project for Permaily's mail (`permaily-mail`); the Gmail API enabled; the OAuth consent screen: user type External, publishing status Testing, app name "Permaily", support email, logo, home page `https://permaily.com`, privacy policy and terms URLs, authorised domain `permaily.com` (verified in Search Console), developer contact; scopes `openid`, `email`, `.../auth/gmail.readonly`; test users: the owner's test accounts; an OAuth client of type Web application with the authorised redirect URI `https://app.permaily.com/api/v1/mailboxes/connect/callback` (and `http://localhost:8443/api/v1/mailboxes/connect/callback` for dev mode); its client identifier into `deploy/enclave.json` with pull request 8, its secret kept for `scripts/provider.sh`. Later, for real users: the app's verification with the restricted scope, a demo video, and the CASA assessment.
- **Microsoft Entra**: an application registration (in the owner's tenant), supported account types "Accounts in any organizational directory and personal Microsoft accounts"; platform Web with the same redirect URIs; a client secret (24 months at most, its end in docs/operations.md); delegated permissions `openid`, `email`, `offline_access`, `Mail.Read` (Microsoft Graph), no application permissions; publisher verification (a Microsoft AI Cloud Partner Program identifier for the organisation) so users of other tenants can consent; the publisher's domain `permaily.com`; terms and privacy URLs.
- **IMAP test accounts** at each listed provider, with two-factor on and an app password each.

## Tests

T for this record. Each is a test of the pull request named in "Implementation pull requests" (in brackets). Played servers: `gmailtest`, `graphtest` (HTTPS with certificates for the real names under a test CA, HTTP/2, scripted answers and failures) and `imaptest` (go-imap's in-memory server behind TLS, scripted).

- **T1** [1] A record without `kind` reads as `forwarding`; a provider record has an empty `rcpt` and no address pointer; `RCPT TO` of any address never finds a provider mailbox.
- **T2** [1] Making a provider mailbox with four mailboxes through A and B at once: one succeeds, the other `too_many_mailboxes`; ten creations in thirty days counted across kinds.
- **T3** [1] One Google account connected through A and B at once to two mailboxes: one pointer write wins; the other answers `provider_account_in_use` and its reservation is given up (tombstone, identifier out of the list).
- **T4** [1] The provider account's pointer put back after a disconnect, or copied under another `account_ref`: a new connection of that account is refused only while a live record names it; the pointer leads to a record that refuses.
- **T5** [1] Disconnecting a provider mailbox: the terminal commit before the grant could be read again; after it, no enclave opens the grant; the sweep deletes `grant`, `cursor`, `pid#`, `seen#`, messages and entries; the provider account can be connected again.
- **T6** [1] The account's removal ends a provider mailbox as ADR 0026's M20; a poll in flight writes nothing after `IngestDeadline` from its read.
- **T7** [2] The trip: start sets the cookie and the entry; the callback without the cookie, with another browser's cookie, with a used state, after ten minutes, or with a state never made: nothing made, `provider_failed`, no entry left.
- **T8** [2] The token exchange with the client secret and the PKCE verifier; an ID token with another audience, another nonce, an old `iat`, or another issuer: refused.
- **T9** [2] Granted scopes: Gmail without `gmail.readonly` (unticked): `scope_not_granted`, nothing stored; a Microsoft token with `Mail.ReadWrite` or `Mail.Send` in its `scope`: refused and never stored; exactly the read-only set: accepted.
- **T10** [2] Microsoft's `error=access_denied` with AADSTS65001 or AADSTS90094: `admin_approval_needed`; any other provider error: `provider_failed`; never the provider's text in the redirect or a log.
- **T11** [2] Connecting again with another Google account: `provider_account_mismatch`, the mailbox still `needs_reauth`, the grant unchanged.
- **T12** [2] The callback with no session cookie (Google's navigation): the trip's account is used; with a session of another account: refused.
- **T13** [2] Refresh: a rotated refresh token is written at the version read before the access token is used; a write that fails keeps the old grant and the poll goes on; an access token is never written to the store (the fake store's every write checked for it).
- **T14** [2] A grant put back to an earlier one of the same account: the poll works or goes to `needs_reauth`; a grant whose `subject` is not the record's `account_ref`: refused, `needs_reauth`, nothing read.
- **T15** [2] Disconnect revokes at Google with the token read before the commit (the played Google sees one revoke); a revoke that fails or times out changes nothing of the disconnect's answer.
- **T16** [2] The operator's route: a secret for a client identifier that is not the image's: refused; set: the provider is offered; removed: not offered, its mailboxes `backoff` with `provider_off`, not `needs_reauth`.
- **T17** [6] One message seen through A and B at once (A's lease lost mid-poll to B): stored once; the claim's key is the provider's identity, not the bytes' sender.
- **T18** [6] The cursor put back to an earlier value (Gmail, Graph, IMAP): the next poll reads changes again and stores nothing twice; a message the person deleted in Permaily is not stored again.
- **T19** [6] The cursor deleted: a reconcile; nothing stored twice; deletions only for what the provider no longer lists within the window.
- **T20** [6] A poll stopped after each write of a message (`Interrupt`): the next poll completes it, one message.
- **T21** [6] A poll reads the record, then B disconnects: the poll's writes end within `IngestDeadline`; the sweep's last pass after `SweepGrace` deletes them (ADR 0026's M7 for imports).
- **T22** [6] Leases: A holds a mailbox; B skips it; A dies; after two minutes B polls from A's last cursor; A's renewal after B took it is refused and A writes nothing more.
- **T23** [6] Fair shares: 1,000 Gmail mailboxes due and one Outlook mailbox: the Outlook one starts within one pass; one account's 5 mailboxes take one slot at a time; a 24-hour import never holds a slot more than `PollBudget`.
- **T24** [6] Backoff: 429 with `Retry-After: 120` waits 120 s; without it 1, 5, 15, 60 minutes then hourly; `Retry-After` of a day is held to an hour; success returns to the state before.
- **T25** [6] `needs_reauth`: `invalid_grant`, a second 401, two `AUTHENTICATIONFAILED`; one notice through A and B at once; no poll while in it; connecting again resumes from the cursor.
- **T26** [6] Paused, closing, blocked, terms not agreed: paused not polled; the others polled, as forwarding receives.
- **T27** [3] Gmail: history of added, labelled `SPAM`, `TRASH`, unlabelled to archive, deleted, moved back from trash: stored, deleted, kept, deleted, stored again with a new identity of the same message id (stored once).
- **T28** [3] Gmail: `SENT` without `INBOX` not stored; `SENT` with `INBOX` stored; `DRAFT`, `CHAT` not stored.
- **T29** [3] Gmail: `404` on `startHistoryId`: a reconcile over the window lists, stores what is missing, deletes what is gone.
- **T30** [4] Graph: a move between two included folders in one poll (removed in one, added in the other): no deletion, one message; a move to `junkemail`: deleted at the poll's end.
- **T31** [4] Graph: excluded well-known folders and their children never read; 201 folders: 200 read and the outcome says so.
- **T32** [4] Graph: `410` on a folder's delta: that folder reconciled; others untouched.
- **T33** [5] IMAP: the client's source holds none of `STORE`, `COPY`, `MOVE`, `EXPUNGE`, `APPEND`, `CREATE`, `DELETE`, `RENAME`, `SUBSCRIBE`, `SELECT`, `BODY[` (not `PEEK`); the played server refuses each and a full poll sends none; `\Seen` is never set on the played server.
- **T34** [5] IMAP: TLS with a certificate for another name, a self-signed one, TLS 1.1, or a server asking `STARTTLS` on 993: refused, nothing sent, no password written to the wire.
- **T35** [5] IMAP: a line of 65 KiB, a literal of 33 MiB, 201 folders, 200,001 UIDs, a command silent for 61 seconds: the connection closed, outcome `bounds` or `timeout`, memory held under the bound (measured by the test's allocator).
- **T36** [5] IMAP: a move between folders (expunge in one, new UID in the other) in one poll: one message, its locations changed; a `UIDVALIDITY` change: the folder reconciled by digest, nothing stored twice.
- **T37** [5] IMAP: deletions with `QRESYNC` (`VANISHED`), with `CONDSTORE` only, and with neither (`UID SEARCH ALL`): the same messages deleted.
- **T38** [5] IMAP: a server without `SPECIAL-USE`: the pinned names excluded; folder names in modified UTF-7 decoded before comparison.
- **T39** [5] Fuzz: the wrapped reader and decoder over arbitrary bytes: no panic, no allocation past the bounds (`scripts/fuzz.sh`'s `imap` group).
- **T40** [1] Every key a provider mailbox's life writes (listed by a run over the played servers) is deleted by the sweep; a forwarding mailbox's sweep unchanged.
- **T41** [6] History: 0 days imports nothing before `connected_at`; 30 and 90 days import newest first; a message older than its retention not imported; the bounds (20,000 messages, 10 GiB, 24 hours) end the import with its count; new mail stored meanwhile.
- **T42** [6] Identifiers: history messages listed by their provider dates; new mail by the time stored; history calls no `inbox.Arrivals`, new mail calls it once.
- **T43** [6] A message over 25 MiB not stored and counted, from the provider's size and from the bytes read when the size lied.
- **T44** [6] Deletions waiting: a poll cut before the head of every folder applies none; the next completes and applies them once; 10,001 waiting ends the poll early.
- **T45** [8] `cmd/permaily-host`'s `defaultEgress` equals the hosts the composed clients declare, IMAP hosts with `:993`; no provider client dials the vendor egress.
- **T46** [6] Nothing of a refresh token, an access token, an app password, an address, a username, a subject or a provider's error text reaches a log line or an error answered (the logs of a full run over the played servers searched for each).
- **T47** [6] Memory: an import waits on the SMTP budget; with the budget held by sessions it ends the poll `busy` within ten seconds, nothing half stored.
- **T48** [6] Two enclaves' quotas: at most 600 Gmail calls a minute an enclave; `dailyLimitExceeded` holds every Gmail mailbox of the enclave until the next hour.
- **T49** [7] Web: the add screen offers forwarding and the providers configured (`GET` of what is set), Gmail's testing-mode sentence while `testing` is set, the history choices that fit the retention.
- **T50** [7] Web: the IMAP form against the contract: the provider list from the server, the password field never echoed or kept after the answer, `imap_login_failed`'s words.
- **T51** [7] Web: a provider mailbox's page in every sync state, "Connect again" in `needs_reauth`, the disconnect's words for each kind.
- **T52** [7] Web: the callback's fixed reasons shown with their words; an unknown reason shown as `provider_failed`.
- **T53** [8] `/status`'s `providers` with every key (counts at zero) and nothing else; `permaily-verify` fails on a write scope, `push: true`, `poll_seconds` 60, an IMAP port 143, a host outside its list.
- **T54** [8] `TestMailEndToEnd`'s provider half: through two dev enclaves and the three played servers, connect, import 30 days, a new message stored and a webhook notice sent, a deletion mirrored, a rule holding a message, a connection's read and its receipt, disconnect.
- **T55** [2] Connecting starts only for an open, agreed account with a free slot, and checks the slot again at the callback.
- **T56** [6] The hourly repair remakes a deleted schedule entry and due minute from the records within the hour; a schedule entry for a mailbox whose record ended is dropped.
- **T57** [3, 4] HTTP/2 only, no redirect followed (a 302 from the played Gmail or Graph is an error), answers past their bounds refused, 30 seconds a call.
- **T58** [6] A provider whose secret was removed: its mailboxes in `backoff` with `provider_off`, no `needs_reauth`, no notice.
- **T59** [1] The notice "a mailbox was added" names the kind and not the address; "can't read one of your mailboxes" holds no label, address, link or code.
- **T60** [2] The IMAP connect: one login tried before anything is made; a failed one makes nothing; the password never in the answer, a log or the trip entry.
- **T61** [6] A person's delete of a provider message: Permaily's copy gone, nothing sent to the provider (the played servers see no write), not stored again on the next poll or reconcile.
- **T62** [8] The staging check: the owner's Google test account, a Microsoft personal and a work account, and one IMAP account of each listed provider connected to a staging image; history imported; a new message within ten minutes; a deletion and a move mirrored; disconnect and revocation; the counts on `/status`.
- **T63** [6] Polling interval: each mailbox polled every 300 to 330 seconds over an hour with two enclaves, never twice in one interval.
- **T64** [1] Making a provider mailbox stopped after each step (pointer, `CreateNew`, grant, cursor, schedule): no mailbox that polls without a grant, no grant without a mailbox (the sweep or the reservation's ten minutes end it), the provider account free again.

## Implementation pull requests

Nine, after the design review and the spike. The design and its seams go first and alone; then 1 to 5 are written at the same time (2 merging after 1); 6 after 1, with 3 to 5 merging into it as they come; 7 alongside 6 against the contract; the image last.

| # | What | Owns (may edit) | Seams it consumes / produces | Tests | Weeks |
|---|---|---|---|---|---|
| 0 | **The design, the contract and the seams**: this record revised after its review; `internal/contracts/mailboxes.go` (`MailboxKind`, `Mailbox.kind`, `provider`, `sync`; `ConnectStart`, `ConnectIMAP`, the fixed reasons), the same in `web/src/lib/api.ts`; `internal/providers/providers.go` (`Source`: `Changes(ctx, cursor) (Page, error)`, `Raw(ctx, id) (io.Reader, size, error)`, `Reconcile`, `Hosts()`; `Message`, `Page`, `Outcome`); `providerstest.Contract` that every client passes against its played server; `mailboxes.Importer` (`Import`), `providersync`'s `Store` and `Lease` interfaces with fakes; `attestation.ProvidersStatus` | `internal/contracts`, `internal/providers` (interfaces only), `web/src/lib/api.ts`, `internal/services/mailboxes` (interfaces only), `pkg/attestation` | Produces every seam below | Compiles; the contract's shapes in Go and TypeScript compared | 0.5 |
| 1 | **The record**: `kind`, `account_ref`, `history_days`, `connected_at`, the list's `ref`, the provider account's pointer, the grant and cursor entries, the schedule entry made with the mailbox, the sweep's prefixes, disconnect and removal for provider kinds, `Import` | `internal/services/mailboxes`, `internal/inbox` (claim keys, `pid#`, `StoredMeta.Source`) | Produces `Importer`, `GrantStore`; consumes the store | T1 to T6, T40, T59, T64 | 1.5 |
| 2 | **OAuth as a client, and the routes**: the trip (start, callback, cookie, entry), the exchange, the checks, refresh and rotation, revocation, the operator's route and `scripts/provider.sh`'s `google-mail`/`microsoft-mail`, the IMAP connect route | `internal/providers/oauth`, `internal/httpapi/mailboxes_connect.go`, `internal/httpapi/crosssite.go`, `scripts/provider.sh`, `internal/idp` (only to export its ID-token check) | Consumes `GrantStore`, `idp`'s verifier; produces `providers.Tokens` | T7 to T16, T55, T60 | 1.5 |
| 3 | **Gmail's client** and `gmailtest` | `internal/providers/gmail` | `providers.Source` | T27 to T29, T57, the contract | 1 |
| 4 | **Graph's client** and `graphtest` | `internal/providers/graph` | `providers.Source` | T30 to T32, T57, the contract | 1 |
| 5 | **IMAP's client**: the closed list, the bounded connection, go-imap v2, `imaptest`, the fuzz group | `internal/providers/imap`, `scripts/fuzz.sh` (a group), `go.mod` | `providers.Source` | T33 to T39, the contract | 1.5 |
| 6 | **The sync engine**: due index, lease, fair shares, polls, storing, deletions, history, backoff, quotas, the repair, the notices | `internal/services/providersync` | Consumes `Importer`, `providers.Source`, `providers.Tokens`, `notices`; produces the runner | T17 to T26, T41 to T44, T46 to T48, T56, T58, T61, T63 | 2 |
| 7 | **Web**: adding by kind, the trip's hand-off, the IMAP form, a provider mailbox's page and states, connect again, disconnect's words, the inbox's delete words; docs/web.md | `web/src/pages/mailboxes/`, `web/src/lib/` | The contract | T49 to T52 | 1 |
| 8 | **The image**: `deploy/enclave.json`'s `mail_providers`, the composition (`cmd/permaily-enclave/providers.go`), `defaultEgress`, `/status`'s `providers` and `permaily-verify`, `TestMailEndToEnd`'s provider half, docs/threat-model.md, docs/data.md, docs/legal.md, docs/operations.md (setting the secrets, rotating Microsoft's, Google's testing users), docs/verification.md, docs/acceptance.md, the privacy page and terms for the owner's review, the staging check; then the release | `cmd/`, `deploy/enclave.json`, `pkg/attestation`'s check, docs, `site/` (legal pages) | Composes everything | T45, T53, T54, T62 | 1 |

**Estimate**: the spike one week (with the owner's applications ready), the build about eleven weeks for one person (the table's sum), about twelve in all. Pull requests 3, 4 and 5 are independent and can be written by three people at once, which brings the calendar to about seven weeks.

## Risks

- **Google's seven-day refresh tokens in testing mode** make every Gmail mailbox reconnect weekly: acceptable for test users, not for real ones. Leaving testing needs verification of a restricted scope and a CASA assessment (weeks to months, a yearly cost); until then Gmail is for at most 100 people the owner adds by hand.
- **Limited Use and the model**: whether Google accepts sending a Gmail message's text to OpenAI for a rule or a connection under its Limited Use requirements is not settled; if not, Gmail mailboxes would need the model off, which ADR 0031's default rule does not allow (nothing leaves while the model is off). TODO LEGAL: owner's review, before Google's verification.
- **Microsoft publisher verification** may take time; without it many work accounts cannot consent.
- **IMAP providers change** their app-password rules and limits; Permaily's single address may be throttled. A provider leaves the list by a release.
- **go-imap v2 is beta**: fuzzing and the bounded wrapper hold what it may do wrong; a later version may change its API.
- **Quotas are documented, not contracted**: Google and Microsoft may lower them; backoff and the project-wide budget keep Permaily under them, at the cost of delay.
- **A cursor or claim lost** costs a reconcile, never a duplicate; a claim deleted by whoever writes the table can store a message twice, as a `seen#` deleted does for forwarding (ADR 0026, accepted).
- **Mirrored deletions are a way to remove evidence**: someone who controls the person's provider account can delete mail from Permaily's copy. Receipts stay.
- **Five minutes is not instant**: a webhook for new mail fires up to five minutes (plus a poll's work) after the provider received it.

## What remains to verify

- Every provider fact marked as documented above (lifetimes, quotas, folder names, `CONDSTORE`/`QRESYNC`, `$filter` on Graph's delta, how long a `historyId` lives), in the spike.
- The memory arithmetic under SMTP and imports at once, measured on staging (T62), as ADR 0026's was.
- That 100 Gmail mailboxes and a few hundred Outlook and IMAP mailboxes polled every five minutes fit the enclave's one vCPU, measured on staging with played servers at that count.

## Options considered

- **Push instead of polling** (Gmail's Pub/Sub watch, Graph's change notifications, IMAP `IDLE`): declined by the owner (decision 5). Pub/Sub needs a Google Cloud topic and an inbound endpoint; Graph's notifications need a public endpoint and subscription renewal every three days; `IDLE` needs a connection held per mailbox. Polling every five minutes is simpler, bounded and shared by two enclaves through the store.
- **A strict record for the cursor**: rejected ("Decision in short"); a commit every five minutes per mailbox passes the store's bound in weeks, and going back costs only work.
- **The grant under the root secret** (as the sign-in applications): rejected; a grant must die with its mailbox's key at disconnect, which the root secret does not.
- **The vendor egress for providers**: rejected; it reaches any public name, and the owner asked that the egress stay a closed, verifiable list.
- **Arbitrary IMAP servers**: declined by the owner (decision 7).
- **Reusing the sign-in applications**: rejected (choice 2).
- **Storing only references and reading the provider on demand**: declined by the owner (decision 3); it would put the provider in the path of every read, rule and receipt.
- **Dedupe IMAP by (folder, UIDVALIDITY, UID)** as the brief proposed: rejected as the only key, since a move between folders would delete a message and store it again with a new identifier and without its kept decisions; the UID is kept as a location and the raw message's digest names the message.

## What it rests on

ADR 0016 (strict anchored records; the 10,000 commits a record), 0017 (the providers' applications and their sealed secrets, the sign-in trip and its `SameSite=Lax` cookie, ID-token checks), 0018 (amended: mailboxes by forwarding or by a provider), 0024 (two enclaves), 0026 (the record, the list, the claim, the order of writes, `IngestDeadline` and `SweepGrace`, disconnecting, the sweep), 0028 and 0031 (decisions kept per message and deleted with it), 0029 (secrets only to the attested key), 0030 (the allowlisted egress and the vendor egress), 0039 (the due index, the lease, fair shares, the repair, `inbox.Arrivals`), 0040 (rules, unchanged), 0041 (notices).
