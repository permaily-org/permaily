# 0010 Publish the KMS key policy over the attested TLS key

Status: Accepted. Verified on the production instance with a staging image (2026-11-10).
Date: 2026-11-10

## Context

Attestation proves which image runs. It does not prove which images can decrypt stored mail: that is decided by the policy of the KMS key (ADR 0008), which the operator writes and nobody outside could read. The operator could allow a private image, or grant the account plain `kms:Decrypt`, without `permaily-verify` noticing.

## Options

1. **The enclave publishes the hash of the policy**, and the verifier compares it with a file in the repository. The file would hold the account id, the role and every allowed PCR0, so each release would need the policy committed before the image exists, and an upgrade (two PCR0s for a while) would never match.
2. **The enclave publishes the policy itself and the verifier analyzes it.** The verifier needs nothing from the repository except its own PCR0.
3. **A separate attestation document for the status**, with the policy hash in `user_data`. More to verify, and `user_data` is already used for the TLS key.

## Decision

Option 2.

- The enclave reads the policy (`kms:GetKeyPolicy`) and the grants (`kms:ListGrants`) of its pinned key, over TLS that ends inside the enclave, and serves them on `GET /status` on every host. A reading is cached for 30 seconds; a failure returns 503, never an old reading.
- `/status` has no signature. `attestation.FetchStatus` accepts it only when the TLS certificate of that connection has the key bound in a verified attestation document, so the body comes from the attested image.
- `pkg/kmspolicy` analyzes the policy conservatively: every statement that allows anything outside a list of administrative actions must require `kms:RecipientAttestation:ImageSha384` (or `PCR0`) with `StringEquals` or `StringEqualsIgnoreCase`; anything it does not understand is a problem. Principals are ignored, because IAM can only grant what the key policy allows to the account.
- `permaily-verify` fails when the policy has a problem, when the key has a grant, when the reading is older than 5 minutes, or when an image other than the verified one is allowed. `-kms-also-allow` accepts named images, for the window of an upgrade.

## Consequences

- Anyone can check which images can read stored mail, at the time of the check.
- The check is a snapshot: administrators can still replace the policy between checks (`kms:PutKeyPolicy`); the verifier says so. Closing that needs a policy that denies `PutKeyPolicy`, which also blocks adding the next release's PCR0, so it waits for a way to authorize releases without editing the policy.
- Every action in the key's administration statement must be in the safe list of `pkg/kmspolicy`, or verification fails. Adding one is a change to both.
- During an upgrade two PCR0s are allowed and a plain `permaily-verify` fails until the old one is removed; that is intended (`docs/operations.md`).
- The host role gets `kms:GetKeyPolicy` and `kms:ListGrants`. The policy is not secret.

## To verify

- Done on 2026-11-10 against the production instance: a second allowed image and a statement granting `kms:Decrypt` without an image both made the verifier fail; creating a grant was denied by the policy.
