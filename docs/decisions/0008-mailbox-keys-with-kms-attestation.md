# 0008 Mailbox data keys from KMS with attestation

Status: Accepted. Implemented and tested with a fake KMS and emulators; to verify against KMS once the enclave runs.
Date: 2026-11-09

## Context

Stored mail must be unreadable to the operator, the parent instance and anyone with AWS credentials, while surviving enclave restarts and upgrades. The enclave has no disk and no network of its own.

## Options

1. **KMS with the Recipient parameter**: KMS checks the enclave's attestation document against the key policy (PCR0) and returns the data key encrypted to an RSA key generated inside the enclave.
2. **Enclave-to-enclave key handoff**: a master secret lives only in enclave memory and is passed to the next version. Loses everything if all enclaves stop.
3. **A key derived inside the enclave and sealed nowhere**: lost on every restart.

## Decision

Option 1:

- One AES-256 data key per mailbox (`GenerateDataKey`, encryption context `{purpose: mailbox, mailbox: <id>}`), wrapped by `alias/permaily-mail`; unwrapped once per enclave lifetime and cached in memory.
- Key policy: administration for the account (no data-key actions), `GenerateDataKey` and `Decrypt` for the instance role only with `kms:RecipientAttestation:ImageSha384` in `var.enclave_pcr0_allowlist`.
- The KMS key ARN is compiled into the image (`deploy/enclave.json`) and every response's `KeyId` is checked.
- The CMS EnvelopedData that KMS returns is parsed with a minimal BER reader in `internal/keys` (Go's `encoding/asn1` only accepts DER), fuzzed in CI.
- Data is sealed with AES-256-GCM plus a key commitment and an AAD bound to purpose, mailbox and message id.

## Consequences

- Every release changes PCR0, so its PCR0 is added to the allowlist before it is deployed, and the previous one is removed afterwards. Both must coexist during an upgrade.
- Administrators can still rewrite the key policy. Since ADR 0010 the enclave publishes the live policy and `permaily-verify` checks it; what remains is in `docs/threat-model.md`.
- KMS costs one request per enclave start per mailbox plus one per new mailbox.

## To verify

- First deployment: the enclave obtains a data key; a request without Recipient and a request from an image not in the allowlist are both denied. Checked on 2026-11-10 with release v0.2.0: the enclave got its key, the account administrator (no Recipient) and the same image in debug mode were denied.
