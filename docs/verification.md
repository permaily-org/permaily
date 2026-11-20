# Verification

## Quick start

```sh
git clone https://github.com/permaily-org/permaily && cd permaily
scripts/verify.sh v0.1.0          # replace with the release you want to check
```

The script needs Nix (with flakes) or Docker. It rebuilds the enclave image of that tag, prints the reproduced PCRs and runs `permaily-verify` against `app.`, `api.`, `mcp.`, `inbox.` and `mta-sts.inbox.permaily.com`, then checks how mail gets in and looks for certificates outside the enclave. Exit code 0 means every check passed.

## What is checked

For each host, `permaily-verify`:

1. Generates a random 32-byte nonce and calls `GET https://<host>/attestation?nonce=<hex>` with normal WebPKI TLS verification.
2. Decodes the COSE_Sign1 document and verifies its ECDSA P-384 signature.
3. Verifies the signing certificate chain up to the AWS Nitro Enclaves root G1 (SHA-256 fingerprint `641a0321a3e244efe456463195d606317ed7cdcc3c1756e09893f3c68f79bb5b`), at the document's timestamp.
4. Checks the timestamp is within 5 minutes of now and the nonce matches.
5. Rejects all-zero PCR0-2 (debug mode).
6. Compares PCR0, PCR1 and PCR2 with your reproduced build.
7. Checks that the document's `user_data` equals `sha256(SubjectPublicKeyInfo)` of the TLS certificate presented on the same connection. This proves the TLS session ends inside the attested enclave.
8. Reads `GET /status` over a connection with that same TLS key and checks the KMS key that locks the root secret everything stored is sealed under (below). When the enclave sends mail of its own, it also checks who else SES would give a copy to (below). When it has accounts, it prints what the operator did to them through the enclave, and fails on nothing (below). When it asks a model, it prints which one and how, fails when the provider's prompt cache is not off, and warns on its canary (below).

