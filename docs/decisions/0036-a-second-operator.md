# 0036 Operators other than the owner: IAM users with MFA that deploy and operate

Status: Accepted.
Date: 2026-11-17

## Context

Only the account's owner could deploy and operate. The owner decided on 2026-11-17 to add a second operator, with mandatory MFA and only the permissions to deploy and operate: no KMS key policies and no IAM. The account's organization forbids identity providers in it (no IAM Identity Center sign-in for this account, no GitHub OIDC), so an operator is an IAM user.

## Options

1. **A copy of the owner's access.** Simple, and gives a second person every power in the account, key deletion included.
2. **An IAM user in a group with a policy for deploying and operating, refused everything without MFA.** What `scripts/deploy.sh` and docs/operations.md use, and no more.
3. **That, plus `tofu apply` limited to release keys.** OpenTofu cannot apply part of a configuration safely with partial permissions: an apply refreshes everything and writes the whole state, so it needs reading every resource and writing the state, and `kms:CreateKey` cannot be limited to the policy `kmspolicy.Sealed` accepts. Not scoped, so not included.

## Decision

2, in `deploy/tofu/operators.tf`, for every name in `operators`.

- **MFA first.** AWS's documented policy for users who manage their own MFA, with one change: without MFA, only setting their password, and adding an MFA device only while the owner holds the operator's enrollment open (`operators_enrolling`, a tag on the user the operator cannot change), during which no access key can be made; with MFA, also more devices and their own access keys. AWS's own example lets anyone without MFA add a device, so whoever stole an operator's access key could make a second factor of their own and every permission with it; here a stolen key can enroll nothing, and the first device goes in only through the console password, during a window the owner opens and closes, with no key in existence. A lost device goes through the owner. The virtual device is named after the user, and only that name is allowed. Credentials are never made by OpenTofu: the owner gives a one-time console password (reset at first sign-in), the operator sets their MFA, and makes an access key only for the CLI once the enrollment is closed, used through `sts get-session-token` with MFA.
- **Labels, not names.** An operator is `permaily-operator-<label>` with a generic label (`2`), so the public repository does not say who operates.
- **Operate**: read the OpenTofu state for `tofu output` (it holds no credential of the operators; it does hold the GitHub dispatch token and the Amplify webhook's address); upload artifacts; SSM commands and sessions on the instance; read the instance's and the drift check's logs; see keys, policies, alarms, rules, metrics, SES's state, CloudTrail's events and the anchors' versions; run the drift check; look up and remove an address from SES's suppression list.
- **Guard**: an explicit deny of every KMS change (create, policy, grants, aliases, enable, disable, delete), of IAM beyond the operator's own password, MFA device and access keys (not their tags, groups or policies), of writes to the state bucket, and of DynamoDB.
- **Managed policies.** The three are customer-managed policies attached to the group, not inline: IAM allows a group 5,120 characters of inline policy in all (whitespace not counted), and they take 6,228; a managed policy may take 6,144 by itself (the largest, `operate`, takes 2,920). The membership and the `operate` attachment wait for the MFA deny and the guard, so an apply that fails part-way never leaves a member with permissions and without them.
- **Tested**: the three policies are JSON templates (`deploy/tofu/operators/`) that `scripts/operator-policies.test.ts` evaluates as IAM would, for a long-term key without MFA, an open enrollment and an MFA session; the guard's and `require-mfa`'s denies are tested next to another attachment that allows everything, since the guard holds whatever else is ever attached to the group or the user, and weakening any of them fails the test. It also checks IAM's size limits.
- **SSM stays** (the owner's choice, 2026-11-17): deploying needs a root shell on the instance, so an operator has one, as the owner does.
- **Not the operator's**: step 1 of a release (its key) and deleting an old release's key, which need `tofu apply`; the owner does them.

## Consequences

- An operator has a shell on the untrusted instance, so its role: the table's readable account data (contact addresses, names, dates), deleting stored data, sending mail as the service. That is what deploying needs and what the owner has; the enclave's guarantees do not depend on who holds it, but the privacy promise that "we" see account data now includes the operators.
- Removing a name deletes the user with its password, MFA devices and keys. The admin token and secrets an operator was given outside AWS are rotated by hand.
- The repository names operators by label only; who they are is not in it. Their user names, tags and the enrollment window are in the state and in IAM.
- Onboarding takes two applies by the owner: one that opens the enrollment, one that closes it.
- A new script that calls AWS needs its actions in `operators_operate`.
