# 0013 Authorize releases without editing the KMS key policy

Status: Accepted. Option 1 implemented in v0.6.0, option 3 in v0.7.0, and both together since v0.8.0: the key of each release cannot be changed from the account, and the enclave still watches it (2026-11-10).
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

## Decision

Option 1 now, because it is small and makes changes visible. Option 3 as the real fix, after a spike. Option 2 in addition, if the organization's owner agrees: it costs one policy.

## Option 1 as built

- The enclave reads the policy and the grants once a minute (`keys.PolicyReader.Run`) and keeps every different state it has read since it started, with the first and last time it saw each, and every period in which it could not read (`history` and `gaps` in the `kms` section of `/status`; both bounded to 32 entries).
- `permaily-verify` applies its checks to every state in that history, not only to the current one: a policy that let another image in, skipped the image check or had a grant fails verification even after it was put back, and the error says from when to when. A truncated history fails. Gaps are printed as warnings.
- The workflow `verify-production` runs the latest release's verifier against production twice an hour (started by an EventBridge rule, because GitHub's own schedule fires only a few times a day; docs/operations.md) and keeps the output in its public run summary; a failed run is an incident. `scripts/verify-production.sh` is the same check by hand.
- An upgrade no longer allows two images at once: the policy goes from the old PCR0 to the new one in one change, then the new image is started. The old enclave keeps the keys it already has in memory, and the new enclave starts with a history that only ever names itself.

What it still does not catch: a change made while the enclave cannot reach KMS (it shows as a gap, not as a change), a change followed by a restart of the enclave (the history starts again; the start time is printed and the scheduled runs show it moved), and anything before the enclave started.

## Plan for option 3

1. **Spike**, about three days (done, see the results below):
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

The spike's code was `spikes/release-handoff/`. Its verifier is now [`pkg/provenance`](../../pkg/provenance/provenance.go) and its comparison with `sigstore-go` is [`tests/sigstore-differential`](../../tests/sigstore-differential/reference.go).

- An enclave can verify offline that a `pcrs.json` comes from this repository's release workflow on `main`, with the bundle `release.yml` already publishes. Checked against the real release v0.5.0 and 23 forged variants.
- `sigstore-go` is too heavy for the image (556 packages, gRPC, Go 1.27). A verifier of our own with the standard library is 323 lines, adds no package to the enclave, and agrees with `sigstore-go` on every case.
- A KMS key policy can be made unchangeable: with the lockout check bypassed at creation, an account administrator was refused `PutKeyPolicy`, `CreateGrant` and every use of the key.
- Open then: what AWS Support can do with such a key (answered by AWS's documentation, see "Watching the key"), the move of the public log to Rekor v2, how the trust root is updated, and the handoff protocol itself (built, see below).

## Option 3 as built

**Keys.** Every enclave image has its own KMS key, found by the alias `alias/permaily-release-<PCR0>`. Its policy (`deploy/tofu/releases.tf`) lets only that image decrypt, lets anyone in the account create a data key under it, lets the account read it and schedule its deletion, and gives nobody `kms:PutKeyPolicy`, `kms:CreateGrant` or anything else: KMS then refuses to change the policy, for the account's administrators and its root user too. `kmspolicy.Sealed` is that rule as code. The enclave applies it before it uses a key, and `permaily-verify` applies it to what the enclave reports.

**Watching the key.** AWS documents one way around: a key whose policy lets nobody manage it is "unmanageable", and the account's owner "must contact AWS Support to regain access" to it. So the policy cannot be changed from the account, but it can by AWS for the account's owner. The enclave therefore keeps reading the policy and the grants of its key once a minute and reports every state it has seen since it started, and the periods in which it could not read (option 1, applied to the new keys). The verifier requires a reading made just now and applies `kmspolicy.Sealed` to every state; the scheduled public verification does that around the clock. A change would need a support case with AWS, would show in every check from then on, and can only be hidden by restarting the enclave, which moves the start time that every check prints.

**Root secret.** One random 32-byte secret per deployment, created inside an enclave. Every data key (mailbox, TLS identity) is wrapped under it (`keys.Root`), not by KMS. Outside enclaves it exists only in records `root-<PCR0>`, one per image: the secret sealed with a data key that KMS wrapped under that image's key, plus a Nitro attestation document from the enclave that wrote the record, whose user data binds the image, the key, the wrapped data key and the sealed secret.

**Reading a record.** An image accepts its record only if the key it names is sealed to the image, and the attestation document says the writer ran the same image (it created the secret) or the image of a public release, proved by that release's `pcrs.json` and Sigstore bundle stored in the record (`pkg/provenance`: certificate issued by Fulcio to the release workflow of this repository, identified also by GitHub's numeric ids, on `refs/heads/main`, on a GitHub-hosted runner; signature; entry and inclusion proof in the public Rekor log). A record the operator writes fails this: KMS lets them create data keys, but only an enclave gets an attestation document, and a private image's document names a private image.

