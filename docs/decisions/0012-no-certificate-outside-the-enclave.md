# 0012 No certificate valid for the enclave hosts outside the enclave

Status: Accepted. Applied on 2026-11-10; verified on the production instance with a staging image.
Date: 2026-11-10

## Context

Attestation binds the TLS key of one connection: the one the verifier makes. A browser cannot verify, so it trusts any certificate valid for `app.permaily.com`. Whoever controls DNS and holds such a certificate could serve the app from an ordinary server.

Certificate Transparency showed four unexpired certificates for `*.permaily.com` on 2026-11-10, all with keys outside the enclave and all valid for `app.`, `api.`, `mcp.` and `inbox.` (not for `mta-sts.inbox.`, two labels deep):

| Issued | Issuer | Holder of the key | Expires |
|---|---|---|---|
| 2026-09-07 | Let's Encrypt | Cloudflare (Universal SSL) | 2026-12-06 |
| 2026-11-06 | Let's Encrypt | Cloudflare (Universal SSL) | 2027-02-04 |
| 2026-11-06 | SSL.com | Cloudflare (Universal SSL, backup) | 2027-02-04 |
| 2026-11-09 | Amazon | AWS (Amplify's managed certificate for the site) | 2027-05-26 |

The CAA records of each enclave host did not help: a wildcard is authorized by the CAA records of the name it is issued for, `permaily.com`.

## Options

1. **Stop issuing wildcards for the domain** and keep the host names.
2. **Move the enclave hosts one label down** (`app.enclave.permaily.com`), out of reach of `*.permaily.com`. Changes every URL of the product and leaves the wildcards in place.
3. **Do nothing and rely on verification.** Only people who run the verifier are protected.

## Decision

Option 1, in two parts.

Applied on 2026-11-10:

- Cloudflare Universal SSL is disabled for the zone. Every record is DNS only, so nothing used it. It is a dashboard setting (SSL/TLS > Edge Certificates): the OpenTofu token only has DNS permissions.
- The public site uses its own ACM certificate for exactly `permaily.com`, `www.` and `beta.` (`aws_acm_certificate.site`, in `us-east-1` because CloudFront takes certificates only from there), instead of Amplify's managed wildcard.
- CAA at the apex: `issuewild ";"` (nobody may issue a wildcard) and `issue` for Amazon only. The enclave hosts keep `issue "letsencrypt.org"`.

Implemented in release v0.4.0:

1. **One TLS key and one ACME account, sealed.** The enclave generates both keys once and stores them, with the last certificate chain, as one record sealed with a data key that KMS gives only to an attested image (`internal/tlsacme.Sealed`, context `purpose=tls-identity`). There is one record per CA, so a staging image and a production image never share an account. A restart or an upgrade loads the record and serves the stored certificate; the CA is asked again only at two thirds of the certificate's lifetime. If the record cannot be loaded the enclave waits and retries: it never makes up another key.
2. **`/status` reports** the key's hash, when it was created, whether it is sealed, and the ACME account URL.
3. **CAA on the enclave hosts** names that account and the only validation method the enclave can pass: `letsencrypt.org; accounturi=<account>; validationmethods=tls-alpn-01` (`var.acme_account_uris`).
4. **`permaily-verify` checks** (`pkg/certwatch`): the CAA records of every host match the account the enclave reports, the domain forbids wildcards, and Certificate Transparency (through Cert Spotter's API) shows no unexpired certificate valid for a host with another key issued since the enclave's key was created.

## Consequences

- No new wildcard can be issued while the CAA record stands, and neither Cloudflare nor Amplify asks for one any more.
- The four certificates above stay valid until they expire; they cannot be revoked by us. The verifier counts them as "older" (issued before the enclave's key existed) and says until when they are valid: the last on 2027-05-26.
- The baseline is the creation time of the enclave's key, which the enclave reports. Deleting the sealed record would make the next enclave create a new key with a later time and hide earlier certificates from the check, so the verifier prints that time: a change is a visible event.
- The verifier trusts Cert Spotter's view of the logs to be complete, and DNS is still the operator's: CAA records can be changed between checks. Certificate Transparency is what makes a certificate issued that way visible afterwards.
- The TLS key now exists outside the enclave, sealed. It is protected exactly like stored mail: whoever the KMS key policy lets in can open it, which is what ADR 0010 checks.
- In debug mode KMS refuses the data key (the PCRs are zero), so a debug enclave gets no TLS key and serves nothing; its console still shows why it fails.
- A restart no longer issues a certificate, which also ends the pressure on Let's Encrypt's limit of five identical certificates per week.
- `us-east-1` is used for one resource, the site's certificate. Everything else stays in `us-east-2` (ADR 0004).
- Never turn on the Cloudflare proxy or Universal SSL for this zone, and never request a wildcard for it.

## To verify

- Done on 2026-11-10: the site serves a certificate for its three names only; `dig CAA permaily.com` shows `issuewild ";"`; Cloudflare reports Universal SSL disabled.
- Done on 2026-11-10 with a staging image: the key, the certificate (same serial) and the account survived two restarts; the verifier failed while the CAA records did not name the account and passed once they did.
- After 2027-05-26: the verifier reports no older certificates.
