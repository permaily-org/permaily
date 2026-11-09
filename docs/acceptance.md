# Acceptance

Each criterion, the evidence that proves it, and its status: **covered** (a test or CI job proves it), **covered in production** (also checked against the live service, with a date), **partial**, **missing**, or **blocked**. Update a row in the same change that adds its evidence.

## F0: verifiable service

| Criterion | Evidence | Status |
|---|---|---|
| The enclave image is byte-identical when built on x86_64 and arm64 | CI `reproducible` job (`.github/workflows/ci.yml`) | covered |
| A rebuild on the same machine gives the same image | `nix build .#eif --rebuild` (2026-11-08) | covered |
| The image holds no build-machine paths | ADR 0003; no `/nix/store` strings in `permaily-enclave` (2026-11-09) | covered |
| An attestation signed outside the AWS Nitro chain is rejected | `TestVerify/untrusted_root_(default_AWS_root)`, `TestVerifyRemote/fake_attester_rejected_against_AWS_root` | covered |
| Debug mode (zero PCRs) is rejected | `TestVerify/debug_mode_zero_PCRs` | covered |
| A different image is rejected | `TestVerify/different_image` | covered |
| A replayed or stale attestation is rejected | `TestVerify/wrong_nonce`, `TestVerify/stale_document` | covered |
| TLS terminated outside the enclave is detected | `TestVerifyRemote/TLS_terminated_by_someone_else_(MITM)` | covered |
| The parser never accepts forged input | `FuzzVerify` (CI, 30 s per run) | covered |
| The embedded AWS root is the published one | `TestAWSRootFingerprint` | covered |
| Each host serves its content and security headers | `TestRouter` | covered |
| `/attestation` binds nonce and TLS key, validates input | `TestAttestation` | covered |
| The host proxy only tunnels to allowlisted destinations | `TestConnectProxy` | covered |
| `permaily-verify` passes against production on all four hosts | `scripts/verify.sh <tag>` | blocked (AWS Paid Plan) |
| Debug mode, a modified image and a MITM fail against production | manual negative runs | blocked (AWS Paid Plan) |
| A certificate is issued inside the enclave with TLS-ALPN-01 | first deployment | blocked (AWS Paid Plan) |

## Public site

| Criterion | Evidence | Status |
|---|---|---|
| Every page has language, title, description, one h1, canonical | `site/tests/content.test.ts` | covered |
| Every page ships a strict CSP | `site/tests/content.test.ts`, `site/e2e/a11y.spec.ts` (no violations) | covered |
| No WCAG 2.2 AA violations, light and dark, desktop and mobile | `site/e2e/a11y.spec.ts` | covered |
| Internal links resolve | `site/tests/content.test.ts` | covered |
| The footer never shows an example hash as the real build | `site/tests/content.test.ts` | covered |
| Security headers are served | `.github/workflows/site-smoke.yml` | blocked (Amplify not connected to this repo) |

## Follow-ups

1. First deployment: run every blocked row, record dates, commit the attestation fixture.
2. Pebble-based tests for `internal/tlsacme`.
