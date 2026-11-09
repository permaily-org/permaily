# Testing

## What counts as a test

- A test asserts an observable outcome and fails when the code breaks. Break the code on purpose once and watch the test fail.
- No snapshot-only tests for logic, and no committed `.skip` or `.only`.
- Mock only true external boundaries (NSM, ACME CA, network, clock). Prefer fakes (`internal/nsm/fakensm`, `pkg/attestation/attestationtest`) over call-recording mocks.
- Every bug fix starts with a failing test.
- Security checks need a negative test that tries to break them: every rejection path in `pkg/attestation` has one (wrong nonce, wrong user data, MITM, debug PCRs, different image, stale, wrong root, tampered, malformed).

## Layers

| Layer | Tool | Where | Runs |
|---|---|---|---|
| Unit and local end to end (Go) | `go test`, `httptest` | `*_test.go` | `make check`, CI |
| Fuzzing | Go native fuzzing | `pkg/attestation/fuzz_test.go` | `make fuzz`, CI (45 s) |
| Mutation | gremlins | `pkg/attestation` | manual (`mutation` workflow) |
| Reproducibility | Nix | CI `eif` + `reproducible` jobs | every PR |
| Site content | Vitest on the built HTML | `site/tests/` | `bun run test`, Amplify build, CI |
| Accessibility and CSP | Playwright + axe | `site/e2e/` | `bun run test:e2e`, CI |
| End to end against production | `permaily-verify` | `scripts/verify.sh` | after each deploy (blocked until the first deploy) |

## Thresholds

Coverage per Go package is in `scripts/coverage-thresholds.txt`, set to the measured value rounded down. Raise them as tests improve; never lower them.

| Package | Threshold | Measured 2026-11-09 |
|---|---|---|
| `pkg/attestation` | 80 (target 95) | 80.3 |
| `internal/router` | 95 | 96.3 |
| `internal/httpapi` | 75 | 77.8 |
| `internal/webui` | 70 | 75.0 |
| `internal/hostproxy` | 65 | 65.3 |

Mutation testing of `pkg/attestation` (gremlins 0.6.0, 2026-11-09): efficacy 100% (29 killed, 0 lived, 22 timed out), mutator coverage 55.77%. Thresholds: efficacy 90, mutator coverage 55.

## TODO

- [ ] `internal/tlsacme`: test issuance and renewal against a local ACME server (Pebble) in CI. Today it has no tests.
- [ ] `internal/enclave` and `cmd/*`: test the vsock paths with a loopback vsock in CI or a fake listener.
- [ ] Raise `pkg/attestation` coverage to 95% (covers the 23 mutants not reached today).
- [ ] A real attestation document captured from the first deployment, committed as a fixture, verified in `TestVerify`.
- [ ] Web app tests (Vitest + Testing Library, axe per component) when it talks to the backend.