The operator's scripts hold themselves to the same rule before they send the admin token or a secret (ADR 0029): each runs `permaily-verify -json` against the enclave first, in the same run, takes the attested key (`tls_spki_sha256` of the host) and sends every request only with that key (`curl --pinnedpubkey`), with the CA and hostname checks as well and no redirects. If the enclave does not verify, nothing is sent. A server with another valid certificate for the name gets no request. The verifier they run is the running release's, built when the release is named (`PERMAILY_RELEASE`, or `handoff.sh`'s `<running>`) from the commit that release's build provenance names, authenticated with `gh attestation verify` and matched with the release's tag at GitHub, never from a tag's name alone, fetched into a private repository and compiled in a fixed Go environment (ADR 0029); without `PERMAILY_PCRS`, the `pcrs.json` they authenticated is the file the verifier checks (`-pcrs`), not a second download: its Sigstore check compares the enclave's trust root with its own, and a later verifier would refuse an older release that is right.

## Who can read stored mail

Stored mail is sealed under a root secret that only enclaves hold, locked for each image by a KMS key of its own. The verifier checks that key ([The root secret](#the-root-secret)): only the image you verified can use it, and nobody can change that. So there is no list of allowed images to watch, and an upgrade does not open anything: the running enclave hands the root secret to the next release itself, after checking that it is a public one.

Releases up to v0.6.0 worked differently: KMS released data keys to the images named in one key's policy, which administrators could replace. For those releases, and for v0.7.0, which still read that key, the verifier checks the policy the enclave reports:

- The enclave reads the key's policy and grants from KMS and serves them on `/status`. The verifier accepts the answer only when the connection uses the attested TLS key, so it comes from the verified image.
- Every statement of the policy that allows more than administration must require the enclave image (`kms:RecipientAttestation:ImageSha384`). A statement that does not, or that the verifier does not understand, fails the check.
- The only image allowed must be the one you verified, or none. Another image fails the check, unless you accept it by its PCR0 with `-kms-also-allow`.
- The key must have no grants, and the enclave's reading must be at most 5 minutes old.
- The same holds for every state of the policy the enclave has seen since it started (once a minute); the error says from when to when.

Releases before v0.3.0 do not report the policy; verify them with `-skip-kms-policy`.

What this does not prove for that old key: the account's administrators could replace its policy until it was retired (2026-11-10), after which the verifier prints that it releases data keys to no image. A change made while the enclave could not reach KMS shows only as a warning with the period, and the history starts again when the enclave restarts.

The workflow `verify-production` of the repository runs the latest release's verifier against the live service every half hour; its runs and their output are public under Actions. `scripts/verify-production.sh` runs the same check from your machine. Those runs cannot reach port 25 and skip its certificate; the operators run the same check with `-require-smtp` every hour from an instance of its own in their AWS account (ADR 0038), whose results are mailed to them and not public, and which is no more independent of them than the account is.

`permaily-verify -provenance bundle.json -pcrs pcrs.json` checks no deployment: it answers, offline, whether those measurements are a release, the way a running enclave decides before it hands the root secret to the next one (`pkg/provenance`, with the trust root compiled into the binary). It reads releases recorded in the first Rekor log and in Rekor v2, where the time comes from Sigstore's timestamp authority. `scripts/provenance-bundle.sh` fetches the bundle of a `pcrs.json`. `permaily-verify -trust-update DIR` answers the other question an enclave decides offline: whether the files in a directory are a trust root that Sigstore published after the one compiled in.

## Who else gets a copy of the mail Permaily sends

Signing in by mail sends one message with a code and a link, through Amazon SES. Whoever administers the AWS account could make SES keep a copy of every message (a configuration set tied to an archive, set as the default of the identity mail is sent from), and with the copy of a sign-in message enter the account. The enclave names no configuration set when it sends, so it reads from SES whether one applies, sends only while none does, and reports every state it read since it started (ADR 0021). When `/status` has `mail`, the verifier requires:

- the domain and the address the enclave says it sends from;
- a reading of SES no older than five minutes;
- the whole history, ending in the current state;
- the current state and every earlier one clean: the domain's identity is there, can send, and has no configuration set; the address has no identity of its own, or one without a configuration set.

A state that was not clean prints with the times it was first and last seen. A reading that fails right now, within the five minutes, is a warning: the enclave sends nothing while it cannot read.

What this does not show: a configuration set put in place and removed between two readings in the moment a message leaves (the enclave reads before a message, unless it read within five seconds, and after each one, so the moment is about a second, up to six when many messages leave at once); anything from before the enclave started; and other settings of SES, which are not read. AWS and the person's mail provider read the message in any case.

## What the operator did to accounts

The operator, with the admin token, can do some things to an account through the enclave: block it and unblock it, ask to delete it and cancel a deletion the operator asked for, sign its devices out, and make and revoke invitations (ADR 0017); and pause and resume a mailbox (ADR 0026). The admins do the same from the web app, and the super admin makes and removes admins (ADR 0022). The enclave counts each kind of what both do and serves the counts on `/status` under `admin`, with the time it started counting. The verifier prints them:

```
      what the operator did to accounts through this enclave since 2026-11-12T04:58:12Z: blocked an account 1 time; made an invitation 2 times
```

It fails on nothing here: blocking an account or asking to delete one is the operator's to do. It is printed so that it is seen, by anyone, twice an hour.

What the operator cannot do through the enclave is not counted because there is no way to do it: change an account's contact address, add or remove a way to sign in, turn "passkey only" off, or sign in as someone; read a mailbox, learn its address or label, disconnect it or delete its mail. Those would be ways into an account or its mail, and the image you verified has no route for them.

What this does not show: which account, on purpose; anything from before the enclave started, since the count is in memory and a restart, which the operator can cause, starts it again (the start time is printed); and what is written to the storage by hand, which the enclave refuses where it would be a way in (ADR 0016, ADR 0017) and which can still delete an account.

## The model the enclave asks

From the release that composes it (ADR 0028), the enclave asks OpenAI's Decisions API what exact conditions cannot settle, and only for the people who agreed to the model's notice. `permaily-verify` prints what the image compiles in and what the enclave reports: the provider, the address (`https://api.openai.com/v1/decisions`), the model (`gpt-6-luna`), the prompt version, the prompt cache, the OpenAI organisation and project every request names, whether zero data retention is approved (what the notice says), the limits of the spend guard, whether a key is set and since when, the guard's state, the canary's, and the calls by kind since the enclave started.

- It **fails** when `prompt_cache` is not `off`: a release that turned the cache on would keep copies of mail at OpenAI that the notice does not mention. This check, the section and its warnings run whatever else is skipped: `-skip-kms-policy` and `-skip-certificates`, alone or together, skip only what their names say, and `/status` is always read.
- It **warns** when the canary is `changed`, `failed`, `stale`, `unreadable` or `none`, or when its `baseline` is not the SHA-256 of `internal/decide/canary.json` at the commit the enclave runs (the one its attestation and provenance name, never your checkout's or the latest release's): read from GitHub at that commit, or from your own checkout of it with `-canary <file>`. It also warns when the requests do not pin both an organisation and a project, and when the guard asks nothing new.

What this proves: which model, address, organisation and project the measured code calls, that it turns the prompt cache off on every request, and the limits it holds itself. What it does not: what OpenAI keeps of what it is sent, whether the operator approved zero data retention or set the project's own spend limit, and whether the key is used elsewhere; those are OpenAI's and the operator's word. The canary is a sample of 156 fixed synthetic statements a day, not a proof that the model did not change between two checks. `/status` never holds the key, its fingerprint, an amount spent, an account, a topic, a message or a probability.

## Connections

From the release that composes them (ADR 0030 and 0031), apps and agents a person allows read some of their mail through the API (`https://api.permaily.com/v1`) and MCP (`https://mcp.permaily.com/mcp`), authorized by the enclave's own authorization server (issuer `https://api.permaily.com`). `permaily-verify` prints the `connections` section of `/status`: the issuer, the endpoints and the two resources, the grant types and the ways a client authenticates, the lifetimes, what a code used again gets, the bound of a revocation in another enclave and what it covers, how a client assertion used again is refused, how vendors' documents are fetched, the devices a connection may have, what connections get while the model cannot be asked, the verified vendors, the rules every read applies first with the canonical identifiers of their formulas, how receipts and the rules' decisions are kept, and what this enclave refused by reason since it started.

- It **fails** when `dynamic_registration` is true or `client_documents` false; when the issuer, an endpoint or a resource is not the deployment's (`https://api.<domain>` and its `/oauth/token`, `/oauth/revoke`, `/oauth/par`, `https://app.<domain>/oauth/authorize`, and the resources `https://api.<domain>/v1` and `https://mcp.<domain>/mcp`, the domain read from the URL checked); when `revocation_seconds` is over 5 or `revocation_covers` is not `every_byte`; when `assertion_replay` is not `strict` or `code_reuse` not `denied`; when `model_unavailable` is not `nothing_leaves`; when `receipts.decisions` is not `strict`; and when the default rule (`default-sign-in-and-security`) is absent, off, or not the version and the formulas, by canonical identifier, that `internal/rules` compiles in the verifier, which `scripts/verify.sh` builds from the release's own commit. Like the model's, this section is checked whatever else is skipped.
- From the release that chains receipts (ADR 0037, pull request 2), it also **fails** on what `ReceiptsStatus.Check` requires at the verifier's own clock: receipts not sealed, not chained or whose `chain` is not `strict`; the record of incidents (`receipts.incidents`) absent, read more than 30 minutes before now or after now, or holding any entry, whatever a later pass found; no finished pass of the audit (`receipts.audit` absent, as in a new deployment before its first pass or an enclave that has not read one yet), or one that finished more than 48 hours before now or after now; a count below zero, a kind of `attestation.ReceiptsBrokenKinds` missing or one it does not know; and any count above zero in `receipts.audit.broken` or in `receipts.broken` (what the enclave that answered found and has not recorded yet). A lost or forked link fails it besides, as a regression named `receipt link` in `anchors`. Step 5's warning that receipts are not chained is gone.
- It **warns**, without failing, when the newest pass linked receipts late (`recovered`: a write that failed after its receipt was stored, or an earlier release still writing), found receipts without a link too old to recover (`unlinked`: they can be deleted unseen), or found links whose commits' lock ended (`expired`: their bytes are still checked, a fork there no longer shows). It prints the record of incidents, the newest pass and its counts, and what the enclave that answered has not recorded yet.

What this proves: what the measured code does with every token and connection (opaque tokens checked on every request, codes once, a revocation's five seconds bounding every byte of an answer already being sent, nothing leaving while the model cannot be asked, no answer leaving before its receipt is stored and named by a committed link of its account's chain), which rule holds sign-in mail back before anything leaves, by its formulas' canonical identifiers, and that the deployment's daily audit read every chain the catalogue names and found nothing deleted or changed since the record of incidents began. What it does not: which accounts or receipts a recorded incident concerns (the record is sealed; the person sees it in their own walk); a deletion between two passes until the next pass or walk reads it; receipts deleted before their chain began or before the audit linked them; whether the audit runs again (only that its newest pass is under 48 hours old); what a vendor does with what it was given, which is the vendor's; how many people connected what, on purpose (`/status` has no count of connections or consents, no client but the verified list, no account and no connection); and anything from before the enclave started, since `refused` is this enclave's count in memory. The vendors' documents are fetched through the host's vendor egress (vsock 8004), which sees which vendor's document is fetched and when, and cannot read or change it.

## Your receipts, offline

From the release that chains receipts (ADR 0037, pull request 3), a person checks their own account's chain of receipts on their own computer, trusting nothing of the service but a head they noted over the attested connection.

1. Signed in to the app, read the chain's head: `GET /api/v1/receipts` answers `head`, with its number `n` and, when its verdict is `ok`, its `sha256` (the Activity screen shows them, with Copy). Write them down apart from the export.
2. Download the export: Activity's "Download for checking" (ADR 0041), or `GET /api/v1/receipts/export`, then `?before=<n>.<index>` of each page's `next` until a page says `done`, each page's JSON on a line of its own, as the server wrote it (`exportReceipts` in `web/src/lib/api.ts`, `writeExport` in `web/src/lib/csv.ts`). A record past 16 MiB is saved in numbered parts, `permaily-receipts-<day>-part-001.jsonl` on, and then `permaily-receipts-<day>-jsonl-manifest.json`, which names each part with its bytes and SHA-256: join them in order (`cat permaily-receipts-<day>-part-*.jsonl > receipts.jsonl`) and check the joined file. A set without its manifest stopped before the end, and a part left out fails the check (`web/tests/receipts`).
3. Build `permaily-verify` from the release's commit, as `scripts/verify.sh` does, and run `permaily-verify -receipts receipts.jsonl -receipts-head <n>:<sha256>`, with one `-receipts-head` for each head you noted (`-json` for a machine-readable result).

It reads the file a page at a time, with the protocol's own checks (`pkg/receiptchain`, the package the service uses for the same rules; `permaily-verify` links no storage and no AWS SDK). It requires the export to be whole, not only each page well made (`receiptchain.ExportPage.Check`: its version, its bounds, its order, every verdict one the protocol has; and the head as the app shows it, `Head.CheckShown`: a `sha256` and an `at` when it is `ok` and only then). The first page begins at its head; each begins exactly where the one before ended (its `next`); no link or receipt between them is left out, and each receipt is the one its link names; only the last page says `done`, and only after link 1; every page is of the same account; a page's head never goes back, nor names another digest at the same number. Each link's document is one JSON object of a link's fields and nothing after it (`receiptchain.ParseLink`). It recomputes every digest: the head's link against the head, each link against what the link above names (`prev`), every receipt against its link's ref, link 1 against the account's genesis.

It says three things apart: whether the file is **consistent** (whole, every digest holding, every noted head in it), whether it is **intact** (consistent, and reporting no break), and what a noted head **authenticates**.

- **Exit 0, `PASS`**: consistent and intact. It prints which links a noted head authenticates (from it down to the first break) and which it does not (above the newest noted head; all of them with no head noted, with a `WARN`); the links recovered (linked late), adopted (from before the chain) and expired, as `NOTE`, which qualify what is known and are not breaks; and each page's head.
- **Exit 1, `FAIL  receipts: not a consistent export`** and the reason: a byte of any document changed, a file cut short, a page dropped, repeated or out of order, a document that is not a link or has anything after it, a verdict the protocol does not have, a head not as the app shows it; a noted head the export does not have at its number, or above the export's own head (the file was cut short, or made before the head was noted), or at a link the export says is lost.
- **Exit 1, `FAIL  receipts of <account>: … consistent with itself and not intact`**: the file holds together and reports at least one break, each printed as `BREAK`: a link lost, forked or unreadable (the export has no document of it, and which receipts it named is not known, so none is named), a link `prev` (its document is not what the link above names), a receipt missing or unreadable (by its position and identifier; the export has no document of it) or changed (its document is not what its link names), and a link whose document names no link before it (the chain records a break there). It still prints what a noted head authenticates and every `NOTE` and head.
- **Exit 2**: flags that are not heads.

A break fails the check whoever made it. The verdicts are the export's word: no digest a head vouches for covers them, so whoever edits the file can remove a receipt and call it missing, or replace one and call it changed, exactly as the storage would have left it, and keep the genuine head. Offline, the check cannot tell the two apart. Whether the storage did it is what the deployment records: a break the enclave finds is an incident before it is shown, and `permaily-verify` against the deployment (`/status`) fails while the record of incidents holds any.

With `-json` it prints one object: `ok` (the exit status: consistent and intact), `consistent`, `intact`, `error` (why it is not consistent), `authentication` (`whole` when a noted head authenticates every link, `partial` when some, `none` otherwise), `account`, the counts of `links` and `receipts`, the spans `authenticated` and `not_authenticated` (from, to), `broken` (links and receipts the export reports broken, a receipt with its `index` and `id`), `breaks` (links naming no link before them, with the reason they give), `recovered`, `adopted`, `expired` and `heads` (each with the pages that said it).

What this proves: with a head noted over the attested connection, that every link and receipt from it down to the first break is what the chain held, consistent from its head to the account's genesis; and, when it passes, that the export reports no break. What it does not: links above the newest head noted (note the head when you export; a later export holds every earlier head, since deleting adds no link); an export checked with no head noted, which is only consistent with itself, since whoever edits it can recompute every digest; links below a break; who made a break the export reports; and that no answer left without its receipt, which rests on the attested code (`Write` links every receipt before its answer), not on the file. The file holds the receipts as stored, senders and subjects of what was made available included: keep it as you would that mail.

## How mail gets in

Mail arrives over SMTP on port 25, which the HTTPS checks above do not cover. When no URLs are given, `permaily-verify` also checks:

1. The MTA-STS policy at `https://mta-sts.inbox.permaily.com/.well-known/mta-sts.txt`, accepted only when served with the attested TLS key. It must say `mode: enforce`, allow only `inbox.permaily.com` as MX, and last at least a day. This is what makes a sending server refuse to deliver without a valid certificate.
2. DNS: exactly one `_mta-sts.inbox.permaily.com` record (without it senders do not apply the policy) and an MX for `inbox.permaily.com` that points only at itself.
3. Port 25: it connects as a sending server would, upgrades with STARTTLS, requires a certificate valid for `inbox.permaily.com`, and requires its key to be the attested one. It sends no mail.

Many home and office networks block outbound port 25. Then step 3 cannot run: the verifier prints a warning and still exits 0. Run it from a server, or pass `-require-smtp` to make that a failure. `-skip-mail` skips these checks.

What this does not prove: a sender that does not implement MTA-STS may still deliver in clear to whoever answers on port 25 (the enclave itself refuses mail without TLS), and the DNS records can change after the check.

## Certificates outside the enclave

Everything above proves the connection the verifier makes. A browser cannot do that: it accepts any valid certificate for `app.permaily.com`. So the verifier also looks for certificates the enclave does not hold. When no URLs are given it checks:

1. What the enclave reports in `/status`: the hash of its TLS key (it must be the attested one), when the key was created, that it is kept sealed (under the root secret), and the URL of its ACME account.
2. CAA records, read through a DNS-over-HTTPS resolver: every enclave host must allow only Let's Encrypt, only that account and only `tls-alpn-01` validation, which is answered from inside the enclave; and the domain must forbid wildcards (`issuewild ";"`), because `*.permaily.com` would be valid for the hosts.
3. Certificate Transparency, read through Cert Spotter's API: every unexpired certificate valid for an enclave host and issued since the enclave's key was created must carry that key. One with another key is a failure and is listed.

Certificates with other keys issued before the enclave's key existed are counted as older, with the date the last one expires: four wildcards from before 2026-11-10 (ADR 0012) and the certificates of earlier releases, whose keys lived only in memory. Nothing can prove who holds an old key, so they are reported, not failed.

What this does not prove: it trusts Cert Spotter's view of the logs, the DNS records can change after the check, and a certificate issued before the key's creation time is not judged. That time is printed; if it changes between two checks, the key was replaced. `-skip-certificates` skips these checks (releases before v0.4.0 do not report their TLS key); `-doh-resolver` and `-ct-api` choose the services.

## Two enclaves behind one address

One enclave runs today; the service is ready to run two behind one address, with TLS ending in each (ADR 0024). Both hold the same TLS key, so the verifier checks each connection as before: the certificate on it carries the attested key, and `/status` is read only over a connection with that key. A run whose requests land on both enclaves passes. What `/status` reports is the enclave's own (the version at which it read the TLS identity, the commits it saw, the mail it watched, the operator's counts), and each answer is checked on its own.

During a renewal, Certificate Transparency lists two valid certificates with the attested key, and for up to ten minutes the other enclave still serves the earlier one. Both count as the enclave's own, so the check passes. A certificate with another key still fails, also one for a key that the other enclave holds after the identity was lost and made again.

Not built: reaching each enclave on purpose. Behind one address the verifier checks the ones its connections land on. Each attestation document names the enclave that made it (`module_id`, printed for each host). Counting the distinct ones over many connections, or giving each enclave an address of its own, needs a decision. While a release is being rolled out to two enclaves, a connection that lands on the other release fails the check of its measurements.

## Checking that an interceptor is detected

`TestProductionMITM` puts a TLS-terminating proxy between the verifier and the live service, as the parent instance or a CDN could, and requires verification to fail. The proxy relays everything untouched, so the attestation document is genuine and fresh; it fails because the document vouches for the enclave's TLS key and the connection uses the proxy's.

```sh
PERMAILY_PCRS=result/pcrs.json go test -tags production -run TestProductionMITM -v ./cmd/permaily-verify
```

It first verifies the service directly, so a pass means the proxy was the only difference.

## Reproducing the measurements yourself

```sh
nix build .#eif        # result/permaily.eif, result/pcrs.json, result/SHA256SUMS
go run ./cmd/permaily-verify -pcrs result/pcrs.json
```

The build gives identical bytes on x86_64 and arm64 Linux (CI enforces this). Each release publishes the image's measurements (`pcrs.json`), the verifier, the host program, `third-party-notices.md` and `SHA256SUMS`, all under the same build provenance, and not the image itself: it contains AWS's GPL kernel, and you get that from AWS when you build it ([ADR 0023](decisions/0023-releases-publish-the-measurements-not-the-image.md)). Releases up to v0.10.0 also carry `permaily.eif`; the rebuild is what counts either way. `permaily-verify -release <tag>` uses the published `pcrs.json`, which is weaker because it trusts CI instead of your own build.

The commit is compiled into the image, so the PCRs identify a commit. Builds from a working tree with uncommitted changes produce different PCRs.

## Attestation endpoint

`GET /attestation?nonce=<hex, 16-512 bytes>` returns:

```json
{
  "document": "<base64 COSE_Sign1 attestation document>",
  "user_data_scheme": "sha256-spki-v1",
  "commit": "<git commit, informational>"
}
```

## Status endpoint

`GET /status` returns what the enclave read about its own environment. It is not signed; trust it only over the attested TLS key (`attestation.FetchStatus`).

```json
{
  "commit": "<git commit, informational>",
  "root": {
    "image": "<PCR0 the enclave measured itself as>",
    "key_arn": "<the KMS key of this image, which locks the root secret>",
    "policy": "<the key policy document, as KMS returned it>",
    "grants": 0,
    "key": {"origin": "AWS_KMS", "spec": "SYMMETRIC_DEFAULT", "usage": "ENCRYPT_DECRYPT", "manager": "CUSTOMER", "state": "Enabled", "multi_region": false},
    "checked_at": "<when the enclave last read them, RFC 3339>",
    "watched_since": "<when this enclave started reading the policy, once a minute>",
    "history": [{"policy": "...", "grants": 0, "first_seen": "...", "last_seen": "..."}],
    "gaps": [{"from": "...", "to": "..."}],
    "origin": "created | handed_over",
    "written_at": "<when the record for this image was written>",
    "from": {"image": "<PCR0>", "commit": "<git commit>", "published_at": "<when the public log recorded that release>"}
  },
  "tls": {
    "spki_sha256": "<hex SHA-256 of the certificate key, as in the attestation>",
    "key_created_at": "<when the key was generated, RFC 3339>",
    "sealed": true,
    "acme_account": "<URL of the ACME account that asks for certificates>"
  },
  "sigstore": {
    "trusted_root_sha256": "<hex SHA-256 of Sigstore's trusted_root.json the enclave checks releases against>",
    "tuf_root_version": 16,
    "tuf_targets_version": 14,
    "updated_at": "<when the enclave was given it; absent while it uses the one compiled into the image>"
  },
  "mail": {
    "domain": "<the domain the enclave's own mail is from>",
    "address": "<the address it is from>",
    "checked_at": "<when the enclave last read both identities from SES>",
    "state": {"domain_identity": "no_configuration_set", "address_identity": "absent"},
    "failing": false,
    "watched_since": "<when this enclave first tried to read them>",
    "history": [{"domain_identity": "no_configuration_set", "address_identity": "absent", "first_seen": "...", "last_seen": "..."}],
    "notices": {"since": "<when this enclave started counting>", "queued": 0, "sent": 0, "failed": 0, "dropped": 0, "skipped": 0, "refused": 0}
  },
  "admin": {
    "since": "<when this enclave started counting: when it started>",
    "actions": {"block": 0, "unblock": 0, "delete": 0, "delete_cancel": 0, "sessions_close": 0, "invitations_made": 0, "invitation_revoked": 0, "provider_app_set": 0, "provider_app_removed": 0, "admin_made": 0, "admin_removed": 0, "mailbox_paused": 0, "mailbox_resumed": 0, "model_key_set": 0, "model_key_removed": 0}
  },
  "model": {
    "provider": "openai",
    "endpoint": "https://api.openai.com/v1/decisions",
    "model": "gpt-6-luna",
    "prompt_version": "p2-2026-11-15",
    "prompt_cache": "off",
    "organization": "<org-…, absent while the image pins none>",
    "project": "<proj_…, absent while the image pins none>",
    "zero_data_retention": false,
    "month_limit_usd": 50, "day_alarm_usd": 10, "account_day_usd": 0.5,
    "key_set": true, "key_set_at": "<when the operator gave it>",
    "guard": "ok | month_limit | provider_limit | key_refused | unreadable",
    "canary": {"baseline": "<hex SHA-256 of canary.json>", "checked_at": "<last completed check>", "state": "none | same | changed | failed | stale | unreadable", "change_seen_at": "<last day a check saw a change>"},
    "since": "<when this enclave started counting>",
    "calls": {"answered": 0, "kept": 0, "refusal": 0, "timeout": 0, "unavailable": 0, "key_refused": 0, "limit": 0, "request": 0, "invalid": 0, "other_model": 0, "held": 0, "canary": 0}
  },
  "connections": {
    "issuer": "https://api.permaily.com",
    "authorization_endpoint": "https://app.permaily.com/oauth/authorize",
    "token_endpoint": "https://api.permaily.com/oauth/token",
    "revocation_endpoint": "https://api.permaily.com/oauth/revoke",
    "pushed_endpoint": "https://api.permaily.com/oauth/par",
    "resources": ["https://api.permaily.com/v1", "https://mcp.permaily.com/mcp"],
    "grants": ["authorization_code", "refresh_token"],
    "client_auth": ["none", "private_key_jwt"],
    "client_documents": true, "dynamic_registration": false,
    "access_token_seconds": 3600, "refresh_token_seconds": 2592000, "refresh_retry_seconds": 30, "code_seconds": 60, "code_reuse": "denied",
    "revocation_seconds": 5, "revocation_covers": "every_byte", "assertion_replay": "strict", "vendor_transport": "h2",
    "connection_default_days": 90, "connection_max_days": 365, "devices_per_connection": 16,
    "model_unavailable": "nothing_leaves",
    "verified": [{"domain": "claude.ai", "owner": "Anthropic"}, {"domain": "chatgpt.com", "owner": "OpenAI"}],
    "rules": [{"id": "default-sign-in-and-security", "version": 1, "formulas": ["qry_ghi2xjhazzz25rgsy6arrif4ke", "qry_dq33uknaplrglzyuws6gcb2uje", "qry_lh52gob7oeycyjrkqg2lpn2lje"], "on": true}],
    "receipts": {
      "sealed": true, "chained": true, "chain": "strict",
      "incidents": {"recorded": 0, "read": "<when this enclave last read the record of incidents, every ten minutes>"},
      "broken": {"receipt_missing": 0, "receipt_unreadable": 0, "receipt_changed": 0, "link_lost": 0, "link_forked": 0, "link_unreadable": 0, "link_prev": 0, "catalogue_lost": 0, "catalogue_forked": 0, "catalogue_unreadable": 0, "catalogue_prev": 0},
      "audit": {"began": "...", "finished": "...", "catalogue": 0, "chains": 0, "removed": 0, "broken": {"receipt_missing": 0, "…": 0}, "recovered": 0, "unlinked": 0, "expired": 0},
      "decisions": "strict", "decisions_per_day": 28800
    },
    "since": "<when this enclave started counting>",
    "refused": {"invalid_token": 0, "wrong_resource": 0, "assertion_replay": 0, "model_unavailable": 0, "decisions_full": 0, "…": 0}
  },
  "anchors": {
    "bucket": "<S3 bucket with Object Lock that holds the commits>",
    "retention_days": 3650,
    "records": [{"name": "tls-<CA host>", "version": 4, "committed_at": "...", "locked_until": "..."}],
    "regressions": []
  }
}
```

`sigstore` says which of the trust roots Sigstore published the enclave uses to decide what is a release: the file is `https://tuf-repo-cdn.sigstore.dev/targets/<hash>.trusted_root.json`. The verifier requires the one compiled into the release while there was no update, and a later one after an update; the enclave takes a later one only with the signatures of Sigstore's TUF keyholders, and only for the logs and the timestamp authorities (ADR 0013).

`anchors` lists the records the enclave refuses an earlier copy of (ADR 0016), as far as it read or wrote them since it started: the version of each, and until when S3 keeps its newest commit whatever anyone asks. The verifier fails when a commit is not locked, and when `regressions` is not empty: a commit the enclave had seen that the store stopped answering with. It warns when a lock ends within thirty days.

`mail` says what SES would do with a message of the enclave besides delivering it, as the enclave read it: each identity is `no_configuration_set`, `configuration_set`, `not_verified` or `absent`. `failing` is true when the newest attempt got no answer to go by, and `state` and `checked_at` then describe the last one that did; `history_truncated` appears after more than 32 states. It is absent in an image that sends no mail. `notices` (ADR 0041; absent in images older than it) counts what the enclave that answered did with the queue of notices about connections and receipts since it started, every count also at zero: `queued` (notices it queued that were not queued or sent before), `sent` (handed to SES), `failed` (attempts that will be tried again), `dropped` (given up 24 hours after their time), `skipped` (an account read and found gone, closing or blocked) and `refused` (entries that do not verify, or notices this image does not word, which nothing sends). A notice is queued once for the deployment and either enclave may send it, so each enclave's counts are its own; nothing of an account, a key or anyone's event is in them. The verifier prints them and warns when `dropped` or `refused` is above zero; it fails on none of them: a notice tells, the app shows, and what the image does with the queue is what its measurements say. A regression of the record of an account is listed in `anchors.regressions` without the account's name. One of a mailbox's record or an account's list of mailboxes (strict records, ADR 0026) is listed under the name `mailbox`, with `fork: true` when the record has two commits of different digests at one version, `unlocked: true` when a commit has no copy S3 keeps locked as the enclave wrote it, and `lost: true` when its newest commit names a value the table no longer holds (whoever writes to the table deleted or changed it); the verifier fails on each, naming which. Pausing and resuming a mailbox is counted in `admin`, never a regression. A regression of one of the model's records (its key, a month's spend, the canary's state, the alarms: strict records, ADR 0028) is listed under the name `model`, with `fork`, `unlocked` or `lost` and without its versions, which for a month's spend would say how much was spent.

`admin` counts what the operator did to accounts through this enclave since it started, by kind: an account blocked or unblocked, a deletion asked or cancelled, the devices of an account signed out, invitations made (one by one) and one revoked, the application of a provider set or removed, an admin made or removed, and a mailbox paused or resumed (`mailbox_paused`, `mailbox_resumed`, from the release that builds mailboxes per account, ADR 0026; never which mailbox: the operator cannot read, disconnect or empty one), and the model's key set or removed (`model_key_set`, `model_key_removed`, ADR 0028; never the key or its fingerprint). It counts what the operator does with the token and what the admins do in the web app together. Every kind the enclave knows is there, also at zero; a later image may count more kinds, which the verifier prints by name. It never holds an account, an address or an identifier. It is absent in an image without accounts and in images up to v0.10.0. The verifier checks nothing of it.

`connections` is absent in an image without connections. `refused` counts, by a fixed reason of the image (never one taken from what a vendor sent), what this enclave refused and a few events (`allowed`, `denied`, `token_issued`, `revoked_person`, `revoked_vendor`) since it started, every reason listed at zero; `decisions_full` counts the messages held, undecided, in a request because a rule's decision had no room in its day (whether the day is spent or its last block waits for its pending decisions), once per request, never a gauge of full days (ADR 0031, "Bounds"). In `anchors`, a regression of a connection's, an account's list's, a token family's or a record of assertions' record is named `connection`, without its versions. `receipts.incidents` is the deployment's record of incidents as this enclave last read it (every ten minutes): `recorded` is how many entries any enclave ever recorded, found by listing their locked commits, so an entry whose value was deleted still counts. `receipts.audit` is the deployment's newest finished pass of the daily audit, read strictly from its record, so both enclaves report the same one; it is absent until the first pass finishes, minutes after a new deployment starts. `receipts.broken` is this enclave's findings not yet recorded (its record pending or failed); zero means nothing waits to be recorded, not that everything was checked. A regression of any record of the chain (a link, an entry of the catalogue, a pass, an entry of the record of incidents) is named `receipt link`, without its name, which holds an account's identifier, or its version. Nothing here names an account, a link's number or a digest.

`root` is absent in an image that stores no mail. `from` is present when another release's enclave handed the root secret over. `history` and `gaps` are every state of the key's policy and grants the enclave has read since it started, and the periods in which it could not read. Releases up to v0.7.0 also report `kms`, the key they wrapped data keys with directly, with the same `history` and `gaps`. If KMS cannot be reached, or the image has no root secret yet, the endpoint answers 503.

## The root secret

Everything the enclave stores is sealed under one root secret, which only enclaves ever hold (ADR 0013). `permaily-verify` checks the key that locks it for the verified image, with the same function the enclave uses (`kmspolicy.Sealed`):

- the policy lets this image, and no other, decrypt;
- no statement lets anyone use the key without proving an image, except creating data keys under it, which opens nothing;
- nobody may replace the policy (`kms:PutKeyPolicy`) and no grant exists, so nobody in the account can change any of this;
- the key material was generated inside KMS, in one region;
- the enclave read the policy within the last 5 minutes, and every state it has read since it started (once a minute) passes the same check. AWS can restore the account owner's access to such a key through its Support; if that ever happened, this is where it would show, with the times.

It then prints where the root secret came from: created inside an enclave of this image, or handed over by the enclave of another release, which this image accepted only after checking that release's build provenance in the public transparency log. You can make the same check on any release with `gh attestation verify pcrs.json --repo permaily-org/permaily`.

What this does not prove: that the code of every earlier release in the chain was good (each was public); what AWS itself does inside KMS; and the state of the policy before the enclave started or while it could not reach KMS, which is printed as the start time and as warnings. v0.7.0 read its key once, when it started, and the verifier says so.

## Test deployments

Images built with `.#eif-staging` use Let's Encrypt staging. Their PCRs differ from the production image and their certificates are not publicly trusted:

```sh
nix build .#eif-staging --out-link result-staging
curl -sO https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x1.pem
go run ./cmd/permaily-verify -pcrs result-staging/pcrs.json -ca-file letsencrypt-stg-root-x1.pem
```

## Planned

`scripts/verify.sh` also compares the reproduced PCRs with the ones published in the release and checks their Sigstore build provenance with `gh attestation verify` when `gh` is installed. Certificate Transparency is checked at verification time, so every half hour by the scheduled job; nothing follows the logs between two checks.
