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
| `permaily-verify` passes against production on every host | `scripts/verify.sh` from a fresh clone (v0.3.0): image rebuilt in Docker, same PCRs as the release, Sigstore provenance; `permaily-verify -require-smtp` of v0.7.0 from a server with port 25 open, with the root secret, KMS (policy history included), mail and certificate checks | covered in production (2026-11-10) |
| Debug mode, a modified image and a MITM fail against production | Debug mode (`PCRs are zero`) and the PCRs of another release (`PCR mismatch`) rejected on the production instance with a staging image; a TLS proxy in front of v0.7.0 rejected by `TestProductionMITM` (`-tags production`) | covered in production (2026-11-10) |
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
| A key policy that lets another image, or anyone without an image, use the mail key fails verification | `TestAnalyzeProblems`, `TestCheckKMS`, `TestFetchStatus`; on the production instance a second allowed image and an added `kms:Decrypt` statement both failed `permaily-verify`, and creating a grant was denied | covered in production (2026-11-10, staging image; `scripts/verify.sh v0.3.0` passes with these checks) |
| A key policy that was opened and closed again while the enclave ran still fails verification, and production is verified on a schedule in public | `TestPolicyReaderHistory`, `TestCheckKMSHistory`; workflow `verify-production`; on the production instance a statement granting `kms:Decrypt` was added for 80 seconds and removed, and `permaily-verify` failed afterwards naming the statement and the time; v0.6.0 was deployed by changing the allowed image in one step, reports the policy as unchanged since it started, and a `verify-production` run passes against it | covered in production (2026-11-10, staging image and v0.6.0) |
| Nobody can change who may use the key that locks the root secret, and only its image can decrypt with it | `TestSealed`; on a key created by `releases.tf`, the account administrator was denied `PutKeyPolicy` (with and without the lockout bypass), `CreateGrant`, `Encrypt`, `EnableKeyRotation`, `UpdateKeyDescription`, `DisableKey`, `ReplicateKey`, `TagResource` and `Decrypt` of a data key they had just created under it | covered in production account (2026-11-10, staging images) |
| The running enclave hands the root secret only to a public release newer than itself, and the next image has the same data | `TestRootHandoff`, `TestHandoffRefuses`, `TestHandoff`; on the production instance a staging image created its root secret, handed it to a later staging image, which started with the same TLS key and certificate and verified as `handed over by the enclave of release ...`; it refused a production release (`not a release`), an image logged five seconds earlier and a handoff back (`published before the running one`), and a request without the admin token | covered in production account (2026-11-10, staging images) |
| An image never takes a root secret it was not given by an enclave of a public release, and never starts a second deployment | `TestRootRefusesPlantedRecords`, `TestRootDoesNotCreateWhenTheStoreFails`, `TestRootInDebugMode`; on the production instance an image started before the handoff waited without creating anything, and an image that found a copy of another image's record under its name refused it | covered in production account (2026-11-10, staging images) |
| A verifier can check that the key locking the root secret belongs to the verified image alone, for good | `TestCheckRoot`, `TestPrintRoot`; `permaily-verify -require-smtp` of v0.7.0 against production from a server, and against both staging images | covered in production (2026-11-10, v0.7.0) |
| Data keys wrapped by the old mail key move under the root secret without losing mail or the TLS key | `TestRootRewrapsLegacyKeys`, `TestMailboxKeyMovesUnderTheRootSecret`, `TestSealedMovesUnderTheRootSecret` ; v0.7.0 created the root secret in production, moved the mailbox key and the TLS identity under it (same TLS key and certificate serial), restarted with no image allowed on the old key and still read stored mail; the old key was then retired, after which the account administrator was denied `PutKeyPolicy`, `CreateGrant` and `GenerateDataKey` on it and the verifier reports `releases data keys to no image` | covered in production (2026-11-10, v0.7.0) |
| The enclave obtains a data key from KMS with its attestation; a non-allowlisted image is denied | v0.2.0 serves the mailbox with only its PCR0 allowlisted; the same image in debug mode gets `AccessDeniedException` on `GenerateDataKey` | covered in production (2026-11-10) |
| The MX stores nothing that arrived without TLS, and a verifier can check that senders are told to require TLS and reach the attested key | `TestRefusesMailWithoutTLS`, `TestVerifySMTP`, `TestCheckMail`; on the production instance `MAIL` without TLS answered 530 and `permaily-verify -require-smtp` passed from a server | covered in production (2026-11-10, staging image; `scripts/verify.sh v0.3.0` passes with these checks) |
| The TLS key and ACME account survive restarts and upgrades, sealed | `TestManagerSurvivesRestart`, `TestObtainAgainstACME` (Pebble); on the production instance the key, the certificate serial and the account were the same after two restarts | covered in production (2026-11-10, staging image and v0.4.0) |
| Only the enclave's ACME account can get certificates for its hosts, no wildcard can be issued, and a logged certificate with another key fails verification | `TestCheckCertificates`, `pkg/certwatch` tests; on the production instance the verifier failed until the CAA records named the account, then passed | covered in production (2026-11-10, staging image) |
| No unexpired certificate valid for an enclave host has a key outside the enclave | The verifier reports the older ones: four `*.permaily.com` certificates held by Cloudflare and AWS and the certificates of earlier releases, the last expiring 2027-05-26 (ADR 0012) | partial until 2027-05-26 |
| Gmail delivers forwarded mail to the MX over TLS (MTA-STS) | MX, MTA-STS and TLS-RPT records published; a message sent from Gmail arrived at v0.3.0 with the policy in `enforce`, and the MX refuses mail without TLS | covered in production (2026-11-10) |

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

1. Send and forward mail from a Gmail account with the release image and record the Gmail row.
2. After 2027-05-26, confirm the verifier reports no older certificates.
