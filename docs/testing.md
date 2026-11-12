# Testing

## What counts as a test

- A test asserts an observable outcome and fails when the code breaks. Break the code on purpose once and watch the test fail.
- No snapshot-only tests for logic, and no committed `.skip` or `.only`.
- Mock only true external boundaries (NSM, ACME CA, Sigstore, network, clock). Prefer fakes (`internal/nsm/fakensm`, `pkg/attestation/attestationtest`, `pkg/provenance/provenancetest`) over call-recording mocks.
- Every bug fix starts with a failing test.
- A message the service sends is added to `sampleMails` (`internal/services/accounts/mail_samples_test.go`) in every variant, and its golden text and fixture are written with `-update`: the checks of the HTML run on every fixture.
- Security checks need a negative test that tries to break them: every rejection path in `pkg/attestation` has one (wrong nonce, wrong user data, MITM, debug PCRs, different image, stale, wrong root, tampered, malformed).
- What is stored outside the enclave is tested with the storage in the hands of someone else: the test writes to it by hand (`store.Memory.SetEntry`, `SetRecord`, `DeleteRecord`) and nothing it writes may let anyone in (`TestStorageWrittenByHand`).
- A property the product promises gets a test named for what it shows, so the name is the promise: `TestABlockedAccountSignsInByNoWay`, `TestNothingSaysAnAddressIsBlockedBeforeProof`, `TestTheOperatorsRoutesCannotChangeAnAddressOrAWayIn`, `TestAnAddressAttemptSignsNobodyIn`, `TestTwoEnclavesChangingAddressesLeaveOneAccountPerAddress`, `TestNoticesHoldNoCodeLinkOrToken`, `TestANoticeThatFailsDoesNotFailTheAction`, `TestASessionThatIsNotFreshlyConfirmedChangesNoWayIn`, `TestAnAttemptMadeToConfirmSignsNobodyInAndChangesNoAddress`, `TestAnAdminWhoseAccountIsNotPasskeyOnlyDoesNothing`, `TestNothingInTheTableMakesAnAdmin`, `TestAnAdminsRoutesCannotChangeAnAddressOrAWayIn`, `TestSomeoneWhoIsNotAnAdminLearnsNothingFromTheAdminRoutes`, `TestOnlyTheSuperAdminMakesAdmins`, `TestASignInPutBackAfterItWasUsedEndsNothing`, `TestAnEnclaveEndsNoSignInMadeBeforeItStarted`, `TestNoSessionMadeBeforeASignOutSurvivesIt`, `TestNoAccountIsMadeWithoutAgreeingToTheTerms`, `TestAnAccountThatAgreedToEarlierTermsChangesNothingUntilItAgrees`, `TestAnAdminWhoMayNotActIsToldBeforeConfirming`, `TestEveryMessageFetchesNothing`, `TestEveryLinkIsOneTheTextHas`, `TestTheHTMLSaysWhatTheTextSays`.
- Two enclaves of one deployment run at once (ADR 0017). A flow that starts and ends in two requests has a test that starts it through one enclave and ends it through the other: two `accounts.Service` over the same storage (`w.enclave()` in `internal/services/accounts/enclaves_test.go`, `enclaveAt` for one whose clock is ahead or behind). A race between two enclaves is put in order, never left to timing: the helpers there run what the other enclave does after one read a record or an entry and before it writes (`between`, `watched`). Run them with `go test -race -count=20`.
- What two enclaves share is tested with two instances over one storage, started together, one after the other, and one changing while the other serves; a test of what happens at once makes the interleaving happen (a store that holds both reads until both have read, or that runs the other enclave just before a write), never with sleeps: `TestTwoEnclavesStartingTogetherOrderOneCertificate`, `TestTheCAsCheckReachesTheEnclaveThatDidNotOrder`, `TestTheMailboxMadeByTwoEnclavesAtOnceIsOne`, `TestTwoEnclavesOfOneImageStartingTogetherHaveOneRootSecret`.
- A change that needs a fresh proof of who the person is (ADR 0022) is tested with the clock the test controls, never with real time. A test of something else starts from a session whose proof is fresh (`world.fresh` in `internal/services/accounts`); the proof itself is tested through the storage (`confirm_test.go`).

## Layers

