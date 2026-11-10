# 0013 Authorize releases without editing the KMS key policy

Status: Proposed. Needs a spike before it is accepted.
Date: 2026-11-10

## Context

The KMS key policy names the images that may get the keys of stored mail and of the TLS identity. `permaily-verify` reads it (ADR 0010), but only at the time of the check: an administrator can replace the policy, let a private image or themselves in, copy the keys and restore it, and no verifier that checks before or after notices.

The policy must change today for a legitimate reason: every release has a new PCR0, added before the upgrade and removed after it. So it cannot simply be made unchangeable.

## Options

1. **Detect.** The enclave reads the policy every minute and reports in `/status` every different policy it has seen since it started, and the periods in which KMS could not be reached. A public job runs `permaily-verify` on a schedule and publishes each result. A change is then seen if it lasts longer than the interval, or leaves a hole in the enclave's history. It does not stop a change that is quick and made while the enclave's access to KMS is cut, and it needs someone watching.
2. **Two people.** The AWS organization denies `kms:PutKeyPolicy` and `kms:CreateGrant` on the key (a resource control policy in the management account), so one administrator of the project account cannot act alone. Real, but invisible from outside: a verifier cannot read the organization's policies.
3. **One unchangeable key per release, and the running enclave hands the keys to the next one.**
   - Each release gets its own KMS key whose policy names only that release's PCR0 and gives nobody `PutKeyPolicy`, `CreateGrant` or `ReplicateKey`. The verifier checks exactly that, and from then on what it read stays true.
   - KMS no longer wraps each mailbox key. It wraps one root secret per deployment; the enclave derives or wraps everything else with it.
   - To upgrade, the operator creates the key of the new release and asks the running enclave to hand off. The running enclave accepts the new image only with proof that it is a public release: the Sigstore bundle that `release.yml` already publishes for `pcrs.json`, verified inside the enclave (signature, certificate identity of the repository's release workflow, and inclusion in the public transparency log). It then encrypts the root secret to the new key and attests that record with the Nitro Secure Module.
   - The new enclave decrypts the record with its own attestation, and accepts it only with a valid attestation document from the release it was built to succeed. So a record planted by the operator is refused.
   - Result: an image can receive the keys only if a public log says the repository's release workflow built it from a commit on `main`. A private image cannot be let in by anybody, the two owners together included.
4. **Keys shared between enclaves in different accounts or clouds.** Removes the single account as a point of trust, at several times the complexity. Not for now.

## Proposal

Option 1 now, because it is small and makes changes visible. Option 3 as the real fix, after a spike. Option 2 in addition, if the organization's owner agrees: it costs one policy.

## Plan for option 3

1. **Spike** (`spikes/release-handoff/`), about three days:
   - Verify a GitHub artifact attestation with `sigstore-go` inside a reproducible enclave image: size, build reproducibility, and how the pinned trust root is updated.
   - Confirm with AWS documentation and a test key that a policy without `PutKeyPolicy` cannot be changed by the account, and what AWS Support can do with such a key.
   - Decide whether old keys are deleted after a handoff (30 days at the earliest) and what a rollback needs.
2. **Root secret**: one sealed record; mailbox keys and the TLS identity wrapped by it instead of by KMS. Migration of the existing records by the release that introduces it.
3. **Handoff**: an operator endpoint guarded by the admin token that takes the new release's PCR0, key ARN and bundle; the transfer record; the check on start.
4. **Infrastructure**: one KMS key per release in OpenTofu, created from the release's `pcrs.json`; the list of keys in the repository.
5. **Verifier**: require that the policy gives nobody a way to change who can use the key, and stop printing the note about administrators.
6. **Runbooks and tests**: upgrade, rollback (restart the old image, which its own key still accepts), lost instance, a refused handoff; negative tests for a forged bundle, a bundle for another repository, a planted record and a replayed record.

About two weeks of work after the spike.

## Spike results (2026-11-10)

[`spikes/release-handoff/`](../../spikes/release-handoff/readme.md):

- An enclave can verify offline that a `pcrs.json` comes from this repository's release workflow on `main`, with the bundle `release.yml` already publishes. Checked against the real release v0.5.0 and 23 forged variants.
- `sigstore-go` is too heavy for the image (556 packages, gRPC, Go 1.27). A verifier of our own with the standard library is 323 lines, adds no package to the enclave, and agrees with `sigstore-go` on every case.
- A KMS key policy can be made unchangeable: with the lockout check bypassed at creation, an account administrator was refused `PutKeyPolicy`, `CreateGrant` and every use of the key.
- Open: what AWS Support can do with such a key, the move of the public log to Rekor v2, how the trust root is updated, and the handoff protocol itself.

## Risks

- A bug in the handoff can lose the root secret. Mail in Permaily is a copy of what the user's mailbox still holds, and the old image and its key stay available until the new one has served mail.
- The enclave gains a dependency (Sigstore verification) and a pinned trust root that must be updated by a release.
- `kms:Encrypt` on the new key must be open to the instance role, which lets the operator write records; the attestation on the transfer record is what makes them worthless.
