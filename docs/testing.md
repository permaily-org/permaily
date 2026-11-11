# Testing

## What counts as a test

- A test asserts an observable outcome and fails when the code breaks. Break the code on purpose once and watch the test fail.
- No snapshot-only tests for logic, and no committed `.skip` or `.only`.
- Mock only true external boundaries (NSM, ACME CA, Sigstore, network, clock). Prefer fakes (`internal/nsm/fakensm`, `pkg/attestation/attestationtest`, `pkg/provenance/provenancetest`) over call-recording mocks.
- Every bug fix starts with a failing test.
- Security checks need a negative test that tries to break them: every rejection path in `pkg/attestation` has one (wrong nonce, wrong user data, MITM, debug PCRs, different image, stale, wrong root, tampered, malformed).

## Layers

| Layer | Tool | Where | Runs |
|---|---|---|---|
| Unit and local end to end (Go) | `go test`, `httptest` | `*_test.go` | `make check`, CI |
| Fuzzing | Go native fuzzing | `pkg/attestation`, `pkg/kmspolicy`, `pkg/provenance`, `internal/keys` (CMS), `internal/inbox` (mail parser) | `make fuzz`, CI |
| Differential | `pkg/provenance` against sigstore-go, on the published release v0.5.0 and on bundles from the test authority | `tests/sigstore-differential` (its own Go module, Go 1.27) | CI `sigstore-differential`; locally `cd tests/sigstore-differential && go test ./...` |
| Storage contract | DynamoDB Local and S3Mock | `internal/store/store_test.go` | CI services; locally with `PERMAILY_TEST_DYNAMODB` and `PERMAILY_TEST_S3` |
| ACME issuance and renewal | Pebble 2.6.0 (test CA that accepts every challenge) | `internal/tlsacme` (`TestObtainAgainstACME`) | CI service; locally with `PERMAILY_TEST_ACME` |
| Whole service, dev mode | SMTP client and HTTPS | `cmd/permaily-enclave/main_dev_test.go` | `go test -tags dev ./cmd/permaily-enclave`, CI |
| Mutation | gremlins | `pkg/attestation` | manual (`mutation` workflow) |
| Reproducibility | Nix | CI `eif` + `reproducible` jobs | every PR |
| Site content | Vitest on the built HTML | `site/tests/` | `bun run test`, Amplify build, CI |
| Accessibility and CSP | Playwright + axe | `site/e2e/` | `bun run test:e2e`, CI |
| End to end against production | `permaily-verify` | `scripts/verify.sh` | after each deploy |
| Production, continuously | the latest release's `permaily-verify` | `scripts/verify-production.sh` | workflow `verify-production`, scheduled twice an hour |
| Interception of production | Go test with a local TLS proxy | `cmd/permaily-verify/production_test.go` (`-tags production`) | after each deploy |

Fuzz runs are bounded by a number of executions (`-fuzztime=<n>x`), not by time: with a time limit Go's fuzzer sometimes fails with `context deadline exceeded` when the limit ends in the middle of an execution, which broke CI without a real finding.

## Thresholds

Coverage per Go package is in `scripts/coverage-thresholds.txt`, set to the measured value rounded down. Raise them as tests improve; never lower them.

| Package | Threshold | Measured 2026-11-10 |
|---|---|---|
| `pkg/attestation` | 85 (target 95) | 85.5 |
| `pkg/kmspolicy` | 99 | 99.0 |
| `pkg/certwatch` | 99 | 99.0 |
| `pkg/provenance` | 97 | 97.3 |
| `internal/tlsacme` | 90 (with Pebble) | 90.3 |
| `internal/router` | 100 | 100.0 |
| `internal/httpapi` | 89 | 89.7 |
| `internal/webui` | 75 | 75.0 |
| `internal/hostproxy` | 73 | 73.7 |
| `internal/enclave` | 87 | 87.7 |
| `internal/envelope` | 91 (target 95) | 91.9 |
| `internal/keys` | 88 (target 95) | 88.8 |
| `internal/inbox` | 84 | 84.7 |
| `internal/msgauth` | 86 | 86.0 |
| `internal/smtpd` | 92 | 92.5 |
| `internal/store` | 88 (with emulators) | 88.1 |

Mutation testing of `pkg/attestation` (gremlins 0.6.0, 2026-11-09): efficacy 100% (29 killed, 0 lived, 22 timed out), mutator coverage 55.77%. Thresholds: efficacy 90, mutator coverage 55.

## TODO

- [x] `internal/tlsacme`: issuance and renewal tested against a local ACME server (Pebble) in CI. The TLS-ALPN-01 answer itself is only exercised by real deployments, because Pebble runs with every challenge accepted.
- [ ] `internal/enclave` and `cmd/*`: test the vsock paths with a loopback vsock in CI or a fake listener (the log sink is tested over TCP).
- [ ] Raise `pkg/attestation` coverage to 95% (covers the 23 mutants not reached today).
- [x] A real attestation document captured from the first deployment, committed as a fixture (`pkg/attestation/testdata`), verified in `TestVerifyRealDocument`.
- [ ] Web app tests (Vitest + Testing Library, axe per component) when it talks to the backend.
