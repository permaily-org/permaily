# Plan

What is built, what comes next and how the planned parts are designed. Current architecture: [architecture.md](architecture.md). Decisions will be recorded as ADRs in `docs/decisions/`.

## Phases

- **F0 (current)**: verifiable hello end to end.
- **F1 inbox**: MX inside the enclave, encrypted storage, KMS bound to attestation.
- **F2**: users (passkeys, Google sign-in), grants, API for apps, remote MCP, audit log.
- **F3**: LLM classification (model and prompts embedded in the image).
- **F4**: per-agent inboxes, a private inbox, scaling (NLB TCP passthrough, ASG), CT and Rekor monitoring in the verifier.

## F1: inbox design

1. Each user gets `u-<128-bit random token>@inbox.permaily.com` and creates a Gmail rule forwarding all mail there. Gmail's forwarding confirmation code is shown in the user's dashboard; links are never followed automatically.
2. `MX inbox.permaily.com` points at the same IP; port 25 is forwarded to the enclave, which runs an SMTP server (`emersion/go-smtp`) with STARTTLS on the same certificate. MTA-STS (served from the enclave at `mta-sts.inbox.permaily.com`) and TLS-RPT make senders require TLS.
3. ARC/DKIM are checked inside the enclave (`emersion/go-msgauth`) with DNS over HTTPS, so the parent cannot spoof DNS answers.
4. In memory: parse (`emersion/go-message`), extract metadata, classify, encrypt, store, evaluate grants, deliver.
5. Storage outside the enclave: bodies in S3, encrypted metadata in DynamoDB, AES-256-GCM with AAD `user|message|version`. Per-user data keys are wrapped by a KMS key that only enclaves with an allowlisted PCR0 can use (`kms:RecipientAttestation:ImageSha384`). Deleting a user destroys their key (crypto-shredding).
6. KMS key policy: no `kms:*` for the account root, `Encrypt`/`GenerateDataKey` only for the enclave role, a deny on `PutKeyPolicy` except for an MFA-protected release role. The enclave publishes the hash of the live key policy in an attested `/status`, and the verifier compares it with `deploy/kms-policy.json`.
7. Grants are evaluated inside the enclave at ingest and again on every read, so revocation is immediate. The audit log is append-only and hash-chained, encrypted per user.
