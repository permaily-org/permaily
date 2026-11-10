# 0012 No certificate valid for the enclave hosts outside the enclave

Status: Accepted. First part applied on 2026-11-10; the rest is planned.
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

Planned, in this order:

1. Seal the TLS key and the ACME account key with KMS, so they survive restarts and upgrades, and publish them in `/status`.
2. CAA on the enclave hosts with `accounturi` (that account) and `validationmethods=tls-alpn-01`.
3. `permaily-verify` reads Certificate Transparency and fails when an unexpired certificate valid for an enclave host has a key that is not the attested one.

## Consequences

- No new wildcard can be issued while the CAA record stands, and neither Cloudflare nor Amplify asks for one any more.
- The four certificates above stay valid until they expire; they cannot be revoked by us. Until 2027-05-26 the planned Certificate Transparency check must ignore certificates issued before 2026-11-10, and say so.
- DNS is still the operator's: the CAA records can be changed. Certificate Transparency is what makes a new certificate visible; without step 3 nobody is told.
- `us-east-1` is used for one resource, the site's certificate. Everything else stays in `us-east-2` (ADR 0004).
- Never turn on the Cloudflare proxy or Universal SSL for this zone, and never request a wildcard for it.

## To verify

- Done on 2026-11-10: the site serves a certificate for its three names only; `dig CAA permaily.com` shows `issuewild ";"`; Cloudflare reports Universal SSL disabled.
- After 2027-05-26: Certificate Transparency lists no unexpired certificate for an enclave host other than the enclave's own.
