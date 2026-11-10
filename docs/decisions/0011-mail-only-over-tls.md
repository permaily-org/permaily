# 0011 Accept mail only over TLS and tell senders to require it

Status: Accepted. Verified on the production instance with a staging image (2026-11-10).
Date: 2026-11-10

## Context

Mail reaches the enclave through the parent instance, which is untrusted. SMTP starts in clear text and upgrades with STARTTLS, so whoever forwards the bytes can remove the STARTTLS offer and read a message the sender then delivers in clear. `permaily-verify` only checked HTTPS; nothing checked port 25.

## Options

1. **Opportunistic TLS** (what F1 shipped): STARTTLS offered, MTA-STS in `testing` mode. A stripped STARTTLS only produces a report.
2. **MTA-STS `enforce` plus an MX that refuses clear text**, with the verifier checking the policy, the DNS records and port 25.
3. **DANE** (TLSA records with DNSSEC). Stronger against a DNS operator, but Gmail does not use DANE for outbound mail, and Gmail is where forwarded mail comes from.

## Decision

Option 2.

- The MX answers `530 5.7.0` to `MAIL` on a connection without TLS, so nothing is stored that arrived in clear at the enclave.
- The MTA-STS policy, served by the enclave on `mta-sts.inbox.permaily.com`, says `mode: enforce` for `inbox.permaily.com` only, for one week. A sender that honors it (Gmail, Outlook and others do) refuses to deliver unless the MX presents a valid certificate for that name.
- The MX, MTA-STS and TLS-RPT records are published by default (`mail_dns_enabled`).
- `permaily-verify`, for the default hosts of a domain, checks that the policy is served with the attested key and says `enforce` for that MX only with `max_age` of at least a day, that DNS has exactly one `_mta-sts` record and an MX that points only at the inbox host, and that port 25 answers STARTTLS with a certificate a sender would accept and whose key is the attested one. If port 25 cannot be reached from the verifier's network it warns, or fails with `-require-smtp`.

## Consequences

- A sender without STARTTLS cannot deliver. Accepted: the product is forwarding from large providers, which all use TLS.
- What stays outside the proof: a sender that does not implement MTA-STS can still be made to deliver in clear to whoever answers on port 25 (the enclave itself would refuse it); and DNS is ours, so removing the `_mta-sts` record stops new senders from applying the policy, while senders that cached it keep enforcing for up to a week. The verifier fails when the record is missing.
- A certificate for the MX name issued to someone else would satisfy MTA-STS. That is the certificate question (CAA and Certificate Transparency), tracked in the threat model.
- Changing the policy needs a new `id` in the `_mta-sts` record (`deploy/tofu/mail.tf`).

## To verify

- Done on 2026-11-10 against the production instance: `MAIL` without TLS answered 530; the verifier passed from a server with port 25 open and warned from a network that blocks it.
- Still to do: a message forwarded from Gmail with the release image.
