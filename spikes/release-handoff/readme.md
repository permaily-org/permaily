# Spike: can an enclave tell a public release from a private image?

For [ADR 0013](../../docs/decisions/0013-authorize-releases-without-editing-the-key-policy.md), option 3: one KMS key per release whose policy nobody can change, and a running enclave that hands the keys only to an image it can prove is a public release. This code is not imported by the product and is never deployed.

```sh
cd spikes/release-handoff && go test ./...     # needs Go 1.27 (sigstore-go v1.4.0 requires it); no network
```

## Questions and answers (2026-11-10)

| Question | Answer |
|---|---|
| Can an enclave verify, offline, that a `pcrs.json` was built by this repository's release workflow from `main`? | Yes. `release.yml` already publishes a Sigstore bundle for it. `VerifyRelease` checks, with only the bundle, the file and a pinned trust root: the signature, a Fulcio certificate issued to `.github/workflows/release.yml@refs/heads/main` of this repository on a GitHub-hosted runner, and the entry in the public Rekor log. Tested with the real bundle of v0.5.0. |
| Does it refuse what an operator would try? | Yes: other measurements, another repository, another branch, another workflow, an edited statement or signature, an entry that is not in the public log. 14 cases, plus 9 on the Merkle proof and checkpoint. |
| What does `sigstore-go` cost inside the enclave? | Too much: 556 packages (the enclave links 347 today), with gRPC and OpenTelemetry among them, a 17.4 MB test binary, and Go 1.27 while the image pins Go 1.26. The build was reproducible (two builds, same hash). |
| Is there a smaller way? | Yes. `minimal/` does the same checks, plus the inclusion proof and the signed checkpoint, in 323 lines of standard library. It links no package the enclave does not already link, and agrees with `sigstore-go` on every case (`TestAgreesWithSigstoreGo`). |
| Can a KMS key policy be made unchangeable? | Yes. A key created with `--bypass-policy-lockout-safety-check` and a policy that gives nobody `kms:PutKeyPolicy` refused, for an account administrator: `PutKeyPolicy` (with and without the bypass flag), `CreateGrant`, `GenerateDataKey`, `Encrypt`, `UpdateKeyDescription`, `EnableKeyRotation`, `DisableKey`, `TagResource` and `CreateAlias`. `GetKeyPolicy`, `ListGrants` and `ScheduleKeyDeletion` worked, as the policy said. Without the flag KMS refuses to create such a key. Test key `8f48c97f-57be-4bfd-8d9a-d81cbc9cec79` in the production account, scheduled for deletion on 2026-11-17. |

## Not answered

- **What AWS can do with such a key.** AWS's documentation calls a key whose policy lets nobody manage it "unmanageable"; whether AWS Support will change its policy for the account owner is not documented where we looked. Ask AWS before relying on it: it is the remaining way around.
- **The account root user** was not tried (it belongs to the organization's owner). A key policy that names `arn:aws:iam::<account>:root` only for some actions should bind the root user to those actions too.
- **Rekor v2.** The bundle is in Rekor v1 (ECDSA key, signed entry timestamp). Sigstore's trust root already lists a v2 log (Ed25519, no signed entry timestamp; time comes from a timestamp authority). When GitHub moves, `minimal/` needs a second code path. `sigstore-go` hides that, at the cost above.
- **Updating the trust root.** Fulcio's and Rekor's keys are compiled into the image; a rotation needs a release, signed under the old root, before the old root stops being used.
- **The handoff itself** (the transfer record attested by the Nitro Secure Module, the check on start, rollback, deleting old keys) is not prototyped.

## Recommendation

Go ahead with option 3 using a verifier of our own like `minimal/`, fuzzed and kept honest by a differential test against `sigstore-go` that runs only in CI (so `sigstore-go` never enters the image). Before writing the handoff, ask AWS the Support question, and decide what a rollback needs.

## Files

- `release.go`: verification with `sigstore-go`, the reference.
- `minimal/`: the same with the standard library.
- `testdata/`: `pcrs.json` of v0.5.0, the two attestations GitHub holds for it (build provenance in the public log, and GitHub's own release attestation, which is not), and Sigstore's public trust root, all as published.
