# Data

What Permaily stores, where, how it is protected, for how long, and who processes it. This is the basis for the privacy policy, the DPA and the subprocessor list.

## F0 (releases up to v0.1.1)

No user data. The enclave keeps only its TLS key and ACME account key in memory; both disappear when it stops. The parent instance logs connection metadata (timestamps, destination host of egress) but never content.

## F1 (deployed since v0.2.0, 2026-11-10)

One test mailbox, compiled into the image (`deploy/enclave.json`), read with a bearer token whose SHA-256 is in the image.

| Data | Where | Protection | Retention | Deletion |
|---|---|---|---|---|
| Raw message (with attachments) | S3 `permaily-mail-<account>`, key `raw/<mailbox>/<message id>`, tagged `retention-days=<n>` | AES-256-GCM with the mailbox data key and key commitment (AAD: purpose, mailbox, message id); SSE-S3 underneath | 7, 30, 90 (default), 180 or 365 days, set per mailbox; applies to mail received after the change | Lifecycle rule for its tag (period + 1 day), or `DELETE /v1/mailbox/messages/{id}` |
| Message metadata (envelope, From/To/Cc, subject, date, List-Id, snippet, attachment names and sizes, DKIM results) | DynamoDB `permaily-mail`, `pk=mailbox#<id>`, `sk=msg#<message id>` | Sealed like the raw message | Same as the raw message (DynamoDB TTL on `expires_at`, also filtered on read) | Same |
| Mailbox state (Gmail forwarding confirmation codes) | DynamoDB, `sk=mailbox` | Sealed | Mailbox lifetime (last 10 codes) | With the mailbox |
| Mailbox data key | DynamoDB, `sk=mailbox`, wrapped | KMS key `alias/permaily-mail`, released only to an attested enclave | Mailbox lifetime | Destroying it makes every message unreadable (crypto-shredding) |

Plaintext outside the enclave: ids, sizes, timestamps, retention period. Message ids are time-ordered.

## F2+ (planned)

| Data | Where | Protection | Retention | Deletion |
|---|---|---|---|---|
| Grants and audit log | DynamoDB | Encrypted per user; audit log hash-chained | Account lifetime (audit: to decide) | With the account |
| Account (passkeys, Google ID) | DynamoDB | Encrypted per user | Account lifetime | With the account |

## Subprocessors (planned)

| Subprocessor | Purpose | Data |
|---|---|---|
| Amazon Web Services | Compute (Nitro Enclaves), storage (S3, DynamoDB), keys (KMS), outgoing mail (SES) | Encrypted content and metadata |
| Cloudflare | DNS for permaily.com; DNS-over-HTTPS resolver for DKIM checks (`cloudflare-dns.com`) | Hostnames; the domains and DKIM selectors of incoming mail senders |
| Let's Encrypt | TLS certificates | Hostnames |
| LLM provider (F3, to decide) | Classification | Message content needed for the user's rules, under the provider's no-training terms |

## TODO

- [x] Default retention: 90 days, configurable to 7, 30, 180 or 365.
- [ ] Decide audit log retention (F2).
- [ ] Choose the F3 LLM provider and document its data terms before enabling classification.
