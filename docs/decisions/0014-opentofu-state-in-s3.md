# 0014 OpenTofu state in S3, encrypted by OpenTofu

Status: Proposed.
Date: 2026-11-10

## Context

The state of `deploy/tofu` is a file on one laptop (`terraform.tfstate`, not committed). If it is lost, every resource has to be imported again; a second operator cannot apply; two applies at once would corrupt it; and it holds one secret in clear, the URL of the site's rebuild webhook. Two values that describe production also live outside the repository, in an untracked `terraform.tfvars`: the PCR0s allowed in the KMS key and the ACME account in the CAA records.

The state is not part of what users verify: the enclave image and the checks of `permaily-verify` are. It is an operations problem.

## Options

1. **S3 backend in the same account**, with versioning and the backend's own lock file (`use_lockfile`, no DynamoDB table), plus OpenTofu's state and plan encryption with a KMS key, so the bucket only ever holds ciphertext.
2. **A hosted backend** (HCP Terraform, Spacelift, env0, Scalr). Locking, history and a UI for free, but a third party holds the state and credentials for the account.
3. **The state file in the repository, encrypted** (OpenTofu encryption or sops). No locking, merge conflicts on every apply, and a public repository full of ciphertext.
4. **Stay local**, with a backup.

## Proposal

Option 1.

- Bucket `permaily-tofu-state-<account>` in `us-east-2`: versioning on, public access blocked, TLS only, old versions kept 90 days.
- A KMS key `alias/permaily-tofu-state` that operators may use, separate from the mail key (which nobody outside the enclave may use).
- In `deploy/tofu`: `backend "s3"` with `use_lockfile = true`, and an `encryption` block with the `aws_kms` key provider and `aes_gcm` for state and plan, enforced.
- The bucket and the key come from a small separate root, `deploy/tofu/bootstrap`, applied once; its own state has no secrets and is committed.
- Move `enclave_pcr0_allowlist` and `acme_account_uris` from the untracked `terraform.tfvars` into a committed `production.auto.tfvars`, updated in the pull request that follows each release. Both values are public (the verifier reads them from the live service) and the repository should say what production is.

## Plan

1. Write and apply `deploy/tofu/bootstrap` (bucket, key, policies).
2. Add the backend and encryption blocks; `tofu init -migrate-state`; check `tofu plan` shows no changes; delete the local state files.
3. Commit `production.auto.tfvars`; remove `terraform.tfvars`.
4. Give the second operator an IAM user with the same policy and check they can plan.
5. Later: a scheduled job with a read-only role (GitHub OIDC) that runs `tofu plan` and reports drift.

About half a day.

## Consequences

- Any operator can apply, one at a time, and a lost laptop loses nothing.
- Reading the state needs both S3 and KMS permissions; the webhook URL is no longer in clear anywhere.
- The state bucket becomes something to protect: deleting it is recoverable only through its versions.
