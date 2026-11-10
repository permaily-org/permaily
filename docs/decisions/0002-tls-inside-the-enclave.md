# 0002 Terminate TLS inside the enclave with ACME TLS-ALPN-01

Status: Accepted. Verified in production with release v0.2.0 (2026-11-10).
Date: 2026-11-08

## Context

Attestation proves which image runs, but a client must also know its connection ends inside that enclave, not at the untrusted parent, a proxy or a CDN.

## Options

1. **ACME inside the enclave, TLS-ALPN-01**: the enclave owns the key, answers the challenge on 443, and binds `sha256(SPKI)` into the attestation `user_data`. No DNS credentials in the enclave.
2. **ACME DNS-01**: allows wildcards, but puts a Cloudflare token inside the enclave.
3. **Self-signed certificate + attestation only**: no public CA, but browsers warn and nothing works without a custom client.
4. **TLS at a load balancer**: breaks the guarantee.

## Decision

Option 1, with one multi-SAN certificate (app, api, mcp, inbox) and one key for the enclave lifetime, so `user_data` is a single value. Implemented with `golang.org/x/crypto/acme` (`internal/tlsacme`); Brave's nitriding (the reference design) is archived, so it is used only as inspiration.

## Consequences

- Each enclave restart issues a new certificate (Let's Encrypt limit: 5 duplicates per week). Test deployments use Let's Encrypt staging.
- The parent could get a certificate for the names by answering the challenge with another ACME account. The verifier detects it per connection; F1 adds CAA `accounturi` with an account key sealed by KMS.

## To verify

- First production issuance and `permaily-verify` PASS on all four hosts.
- Renewal at two thirds of the lifetime keeps the same key.
