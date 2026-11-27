# Providers spike (ADR 0042)

The prototype behind [ADR 0042](../../docs/decisions/0042-reaching-mail-through-providers-how.md), section "The spike, before building". It is its own Go module, so neither its dependencies nor its code reach the main module, its vendoring or the enclave image. It is never imported or deployed.

This first part needs no credentials. On 2026-11-27 the owner had not yet created the Google project `permaily-mail` or the IMAP test accounts, and Outlook is off (decision 18). So it holds:

- `graphsim/`: a simulated Microsoft identity platform and Graph mail API, plus the tests that act as `graphtest`'s scripts for items 4, 5 and 8 (Microsoft's side). Every scripted answer cites its Microsoft documentation page and is either "as documented" or "not documented (T201)" (`graphsim/scripts.go`).
- `imapwire/`: item 7, with wire transcripts of go-imap v2's client against go-imap's in-memory server and against a server scripted with RFC 7162's own examples (`imapwire/testdata/`).
- `cmd/providerspike/`: the harness for items 1, 2, 3, 6 and 8 (Google's side). It runs against real accounts once they exist (see Runbook). Its offline tests cover its guards and the streaming `format=raw` decoder.

```sh
cd spikes/providers
go test -race ./...            # everything offline; no provider is called
go test ./imapwire -update     # rewrite the transcripts after a deliberate change
```

## Status by item

| Item | What | Status |
|---|---|---|
| 1 | Google's trip in testing mode | Waiting on the owner's accounts; harness ready (`google-trip`, `google-refresh`) |
| 2 | Gmail's history | Waiting on the owner's accounts; harness ready (`gmail-baseline`, `gmail-history`, `gmail-history-age`) |
| 3 | Gmail's messages | Waiting on the owner's accounts; harness ready (`gmail-message`, `gmail-list`). The streaming decoder is done offline (24 MiB, below) |
| 4 | Microsoft's trip | Done as documented, against `graphsim`; real check is T201 |
| 5 | Graph's folders and delta | Done as documented, against `graphsim`; undocumented points listed for T201 |
| 6 | IMAP on each listed host | Waiting on the owner's accounts; harness ready (`imap-host`) |
| 7 | go-imap v2 | Done against go-imap's in-memory server and a scripted server; pinned `v2.0.0-beta.9` |
| 8 | HTTP versions | Google: harness ready (`http-versions`). It needs no account, only a real TLS handshake, which this part was told not to make. Microsoft: not documented, T201 |

## ADR change needed

Each of these contradicts ADR 0042 as written. The ADR is not changed here; the owner decides.

1. **Microsoft's `oid` needs the `profile` scope.** Per the [ID token claims reference](https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference), `oid` and `preferred_username` need `profile`. The ADR requests `openid email offline_access https://graph.microsoft.com/Mail.Read` and refuses a trip whose ID token lacks `tid` or `oid`. As documented, every Microsoft trip would be refused. Fix: add `profile` to the authorize scope and to the Entra registration's delegated permissions, or key the identity on `sub`. Test: `TestTripWithTheADRsScopesHasNoOID`.
2. **A Microsoft refresh token does not end at the person's own password change.** The [refresh tokens page](https://learn.microsoft.com/en-us/entra/identity-platform/refresh-tokens) has a revocation table. A confidential client's token stays alive when the user changes their password, uses SSPR, or an admin resets it in the Azure portal. It is revoked by an admin reset in the Entra or Microsoft 365 admin center, or by any revocation. The ADR's "Lifetimes" says it "ends at a password reset". This matters for what Permaily tells a person: changing their password does not disconnect Permaily. Test: `TestPasswordChangesAndRevocation`.
3. **The Microsoft scope comparison must fold case.** The documented token response shows `scope` as `https%3A%2F%2Fgraph.microsoft.com%2Fmail.read`, lower case and percent-encoded ([auth code flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow)). The ADR's table compares `Mail.Read` literally, so it would refuse that answer. It also needs to say what a percent-encoded value means. T201 shows the real form. Test: `TestScopeRenderingsAgainstTheADRsComparison`.
4. **A Microsoft refresh never carries an ID token.** An `id_token` is returned only when that leg requests `openid` (auth code flow page). The ADR's refresh asks for `https://graph.microsoft.com/Mail.Read offline_access`. Its check "an answer whose ID token (when present) names another `tid` or `oid` writes the grant `broken`" can therefore never fire. Either add `openid` to the refresh, or drop the check and rely on the tenant path. Test: `TestRefreshRotatesAndOmitsTheIDToken`.
5. **`admin_approval_needed` from AADSTS numbers is best-effort at most.** At the redirect, an authorize error carries only `error` and `error_description`, with no `error_codes`. Microsoft's [error code reference](https://learn.microsoft.com/en-us/entra/identity-platform/reference-error-codes) says: "Never use [error_description] to react to an error", and "Apps that take a dependency on text or error code numbers will be broken over time." T10's mapping of AADSTS65001 and AADSTS90094 should be labelled best-effort, with no match meaning `provider_failed` (the safe default it already has). The docs do not show what the redirect carries for an admin-approval case (T201). Test: `TestErrorShapes`.
6. **`imaptest` cannot rest on go-imap's in-memory server for CONDSTORE.** At `v2.0.0-beta.9` the server has no CONDSTORE at all: `EXAMINE INBOX (CONDSTORE)` and `FETCH … (CHANGEDSINCE n)` both answer `BAD`. It also sets `\Seen` on a non-peek `BODY[]` under `EXAMINE`. The ADR's played server ("go-imap v2's in-memory server behind TLS … scripted to misbehave") needs its own CONDSTORE layer (HIGHESTMODSEQ, MODSEQ, CHANGEDSINCE) to test those paths. Transcripts: `condstore-memserver.txt`, `memserver-body-under-examine.txt`.

## Findings

### Items 4 and 5: Microsoft, as documented

`graphsim` serves `login.microsoftonline.com` and `graph.microsoft.com` under a throwaway test CA. A client built for the production URLs runs against it unchanged. Its 22 tests play these:

- **Token response**: `access_token`, `token_type` `Bearer`, `expires_in`, `scope` (optional; omitted means the first leg's scopes), `refresh_token` only with `offline_access`, `id_token` only with `openid`.
- **Refresh rotation**: each refresh returns a new token and the old one stays valid ("Refresh tokens replace themselves with a fresh token upon every use. The Microsoft identity platform doesn't revoke old refresh tokens"). The lifetime is 90 days, renewed by use; idle expiry is `invalid_grant` with AADSTS700082, and revocation is AADSTS50173.
- **ID token**: v2.0 claims; the personal tenant is `9188040d-6c67-4c5b-b112-36a304b66dad`; `email` appears only with the `email` scope and only if the account has one, and it is unverified and mutable. Signed RS256, with keys at `/{tenant}/discovery/v2.0/keys`.
- **Token errors**: `error`, `error_description`, `error_codes`, `timestamp`, `trace_id`, `correlation_id`. A reused code gives AADSTS54005. MFA or conditional access gives `interaction_required` with AADSTS50076.
- **Well-known folders**: all 17 names "work regardless of the locale of the user's mailbox". The simulator gives a Spanish mailbox localised display names, which are illustrative because the docs do not list them.
- **Folder ids**: constant ("Container types … their regular IDs were already constant").
- **Delta**: per folder; a page has `@odata.nextLink` or `@odata.deltaLink`, never both. A message deleted or moved out shows as `{"id", "@removed": {"reason": "deleted"}}`; the removal does not say where the message went. Replays happen. With `$filter` on `receivedDateTime` a round returns at most 5,000 messages; the ADR's unfiltered first round avoids that cap.
- **Delta errors**: an expired token gives "a 40X-series error with error codes such as `syncStateNotFound`". A sync reset gives 410 Gone with a `Location` holding an empty `$deltatoken`. Outlook delta tokens have no fixed lifetime.
- **Immutable ids**: `Prefer: IdType="ImmutableId"` applies per request and survives moves between folders, including junk and deleted items. It changes on a move to the archive mailbox or on export and import. Delta links work with both formats. Regular ids change on a move.
- **Throttling**: 429 with `Retry-After` (seconds) and code `TooManyRequests`; 503 may carry `Retry-After`. The Outlook limits are 10,000 requests per app and mailbox in 10 minutes, and 4 concurrent requests.
- **Over quota and conditional access**: folder delta may answer 403 `ErrorQuotaExceeded` when the mailbox is over quota. Graph's conditional-access answer is a 403 with `insufficient_claims`.

**Not documented: questions for T201.** The simulator assumes an answer for each of these and marks it `Undocumented` in `scripts.go`:

- That a message deleted for good answers 404 `ErrorItemNotFound`, and a malformed id 400 `ErrorInvalidIdMalformed`. No Microsoft reference page names either code; they appear only in Q&A threads. The ADR's Graph deletion evidence rests on `ErrorItemNotFound`.
- That well-known folders cannot be moved, renamed or deleted. `mailFolder: move` lists no exception. The ADR's I2 rests on this.
- That a folder the person deletes goes into deleted items, with its messages' `parentFolderId` unchanged.
- The exact status of `syncStateNotFound`, and the code of the 410 (`resyncRequired` appears only in community answers).
- That `$value` leaves `isRead` unchanged.
- The answer to a fifth concurrent request.
- What the redirect carries when a work tenant needs admin approval.
- Which `error` value carries AADSTS53003.
- Whether a code redemption's `scope` echoes the OIDC scopes.
- Whether a refresh at `/{tid}/oauth2/v2.0/token` works for a personal account (`tid` 9188040d-…). G17 rests on this.
- Whether `login.microsoftonline.com` and `graph.microsoft.com` negotiate `h2`.

### Item 7: go-imap v2

- **Version.** The ADR names none, so the spike pins the latest v2 release on 2026-11-27, `github.com/emersion/go-imap/v2 v2.0.0-beta.9`.
- **Read-only select is `EXAMINE`.** `Select(name, &SelectOptions{ReadOnly: true})` sends `EXAMINE`, and `BODY.PEEK[]` leaves `\Seen` unset (`examine-peek.txt`).
- **CONDSTORE.** The client sends `EXAMINE INBOX (CONDSTORE)` and `UID FETCH 1:* (UID FLAGS MODSEQ) (CHANGEDSINCE n)`, and parses `HIGHESTMODSEQ` and `MODSEQ` from RFC 7162's example answers (`condstore-client.txt`). It refuses `ENABLE CONDSTORE` before sending anything (it allows only IMAP4rev2, UTF8=ACCEPT and METADATA), so CONDSTORE is enabled by the `SELECT`/`EXAMINE` parameter, which RFC 7162 section 3.1 allows.
- **FETCH item order changes between runs.** `writeFetchItems` ranges over a Go map, so the order of `FLAGS`, `MODSEQ`, `INTERNALDATE` and the like on the wire changes from run to run. The transcripts compare them sorted. The adapter's own command writer (pull request 6) should fix the order.
- **`UID SEARCH`**: a plain `* SEARCH` answer under IMAP4rev1, and with ESEARCH and `ReturnAll`, `UID SEARCH RETURN (ALL) …` with a `* ESEARCH (TAG …) UID ALL …` answer. This includes the presence search `UID SEARCH UID 2,9` that deletions use.
- **Streamed literal.** `BODY.PEEK[]` of a 24 MiB message is handed over as a stream. After the caller has read 64 KiB, the client has read about 66 KB of the 25,165,945-byte literal from the socket; the test asserts a read-ahead of at most 1 MiB, since the exact count depends on socket and bufio buffering (`TestStreamedLiteral`). The content arrives intact.
- **`NO [NONEXISTENT]`**: `EXAMINE` of a folder that never existed, or was deleted, answers `NO [NONEXISTENT]`. A folder deleted and made again under the same name gets a greater `UIDVALIDITY` (`nonexistent-recreated.txt`). This is the in-memory server's behaviour, not a real host's; item 6 checks each host.
- **What the in-memory server lacks**: CONDSTORE, SPECIAL-USE, real connection limits, and the read-only guard on `BODY[]`. It is not a provider, and nothing here says how any listed host behaves.

### Item 3: the streaming `format=raw` decoder (offline)

`streamRaw` in `cmd/providerspike/rawstream.go` reads one `messages.get?format=raw` response and decodes its base64url `raw` member into a writer as it arrives. It keeps the other members (`labelIds`, `internalDate`, `sizeEstimate`, `historyId` and the like), each bounded. A 24 MiB message, padded base64url, fed through a reader that hands over half of each read, decodes intact without a write above 1 MiB. Escapes inside `raw`, truncation and oversized members are refused.

### Gmail units

The ADR's figures match [Google's quota page](https://developers.google.com/workspace/gmail/api/reference/quota): `getProfile` 1, `history.list` 2, `messages.list` 5, `messages.get` 20, or 60 with `format=raw`, `full` or `html`; 6,000 units a user a minute; 1,200,000 a project a minute. Gmail does not report units per response, so the harness tallies the documented cost of each call it makes (`gmail-units`). The owner compares the tally with the Cloud console's quota metrics.

## Runbook (for the owner, once the accounts exist)

**Create first** (from ADR 0042, "What the owner must create before the spike"):

- **Google Cloud**: a project for Permaily's mail (`permaily-mail`), separate from sign-in's, with the Gmail API enabled. The OAuth consent screen: External, Testing, app name, support email, logo, home page, privacy and terms URLs, authorised domain `permaily.com`, developer contact. Scopes: `openid`, `email`, `.../auth/gmail.readonly`. Test users: only the owner's synthetic test accounts. A Web client with redirect URIs `https://app.permaily.com/api/v1/mailboxes/connect/callback` and `http://localhost:8443/api/v1/mailboxes/connect/callback`.
- **IMAP test accounts**: one at each listed host, with two-factor on and an app password each.

Gmail is used only through test accounts that hold mail the owner sent for the purpose; never a person's mailbox.

**Credentials** (never in the repository, never pasted into notes):

```sh
mkdir -p ~/.config/permaily && chmod 700 ~/.config/permaily
# {"client_id": "...", "client_secret": "..."}: the Web client above
$EDITOR ~/.config/permaily/spike-google-client.json && chmod 600 ~/.config/permaily/spike-google-client.json
# {"username": "...", "app_password": "..."}: one file per host id (yahoo, aol, icloud, fastmail, gmx-com, gmx-net, zoho, zoho-eu, zoho-pro)
$EDITOR ~/.config/permaily/spike-imap-yahoo.json && chmod 600 ~/.config/permaily/spike-imap-yahoo.json
```

The harness refuses a credential file that any other user can read. It writes the Google grant to `~/.config/permaily/spike-google-grant.json` (0600) and keeps a local pseudonym key in `spike-hmac.key`. It writes its findings to `spikes/providers/results/` (git ignored, 0600). It refuses to write any field that looks like an address or a token, and replaces message ids, folder names and `sub` with keyed pseudonyms.

**Commands**, run from `spikes/providers`. Each takes a few seconds unless noted. A first run of any `go run` also compiles, which takes about a minute.

1. Day 0, item 8 (no credentials): `go run ./cmd/providerspike http-versions`.
2. Day 0, item 1: `go run ./cmd/providerspike google-trip -label ticked`. Open the printed address in a browser signed in to a test account and grant everything. Then revoke the app at myaccount.google.com/permissions, run `google-trip -label unticked`, and untick Gmail on the consent screen. Then run `google-trip -label ticked` again to leave a full grant.
3. Day 0, item 2 baseline: `go run ./cmd/providerspike gmail-baseline`, right after the last trip.
4. Day 0, item 2 scenarios. In Gmail on the test account: change a label, archive, mark as spam, move to trash, delete forever, move one message back from trash, and apply one label to about 2,000 messages (this takes a few minutes to prepare). Then run `gmail-history -twice`.
5. Day 0, item 3:
   - Send the test account a new message, and relabel an older one, both after the baseline. Then run `gmail-message -q 'newer_than:1h'` and `gmail-message -id …` (or `-q` matching the relabelled one).
   - Send a message of about 24 MiB and run `gmail-message -q 'larger:20M'`.
   - Run `gmail-list -days 30`.
   - Leave the account idle from here on.
6. Day 1: `gmail-history-age`.
7. Day 7: `gmail-history-age` and `google-refresh`; both should still work. Item 2's transcripts up to here gate pull request 4.
8. Day 8: `google-refresh`. Testing mode's seven-day end should answer `invalid_grant`. Then run `google-trip -label ticked` again, which leaves the mailbox's history alone, so day 14 has a token.
9. Day 14: `gmail-history-age`. This confirms a constant and gates nothing (G21).
10. Item 6, for each host id:
    - `go run ./cmd/providerspike imap-host -host <id>`, on two different days, for `UIDVALIDITY` stability.
    - Once with `-folder-writes`, which creates, deletes and remakes `permaily-spike-scratch` on the test account.
    - Once with `-max-sessions 10`. This opens up to ten sessions; a host may lock the account briefly.

**Afterwards**: summarise `results/*.jsonl` by hand into a "Results" section here, per item and per host. Use no addresses, tokens, message content or subjects; the pseudonyms are enough to say "the same message". Then delete `results/`, the grant and the key once the spike is done.
