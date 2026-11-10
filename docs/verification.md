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
8. Reads `GET /status` over a connection with that same TLS key and checks the policy of the KMS key that protects stored mail (below).

## Who can read stored mail

Stored mail is encrypted with keys that AWS KMS releases only to an enclave that proves its image. Which images are allowed is written in the key's policy, so the verifier checks that too:

- The enclave reads its key's policy and grants from KMS and serves them on `/status`. The verifier accepts the answer only when the connection uses the attested TLS key, so it comes from the verified image.
- Every statement of the policy that allows more than administration must require the enclave image (`kms:RecipientAttestation:ImageSha384`). A statement that does not, or that the verifier does not understand, fails the check.
- The only image allowed must be the one you verified. Another image fails the check, because it could decrypt the mail and you have not verified it.
- The key must have no grants, and the enclave's reading must be at most 5 minutes old.

During an upgrade the old and the new release are both allowed for a short time. Then verification fails unless you accept the other release by its PCR0: `permaily-verify -pcrs pcrs.json -kms-also-allow <PCR0 of the other release>`. Releases before v0.3.0 do not report the policy; verify them with `-skip-kms-policy`.

What this does not prove: the account's administrators can replace the policy later, and the verifier sees it only at the time of the check (it prints a note saying so). The policy's SHA-256 is printed so two checks can be compared.

## How mail gets in

Mail arrives over SMTP on port 25, which the HTTPS checks above do not cover. When no URLs are given, `permaily-verify` also checks:

1. The MTA-STS policy at `https://mta-sts.inbox.permaily.com/.well-known/mta-sts.txt`, accepted only when served with the attested TLS key. It must say `mode: enforce`, allow only `inbox.permaily.com` as MX, and last at least a day. This is what makes a sending server refuse to deliver without a valid certificate.
2. DNS: exactly one `_mta-sts.inbox.permaily.com` record (without it senders do not apply the policy) and an MX for `inbox.permaily.com` that points only at itself.
3. Port 25: it connects as a sending server would, upgrades with STARTTLS, requires a certificate valid for `inbox.permaily.com`, and requires its key to be the attested one. It sends no mail.

Many home and office networks block outbound port 25. Then step 3 cannot run: the verifier prints a warning and still exits 0. Run it from a server, or pass `-require-smtp` to make that a failure. `-skip-mail` skips these checks.

What this does not prove: a sender that does not implement MTA-STS may still deliver in clear to whoever answers on port 25 (the enclave itself refuses mail without TLS), and the DNS records can change after the check.

## Certificates outside the enclave

Everything above proves the connection the verifier makes. A browser cannot do that: it accepts any valid certificate for `app.permaily.com`. So the verifier also looks for certificates the enclave does not hold. When no URLs are given it checks:

1. What the enclave reports in `/status`: the hash of its TLS key (it must be the attested one), when the key was created, that it is kept sealed with KMS, and the URL of its ACME account.
2. CAA records, read through a DNS-over-HTTPS resolver: every enclave host must allow only Let's Encrypt, only that account and only `tls-alpn-01` validation, which is answered from inside the enclave; and the domain must forbid wildcards (`issuewild ";"`), because `*.permaily.com` would be valid for the hosts.
3. Certificate Transparency, read through Cert Spotter's API: every unexpired certificate valid for an enclave host and issued since the enclave's key was created must carry that key. One with another key is a failure and is listed.

Certificates with other keys issued before the enclave's key existed are counted as older, with the date the last one expires: four wildcards from before 2026-11-10 (ADR 0012) and the certificates of earlier releases, whose keys lived only in memory. Nothing can prove who holds an old key, so they are reported, not failed.

What this does not prove: it trusts Cert Spotter's view of the logs, the DNS records can change after the check, and a certificate issued before the key's creation time is not judged. That time is printed; if it changes between two checks, the key was replaced. `-skip-certificates` skips these checks (releases before v0.4.0 do not report their TLS key); `-doh-resolver` and `-ct-api` choose the services.

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

The build gives identical bytes on x86_64 and arm64 Linux (CI enforces this). Each release also publishes `pcrs.json`; `permaily-verify -release <tag>` uses it, which is weaker because it trusts CI instead of your own build.

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
  "kms": {
    "key_arn": "<the key compiled into the image>",
    "policy": "<the key policy document, as KMS returned it>",
    "grants": 0,
    "grants_truncated": false,
    "fetched_at": "<when the enclave read it, RFC 3339>"
  },
  "tls": {
    "spki_sha256": "<hex SHA-256 of the certificate key, as in the attestation>",
    "key_created_at": "<when the key was generated, RFC 3339>",
    "sealed": true,
    "acme_account": "<URL of the ACME account that asks for certificates>"
  }
}
```

`kms` is `null` in an image that stores no mail. If KMS cannot be reached the endpoint answers 503.

## Test deployments

Images built with `.#eif-staging` use Let's Encrypt staging. Their PCRs differ from the production image and their certificates are not publicly trusted:

```sh
nix build .#eif-staging --out-link result-staging
curl -sO https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x1.pem
go run ./cmd/permaily-verify -pcrs result-staging/pcrs.json -ca-file letsencrypt-stg-root-x1.pem
```

## Planned

`scripts/verify.sh` also compares the reproduced PCRs with the ones published in the release and checks their Sigstore build provenance with `gh attestation verify` when `gh` is installed. Certificate Transparency is checked at verification time; nothing watches it continuously yet.