| Layer | Tool | Where | Runs |
|---|---|---|---|
| Unit and local end to end (Go) | `go test`, `httptest` | `*_test.go` | `make check`, CI |
| Fuzzing | Go native fuzzing | `pkg/attestation`, `pkg/kmspolicy`, `pkg/provenance` (bundles of both Rekor logs, timestamps, and trust root updates), `internal/keys` (CMS), `internal/inbox` (mail parser), `internal/webauthn` (what a browser answers when a passkey is made and used), `internal/idp` (ID tokens, key sets and every answer of a provider) | `make fuzz`, CI |
| Differential | `pkg/provenance` against sigstore-go, on the published release v0.5.0, on a signature recorded in the public Rekor v2 log, and on bundles from the test authority shaped like both logs; and its TUF checks against go-tuf, on Sigstore's metadata from TUF root 12 to 16 and on the test authority's | `tests/sigstore-differential` (its own Go module, Go 1.27) | CI `sigstore-differential`; locally `cd tests/sigstore-differential && go test ./...` |
| Differential | `internal/webauthn` against go-webauthn, on creations and uses from two software authenticators for each algorithm, valid and broken in many ways; every case where the two differ on purpose is named with its reason | `tests/webauthn-differential` (its own Go module) | CI `webauthn-differential`; locally `cd tests/webauthn-differential && go test ./...` |
| Differential | `internal/idp`'s check of an ID token against go-oidc, on right and broken tokens of Google and Microsoft; it never accepts what the library refuses, and every case it refuses and the library accepts is named with its reason | `tests/idp-differential` (its own Go module) | CI `idp-differential`; locally `cd tests/idp-differential && go test ./...` |
| Captured answers | `internal/webauthn` on 96 public captures of what real authenticators and browsers answered (YubiKeys and other security keys, Apple, Android, Windows Hello with and without a TPM, iCloud Keychain, Google Password Manager, 1Password; the vectors of the WebAuthn specification), each with its source and licence; one that is refused must be refused for the reason its file states | `internal/webauthn/testdata/vectors` (`TestCapturedAnswers`, `TestCapturedAnswersCoverTheKinds`); the same captures against go-webauthn in `tests/webauthn-differential` | `make check`, CI |
| Commits against S3 | a real bucket with Object Lock: conditional writes, locks, delete markers, deletion refused; and what strict records rest on (ADR 0026, pull request 0; its subtest `strict records`, run first): a fork made through a delete marker, which `History` reports and `Head` reads by the smaller digest, a commit appended above it, the lock headers read back by version, a replayed copy without a lock and one with a governance lock of a few seconds (no fork, nothing unlocked, both deletable, the original not), two commits appended at once (one taken). S3Mock shows listing, versions and one conditional write at a time, but enforces no lock and took two conditional writes of one key at once. What S3 did is in docs/acceptance.md | `internal/store` (`TestS3CommitsAgainstS3`) | by hand, against the staging anchors bucket only, with `PERMAILY_TEST_ANCHOR_BUCKET=permaily-anchors-staging-<account>`, `AWS_REGION=us-east-2` and the `permaily` profile's credentials in the environment (`eval "$(aws configure export-credentials --profile permaily --format env)"`, then pass `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` into the Go container; never print them); each run leaves six objects locked for a day under `commits/` (records named `store-test-fork-`, `store-test-replay-` and `store-test-race-` with the time of the run) and four locked for two minutes, which the bucket's lifecycle removes; skipped otherwise |
| Storage contract | DynamoDB Local and S3Mock: the contract of every store (`TestAWS`), and ADR 0026's descending scans and a message's parts on both stores (`TestScansAndPartsInMemory`, `TestScansAndPartsOnAWS`: S5, S12 to S16), DynamoDB's page cut at 1 MB (`TestS11APageCutAtOneMegabyte`, DynamoDB Local only) | `internal/store/store_test.go`, `parts_test.go` | CI services; locally with `PERMAILY_TEST_DYNAMODB` and `PERMAILY_TEST_S3` |
| The store's cases of ADR 0026 (S1 to S17) | Each case is a test whose name or subtest starts with its number: strict records over a fake bucket like S3's and in memory (`strict_test.go`: S1, S3, S4, S6 to S10), records that are not strict read with the requests to S3 recorded on `develop` before pull request 0 (`notstrict_test.go`: S2), scans and parts (`parts_test.go`: S5, S11 to S17). `Memory.Interrupt` stops or holds any request of the in-memory store, for the tests of later pull requests | `internal/store` | `make check`, CI; `go test -race -count=20 ./internal/store` after a change to them |
| Mailboxes' seam contract | The same cases (C1 to C16 of ADR 0026) against `mailboxestest.Fake` and, from ADR 0026's pull request 1, against `*mailboxes.Service` over `store.Memory`, so the fake the other pull requests test against fails where the real service would | `internal/services/mailboxes/mailboxestest` (`Contract`, `TestTheFakeKeepsTheContract`) | `make check`, CI |
| ACME issuance and renewal | Pebble 2.6.0 (test CA that accepts every challenge) | `internal/tlsacme` (`TestObtainAgainstACME`, and two enclaves on one storage: `TestTwoEnclavesAgainstACME`) | CI service; locally with `PERMAILY_TEST_ACME` |
| The CA's TLS-ALPN-01 check landing on the enclave that did not order | Pebble 2.6.0 that validates (no `PEBBLE_VA_ALWAYS_VALID`, `-dnsserver` pointing at `pebble-challtestsrv -defaultIPv4 127.0.0.1`), connecting to `127.0.0.1:5001`, where the test sends every connection to the other enclave | `internal/tlsacme` (`TestTheCAsCheckReachesTheOtherEnclaveAgainstACME`) | by hand, with `PERMAILY_TEST_ACME_VALIDATING=<its directory>` (and `PERMAILY_TEST_ACME_TLSALPN` if not on 5001); skipped otherwise |
| Whole service, dev mode | SMTP client and HTTPS: mail in and read back; an invitation, a sign-up by email, a session and signing out; the operator blocking an account, its sign-in refused, a change of address, the notices in the outbox and the counts on `/status` (`TestOperatorEndToEnd`); the admin panel: the super admin refused until "passkey only", making an admin, who confirms with a passkey and blocks an account, the log, the counts and the notices (`TestAdminPanelEndToEnd`); two services over one storage, a sign-up by mail started through one and ended through the other (`TestASignInStartedThroughOneEnclaveEndsThroughTheOther`) | `cmd/permaily-enclave/main_dev_test.go` | `go test -tags dev ./cmd/permaily-enclave`, CI |
| Mutation | gremlins | `pkg/attestation` | manual (`mutation` workflow) |
| Reproducibility | Nix | CI `eif` + `reproducible` jobs | every PR |
| Third-party programs in the image | `bun test` on synthetic images (a kernel or a ramdisk file that is not the one described, a file the inventory misses, one it names that the image lacks) and on `nix/aws-blobs.json`; `scripts/image-inventory.ts` on the built production and staging images | `scripts/image-inventory.test.ts`; CI `eif` | `make check`, CI `repo` and `eif`, every PR |
| Mail | Golden texts of every message (`TestEveryMessageReadsAsItsGoldenText`, written before the HTML existed); on every message's fixture, the rules of its HTML: nothing fetched, no link the text lacks, the text's words in order, nothing identifying in the markup, hostile values escaped, under 40 KB, both themes, the code on one line (`internal/domain/mail`); the fixtures and templates current (`TestThePreviewShowsWhatTheServiceSends`, `bun run check` in `packages/mail`); the template build deterministic and the tokens read from `packages/ui/colors.css` (`bun run test` there) | `internal/domain/mail`, `internal/services/accounts/mail_samples_test.go`, `packages/mail/tests` | `make check`, CI `go` and `repo` |
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
| `internal/tlsacme` | 91 (with Pebble) | 92.1 (2026-11-12); the test of two enclaves started together moves it by a few tenths, run to run |
| `internal/router` | 100 | 100.0 |
| `internal/httpapi` | 95 | 95.8 (2026-11-12) |
| `internal/webui` | 75 | 75.0 |
| `internal/hostproxy` | 73 | 73.7 |
| `internal/enclave` | 87 | 87.7 |
| `internal/envelope` | 97 | 97.0 (2026-11-12) |
| `internal/keys` | 89 (target 95) | 89.9 (2026-11-12) |
| `internal/inbox` | 86 | 86.3 (2026-11-12) |
| `internal/msgauth` | 86 | 86.0 |
| `internal/smtpd` | 92 | 92.5 |
| `internal/store` | 97 (with emulators) | 97.4 (2026-11-12, ADR 0026's pull request 0) |
| `internal/contracts` | 100 | 100.0 (2026-11-11) |
| `internal/domain` | 100 | 100.0 (2026-11-11) |
| `internal/domain/mail` | 100 | 100.0 (2026-11-12) |
| `internal/services/accounts` | 99 | 99.6 (2026-11-12) |
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
