# Data

What Permaily stores, where, how it is protected, for how long, and who processes it. This is the basis for the privacy policy, the DPA and the subprocessor list.

## F0 (today)

No user data. The enclave keeps only its TLS key and ACME account key in memory; both disappear when it stops. The parent instance logs connection metadata (timestamps, destination host of egress) but never content.

## F1+ (planned)

| Data | Where | Protection | Retention | Deletion |
|---|---|---|---|---|
| Message bodies and attachments | S3 (`raw/<user>/<message>`) | AES-256-GCM with a per-user data key; AAD binds user, message and version | User setting (default to decide) | Destroying the user's data key (crypto-shredding), plus S3 lifecycle |
| Message metadata (sender, subject, date, labels) | DynamoDB | Encrypted with the same per-user key | Same as bodies | Same, plus DynamoDB TTL |
| Per-user data keys | DynamoDB, wrapped | KMS key usable only by enclaves with an allowlisted PCR0 (`kms:RecipientAttestation:ImageSha384`) | Account lifetime | Deleted with the account |
| Grants and audit log | DynamoDB | Encrypted per user; audit log hash-chained | Account lifetime (audit: to decide) | With the account |
| Account (passkeys, Google ID) | DynamoDB | Encrypted per user | Account lifetime | With the account |

The operator can see sizes, timing, and which grant receives which message (needed to route deliveries). Content never leaves the enclave unencrypted except to the app or agent the user authorized, over TLS from inside the enclave.

## Subprocessors (planned)

| Subprocessor | Purpose | Data |
|---|---|---|
| Amazon Web Services | Compute (Nitro Enclaves), storage (S3, DynamoDB), keys (KMS), outgoing mail (SES) | Encrypted content and metadata |
| Cloudflare | DNS only | None (no proxying) |
| Let's Encrypt | TLS certificates | Hostnames |
| LLM provider (F3, to decide) | Classification | Message content needed for the user's rules, under the provider's no-training terms |

## TODO

- [ ] Decide default retention and audit log retention.
- [ ] Choose the F3 LLM provider and document its data terms before enabling classification.
