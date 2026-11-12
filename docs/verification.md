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
8. Reads `GET /status` over a connection with that same TLS key and checks the KMS key that locks the root secret everything stored is sealed under (below). When the enclave sends mail of its own, it also checks who else SES would give a copy to (below). When it has accounts, it prints what the operator did to them through the enclave, and fails on nothing (below).

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

The workflow `verify-production` of the repository runs the latest release's verifier against the live service every half hour; its runs and their output are public under Actions. `scripts/verify-production.sh` runs the same check from your machine.

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

The operator, with the admin token, can do some things to an account through the enclave: block it and unblock it, ask to delete it and cancel a deletion the operator asked for, sign its devices out, and make and revoke invitations (ADR 0017). The admins do the same from the web app, and the super admin makes and removes admins (ADR 0022). The enclave counts each kind of what both do and serves the counts on `/status` under `admin`, with the time it started counting. The verifier prints them:

```
      what the operator did to accounts through this enclave since 2026-11-12T04:58:12Z: blocked an account 1 time; made an invitation 2 times
```

It fails on nothing here: blocking an account or asking to delete one is the operator's to do. It is printed so that it is seen, by anyone, twice an hour.

What the operator cannot do through the enclave is not counted because there is no way to do it: change an account's contact address, add or remove a way to sign in, turn "passkey only" off, or sign in as someone. Those would be ways into an account, and the image you verified has no route for them.

What this does not show: which account, on purpose; anything from before the enclave started, since the count is in memory and a restart, which the operator can cause, starts it again (the start time is printed); and what is written to the storage by hand, which the enclave refuses where it would be a way in (ADR 0016, ADR 0017) and which can still delete an account.

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
    "history": [{"domain_identity": "no_configuration_set", "address_identity": "absent", "first_seen": "...", "last_seen": "..."}]
  },
  "admin": {
    "since": "<when this enclave started counting: when it started>",
    "actions": {"block": 0, "unblock": 0, "delete": 0, "delete_cancel": 0, "sessions_close": 0, "invitations_made": 0, "invitation_revoked": 0, "provider_app_set": 0, "provider_app_removed": 0, "admin_made": 0, "admin_removed": 0}
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

`mail` says what SES would do with a message of the enclave besides delivering it, as the enclave read it: each identity is `no_configuration_set`, `configuration_set`, `not_verified` or `absent`. `failing` is true when the newest attempt got no answer to go by, and `state` and `checked_at` then describe the last one that did; `history_truncated` appears after more than 32 states. It is absent in an image that sends no mail. A regression of the record of an account is listed in `anchors.regressions` without the account's name. One of a mailbox's record or an account's list of mailboxes (strict records, ADR 0026) is listed under the name `mailbox`, with `fork: true` when the record has two commits of different digests at one version, and `unlocked: true` when a commit has no copy S3 keeps locked as the enclave wrote it; the verifier fails on either, naming which.

`admin` counts what the operator did to accounts through this enclave since it started, by kind: an account blocked or unblocked, a deletion asked or cancelled, the devices of an account signed out, invitations made (one by one) and one revoked, the application of a provider set or removed, and an admin made or removed. It counts what the operator does with the token and what the admins do in the web app together. Every kind the enclave knows is there, also at zero; a later image may count more kinds, which the verifier prints by name. It never holds an account, an address or an identifier. It is absent in an image without accounts and in images up to v0.10.0. The verifier checks nothing of it.

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
