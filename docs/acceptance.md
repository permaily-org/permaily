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
| `permaily-verify` passes against production on all four hosts | `scripts/verify.sh v0.2.0` from a fresh clone: image rebuilt in Docker, same PCRs as the release, Sigstore provenance | covered in production (2026-11-10) |
| Debug mode, a modified image and a MITM fail against production | Debug mode (`PCRs are zero`) and the PCRs of another release (`PCR mismatch`) rejected on the production instance with the staging image; the MITM only in `TestVerifyRemote` | partial (2026-11-10) |
| A certificate is issued inside the enclave with TLS-ALPN-01 | v0.2.0 serves a Let's Encrypt certificate for its five names, with its key bound in the attestation | covered in production (2026-11-10) |
| A real attestation document verifies against the embedded AWS root, and fails with another nonce, TLS key, image or date | `TestVerifyRealDocument` (document from v0.2.0 in `pkg/attestation/testdata`) | covered |

## F1: inbox

| Criterion | Evidence | Status |
|---|---|---|
| A message sent over SMTP with STARTTLS is readable through the API with the token, and not without it | `TestMailEndToEnd` (dev mode, whole service); sent from another server to `inbox.permaily.com:25` | covered in production (2026-11-10) |
| The MX rejects unknown recipients, oversize messages, and fails temporarily (4xx) when storage fails | `TestRejections` | covered |
| The store never holds plaintext senders, subjects, bodies or attachment names | `TestStoreNeverSeesPlaintext`; scan of the production table and bucket after a delivery | covered in production (2026-11-10) |
| Sealed data cannot be opened with another key, context or after tampering | `TestSealOpen` | covered |
| Data keys come from KMS through Recipient and CMS (DER and BER), and a key other than the pinned one is rejected | `TestKMSRoundTrip`, `TestKMSRejectsOtherKey` | covered |
| The CMS and mail parsers never panic | `FuzzCMS`, `FuzzParse` (CI) | covered |
| The mailbox survives an enclave restart (data key unwrapped again) | `TestRestartKeepsAccess`; v0.2.0 read a message stored by the previous image | covered in production (2026-11-10) |
| Retention applies and expired mail is not served | `TestRetention`, store contract test | covered |
| DynamoDB and S3 storage behave as the in-memory store | `TestAWS` against DynamoDB Local and S3Mock (CI services) | covered |
| Gmail forwarding codes are recorded, verified only with a google.com DKIM signature | `TestForwardingConfirmation` | covered |
| Account administrators cannot obtain data keys from the mail KMS key | `aws kms generate-data-key` with the admin role returns AccessDenied | covered in production (2026-11-09) |
| A key policy that lets another image, or anyone without an image, use the mail key fails verification | `TestAnalyzeProblems`, `TestCheckKMS`, `TestFetchStatus`; on the production instance a second allowed image and an added `kms:Decrypt` statement both failed `permaily-verify`, and creating a grant was denied | covered in production (2026-11-10, staging image) |
| The enclave obtains a data key from KMS with its attestation; a non-allowlisted image is denied | v0.2.0 serves the mailbox with only its PCR0 allowlisted; the same image in debug mode gets `AccessDeniedException` on `GenerateDataKey` | covered in production (2026-11-10) |
| Gmail delivers forwarded mail to the MX over TLS (MTA-STS) | needs the MX, MTA-STS and TLS-RPT records (`mail_dns_enabled`) and a forwarded Gmail account | missing |

## Public site

| Criterion | Evidence | Status |
|---|---|---|
| Every page has language, title, description, one h1, canonical | `site/tests/content.test.ts` | covered |
| Every page ships a strict CSP | `site/tests/content.test.ts`, `site/e2e/a11y.spec.ts` (no violations) | covered |
| No WCAG 2.2 AA violations, light and dark, desktop and mobile | `site/e2e/a11y.spec.ts` | covered |
| Internal links resolve | `site/tests/content.test.ts` | covered |
| The footer never shows an example hash as the real build | `site/tests/content.test.ts` | covered |
| Security headers are served | `.github/workflows/site-smoke.yml` | covered in production (2026-11-09) |

## Follow-ups

1. Publish the mail DNS (`mail_dns_enabled = true`), forward a Gmail account and record the Gmail row; then switch MTA-STS to `enforce`.
2. Pebble-based tests for `internal/tlsacme`.
3. Run the MITM check against production (a proxy with another certificate in front of the enclave).
