# Testing

## What counts as a test

- A test asserts an observable outcome and fails when the code breaks. Break the code on purpose once and watch the test fail.
- No snapshot-only tests for logic, and no committed `.skip` or `.only`.
- Mock only true external boundaries (NSM, ACME CA, Sigstore, network, clock). Prefer fakes (`internal/nsm/fakensm`, `pkg/attestation/attestationtest`, `pkg/provenance/provenancetest`) over call-recording mocks.
- Every bug fix starts with a failing test.
- Security checks need a negative test that tries to break them: every rejection path in `pkg/attestation` has one (wrong nonce, wrong user data, MITM, debug PCRs, different image, stale, wrong root, tampered, malformed).
- What is stored outside the enclave is tested with the storage in the hands of someone else: the test writes to it by hand (`store.Memory.SetEntry`, `SetRecord`, `DeleteRecord`) and nothing it writes may let anyone in (`TestStorageWrittenByHand`).

## Layers

| Layer | Tool | Where | Runs |
|---|---|---|---|
| Unit and local end to end (Go) | `go test`, `httptest` | `*_test.go` | `make check`, CI |
| Fuzzing | Go native fuzzing | `pkg/attestation`, `pkg/kmspolicy`, `pkg/provenance` (bundles of both Rekor logs, timestamps, and trust root updates), `internal/keys` (CMS), `internal/inbox` (mail parser), `internal/webauthn` (what a browser answers when a passkey is made and used), `internal/idp` (ID tokens, key sets and every answer of a provider) | `make fuzz`, CI |
| Differential | `pkg/provenance` against sigstore-go, on the published release v0.5.0, on a signature recorded in the public Rekor v2 log, and on bundles from the test authority shaped like both logs; and its TUF checks against go-tuf, on Sigstore's metadata from TUF root 12 to 16 and on the test authority's | `tests/sigstore-differential` (its own Go module, Go 1.27) | CI `sigstore-differential`; locally `cd tests/sigstore-differential && go test ./...` |
| Differential | `internal/webauthn` against go-webauthn, on creations and uses from two software authenticators for each algorithm, valid and broken in many ways; every case where the two differ on purpose is named with its reason | `tests/webauthn-differential` (its own Go module) | CI `webauthn-differential`; locally `cd tests/webauthn-differential && go test ./...` |
| Differential | `internal/idp`'s check of an ID token against go-oidc, on right and broken tokens of Google and Microsoft; it never accepts what the library refuses, and every case it refuses and the library accepts is named with its reason | `tests/idp-differential` (its own Go module) | CI `idp-differential`; locally `cd tests/idp-differential && go test ./...` |
| Captured answers | `internal/webauthn` on 96 public captures of what real authenticators and browsers answered (YubiKeys and other security keys, Apple, Android, Windows Hello with and without a TPM, iCloud Keychain, Google Password Manager, 1Password; the vectors of the WebAuthn specification), each with its source and licence; one that is refused must be refused for the reason its file states | `internal/webauthn/testdata/vectors` (`TestCapturedAnswers`, `TestCapturedAnswersCoverTheKinds`); the same captures against go-webauthn in `tests/webauthn-differential` | `make check`, CI |
| Commits against S3 | a real bucket with Object Lock: conditional writes, locks, delete markers, deletion refused | `internal/store` (`TestS3CommitsAgainstS3`) | by hand, with `PERMAILY_TEST_ANCHOR_BUCKET=permaily-anchors-staging-<account>` and credentials; skipped otherwise |
| Storage contract | DynamoDB Local and S3Mock | `internal/store/store_test.go` | CI services; locally with `PERMAILY_TEST_DYNAMODB` and `PERMAILY_TEST_S3` |
| ACME issuance and renewal | Pebble 2.6.0 (test CA that accepts every challenge) | `internal/tlsacme` (`TestObtainAgainstACME`) | CI service; locally with `PERMAILY_TEST_ACME` |
| Whole service, dev mode | SMTP client and HTTPS: mail in and read back; an invitation, a sign-up by email, a session and signing out | `cmd/permaily-enclave/main_dev_test.go` | `go test -tags dev ./cmd/permaily-enclave`, CI |
| Mutation | gremlins | `pkg/attestation` | manual (`mutation` workflow) |
| Reproducibility | Nix | CI `eif` + `reproducible` jobs | every PR |
| Web app | Vitest with Testing Library in jsdom, against a played `fetch`: each module of `web/src/lib`, the session gate, every step of signing in (mail and code, the link that asks first, passkey, recovery code, provider, confirming an address, account closing) and the Sign-in and account parts of Settings. Each component test file runs axe (WCAG 2.0 to 2.2, A and AA, without contrast, which jsdom cannot measure) and asserts what must never be stored or left in the address | `web/tests/` ([web.md](web.md), "Tests") | `bun run test` in `web/`, `make check`, CI `web` |
| Site content | Vitest on the built HTML | `site/tests/` | `bun run test`, Amplify build, CI |
| Accessibility and CSP | Playwright + axe | `site/e2e/` | `bun run test:e2e`, CI |
| End to end against production | `permaily-verify` | `scripts/verify.sh` | after each deploy |
| Sigstore, daily | the compiled-in trust root is the newest one Sigstore publishes, and `pkg/provenance` takes what it publishes; `pkg/provenance` accepts what GitHub attests today, and a signature recorded today in Rekor v2 | `scripts/sigstore-trust-root.sh` (`permaily-verify -trust-update`); `permaily-verify -provenance` on a fresh attestation and on a bundle from `tests/sigstore-differential/cmd/rekor2-sign` | workflow `sigstore-watch` |
| A change to the compiled-in trust root | it is exactly what Sigstore publishes today, and the code of the base branch accepts it as an update | `scripts/check-trust-root-change.sh` | CI `go`, on pull requests into `develop` |
| Each release, before it is cut | a running enclave would accept it: `-provenance` with the new verifier and the previous release's | step of `release.yml` | `release.yml` |
| Production, continuously | the latest release's `permaily-verify` | `scripts/verify-production.sh` | workflow `verify-production`, twice an hour |
| Interception of production | Go test with a local TLS proxy | `cmd/permaily-verify/production_test.go` (`-tags production`) | after each deploy |

