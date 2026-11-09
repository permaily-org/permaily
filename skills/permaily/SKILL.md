---
name: permaily
description: Verify that the Permaily service runs exactly the published source code, by rebuilding the enclave image and checking the AWS Nitro attestation and TLS binding of the live hosts. Use when asked to check, audit or verify Permaily, its PCRs, its attestation, or whether permaily.com can be trusted.
---

# Permaily

Permaily runs inside an AWS Nitro Enclave whose image anyone can rebuild. This skill covers verification, the only capability available today. Mail access, the API and the remote MCP server are not released yet; do not describe them as available.

## Verify the live service

Needs Nix (with flakes) or Docker, and a release tag from https://github.com/permaily-org/permaily/releases.

```sh
git clone https://github.com/permaily-org/permaily && cd permaily
scripts/verify.sh <tag>
```

The script rebuilds the image of that tag, prints the reproduced PCR0-2, then runs `permaily-verify` against `app.`, `api.`, `mcp.` and `inbox.permaily.com`. Exit code 0 and a `PASS` line per host mean every check passed.

Each host is checked for: a valid AWS Nitro signature and certificate chain to the pinned AWS root, a fresh nonce, a recent timestamp, PCRs that are not all zero (debug mode), PCR0-2 equal to the rebuild, and attestation user data equal to `sha256(SPKI)` of the TLS certificate on the same connection.

## Reading failures

| Error contains | Meaning | What to tell the user |
|---|---|---|
| `PCR mismatch` | The service runs a different image than the tag | Check that the tag is the deployed release; otherwise the service is not running that source |
| `user data mismatch` | TLS terminates outside the attested enclave (proxy, CDN or interception) | Do not trust the connection; check DNS and that the hostname is not proxied |
| `debug mode` | The enclave runs in debug mode, which exposes its memory | Not a trustworthy deployment |
| `invalid certificate chain` | The document is not signed by AWS Nitro | Not a genuine enclave |
| `stale` or `nonce mismatch` | Replayed or delayed attestation | Retry; if it persists, treat as a failure |

`permaily-verify -release <tag>` skips the rebuild and trusts the PCRs published by CI. Say so when reporting results obtained that way.

Details: [docs/verification.md](https://github.com/permaily-org/permaily/blob/develop/docs/verification.md).