**Handing over.** `POST /v1/admin/handoff` (admin token) with the `pcrs.json` and bundle of the next release and of the running one. The enclave checks that both are public releases, that the running one is itself, that the next one was logged after it (never back to an older release, which could have a known flaw), and that the next image's key is sealed to it; then it asks KMS for a data key under that key, delivered encrypted to its own attested key, and writes the record. Neither the root secret nor the data key leaves the enclave in the clear. The token only limits who can make the enclave work: the decision does not depend on the caller.

**Starting.** An image without a record waits (`waiting for the running release to hand over the root secret`) when the deployment already has a root secret, and creates one only in an empty deployment. So starting the new image before the handoff is harmless. A store that fails is never taken for an empty one.

**Moving from the old key.** v0.7.0, the first release with a root secret, still opened the data keys that `alias/permaily-mail` had wrapped and rewrapped them under the root secret when it loaded them. The old key was then retired with a policy that lets nobody use it or change it from the account, and v0.8.0 dropped the code that read it; the key itself is scheduled for deletion on 2026-12-10. Copies of the old wrapped data keys are useless once it is gone.

**Staging.** `.#eif-staging` is a deployment of its own: its own table and bucket, and a release policy that accepts only images built by `.github/workflows/staging-image.yml` on a `staging/` branch. A production image refuses to hand over to a staging image and the reverse, so tests never touch production's root secret.

What changed from the proposal: KMS wraps a transfer key instead of the root secret itself, so the secret is never sent to KMS; keys are found by an alias instead of being passed in; the downgrade rule; and the staging deployment.

## What it rests on

- **Sigstore's public instance and GitHub Actions.** Whoever can make Fulcio issue a certificate for this repository's release workflow, or make GitHub run that workflow on other code, can get an image accepted. The trust root (Fulcio's certificates, Rekor's key) is compiled into the image; a rotation needs a release signed under the old root first. GitHub's attestations are in Rekor v1; a move to Rekor v2 needs a second code path in `pkg/provenance` before the release workflow changes.
- **`main` is public.** An image is accepted because it was built from a commit on `main`. Anyone can read that commit; nothing here judges it.
- **Old releases.** An older image can still open its own record for as long as its key exists, so a flaw in an old release stays exploitable until its key is deleted (`release_keys` in OpenTofu; seven days after scheduling, and with no way back from the moment it is scheduled, because the policy lets nobody enable a key again). The same record is what makes a rollback possible, so delete the key once the new release has started from its own record and no rollback is planned.
- **AWS.** KMS enforces the policy and the attestation check, and AWS Support can restore the account owner's access to a key that nobody can manage ([default key policy](https://docs.aws.amazon.com/kms/latest/developerguide/key-policy-default.html), "Reduces the risk of the KMS key becoming unmanageable"). That is why the enclave watches its key: the owners cannot change who may use it on their own or quietly, but with AWS they could, and it would show.
- **TLS to KMS.** A data key that KMS returns is believed because the connection to KMS is authenticated with the public WebPKI, from inside the enclave. This was already so for the data keys of earlier releases.

## Risks

- A bug in the handoff can lose the root secret. Mail in Permaily is a copy of what the user's mailbox still holds, and the old image and its key stay available until the new one has served mail.
- Deleting the key of the running image, or every root record, loses everything stored. Neither can be undone after the deletion window.
- The enclave verifies Sigstore bundles with its own code (`pkg/provenance`), compared with `sigstore-go` in CI, and carries a pinned trust root that only a release can update.
- `kms:GenerateDataKey` on each key is open to the account, which lets the operator write records; the attestation document in a record is what makes theirs worthless.