Fuzz runs are bounded by a number of executions (`-fuzztime=<n>x`), not by time: with a time limit Go's fuzzer sometimes fails with `context deadline exceeded` when the limit ends in the middle of an execution, which broke CI without a real finding.

## Thresholds

Coverage per Go package is in `scripts/coverage-thresholds.txt`, set to the measured value rounded down. Raise them as tests improve; never lower them.

| Package | Threshold | Measured 2026-11-10 |
|---|---|---|
| `pkg/attestation` | 85 (target 95) | 85.5 |
| `pkg/kmspolicy` | 99 | 99.0 |
| `pkg/certwatch` | 99 | 99.0 |
| `pkg/provenance` | 99 | 99.4 |
| `internal/tlsacme` | 90 (with Pebble) | 90.3 |
| `internal/router` | 100 | 100.0 |
| `internal/httpapi` | 94 | 94.8 (2026-11-11) |
| `internal/webui` | 75 | 75.0 |
| `internal/hostproxy` | 73 | 73.7 |
| `internal/enclave` | 87 | 87.7 |
| `internal/envelope` | 94 (target 95) | 94.3 (2026-11-11) |
| `internal/keys` | 89 (target 95) | 89.5 |
| `internal/inbox` | 84 | 84.7 |
| `internal/msgauth` | 86 | 86.0 |
| `internal/smtpd` | 92 | 92.5 |
| `internal/store` | 96 (with emulators) | 96.1 (2026-11-12) |
| `internal/contracts` | 100 | 100.0 (2026-11-11) |
| `internal/domain` | 100 | 100.0 (2026-11-11) |
| `internal/services/accounts` | 98 | 99.1 (2026-11-12) |
| `internal/sesmail` | 98 | 98.6 (2026-11-11) |
| `internal/webauthn` | 99 | 99.4 (2026-11-11) |
| `internal/idp` | 100 | 100.0 (2026-11-11) |

Coverage of the web app is in `web/vitest.config.ts`, one threshold for each area that talks to the server, set the same way. The screens on sample data have none: they are still changing.

| Area of `web/src` | Statements, branches, functions, lines | Measured 2026-11-11 |
|---|---|---|
| `lib` | 98, 96, 100, 100 | 98.11, 96.13, 100, 100 |
| `components/SessionGate.tsx` | 96, 94, 100, 100 | 96.29, 94.82, 100, 100 |
| `pages/auth` | 97, 94, 98, 100 | 97.02, 94.43, 98.93, 100 |
| `pages/settings/signin` | 95, 90, 98, 99 | 95.15, 90.87, 98.85, 99.47 |
| `pages/settings/account` | 94, 87, 100, 99 | 94.91, 87.5, 100, 99.06 |

Mutation testing of `pkg/attestation` (gremlins 0.6.0, 2026-11-09): efficacy 100% (29 killed, 0 lived, 22 timed out), mutator coverage 55.77%. Thresholds: efficacy 90, mutator coverage 55.

## TODO

- [x] `internal/tlsacme`: issuance and renewal tested against a local ACME server (Pebble) in CI. The TLS-ALPN-01 answer itself is only exercised by real deployments, because Pebble runs with every challenge accepted.
- [ ] `internal/enclave` and `cmd/*`: test the vsock paths with a loopback vsock in CI or a fake listener (the log sink is tested over TCP).
- [ ] Raise `pkg/attestation` coverage to 95% (covers the 23 mutants not reached today).
- [x] A real attestation document captured from the first deployment, committed as a fixture (`pkg/attestation/testdata`), verified in `TestVerifyRealDocument`.
- [x] Web app tests (Vitest + Testing Library, axe per component): `web/tests/`, 395 tests. Twenty changes that break the code on purpose (any address taken by `leaveFor`, any reason or provider taken from the address, an invitation left in storage, focus not moved, and others) each failed at least one test.
- [ ] Web app tests in a real browser (Playwright, both themes, contrast), once a dev enclave serves the app in CI: see [accessibility.md](accessibility.md).
