# internal/AGENTS.md: enclave service and host proxy

Go packages behind `cmd/permaily-enclave` (trusted, runs in the enclave) and `cmd/permaily-host` (untrusted, runs on the parent EC2 instance). Architecture: [../docs/architecture.md](../docs/architecture.md). Trust boundaries: [../docs/threat-model.md](../docs/threat-model.md).

| Package | Role |
|---|---|
| `enclave` | vsock listener, egress HTTP client through the host's CONNECT proxy (the parent instance is vsock CID 3), and the log sink that sends log lines to the host |
| `tlsacme` | one multi-SAN certificate via ACME TLS-ALPN-01; the certificate key and the ACME account key are kept sealed under the root secret (`Sealed`) and reused across restarts |
| `nsm`, `nsm/fakensm` | real NSM attester; fake attester for dev mode and tests |
| `contracts` | types and error codes that cross the boundary (API, MCP, command line, web app); imports nothing of the service (F2) |
| `domain` | rules that need only their arguments: session lifetimes, what an address or an invitation looks like; imports only `contracts` (F2) |
| `services/accounts` | accounts and sign-in (ADR 0017): invitations, sign-in by email, sessions; knows nothing of HTTP (F2) |
| `httpapi` | `/attestation`, `/status` (the key that locks the root secret, the TLS identity), `/version`, mailbox API, `/v1/admin/handoff`, `/v1/admin/invitations`, and the web app's own API under `/api/v1/` (cookie, same origin) |
| `router` | dispatch by Host (app, api, mcp, inbox) and security headers |
| `webui` | embeds the SPA build (`dist/` is copied in at build time) |
| `hostproxy` | TCP to vsock forwarding, allowlisted CONNECT proxy and printing of the enclave's log lines (host side) |
| `buildinfo` | commit set via ldflags |
| `smtpd` | MX on port 25 (vsock), STARTTLS with the enclave certificate, refuses mail without TLS (F1) |
| `inbox` | mailbox: parse, Gmail confirmation codes, seal, store, serve (F1) |
| `msgauth` | DKIM verification with DNS-over-HTTPS (F1) |
| `keys` | keys for one purpose derived from the root secret (`Derive`); the root secret (`Root`): created in an enclave, stored one record per image under that image's KMS key, accepted only from an enclave of the same image or of a public release, handed only to a newer public release (ADR 0013); data keys wrapped under it; KMS with attestation (Recipient, CMS); the reader that watches the policy and grants of the image's key and keeps every state seen, for `/status`; local keys for dev |
| `envelope` | AES-256-GCM with key commitment and context binding (F1); `Sign` for values that stay readable outside the enclave and only an enclave can make (F2) |
| `store` | DynamoDB and S3 store for mailboxes, messages, the enclave's own records (sealed, or readable and signed) and entries (small versioned rows that can expire: sessions, invitations); in-memory store for dev and tests |

## Rules

- `cmd/permaily-enclave` with `-tags dev` runs locally over TCP with a self-signed certificate and `fakensm`; never import `fakensm` from non-dev enclave code.
- No CGO, no files read at runtime inside the enclave; CA roots come from `x509roots/fallback`.
- New outbound destinations must be added to the host allowlist (`cmd/permaily-host`) and documented in [../docs/threat-model.md](../docs/threat-model.md).
- The TLS key is created once and reused across restarts and upgrades; `/attestation` binds `sha256(SPKI)` as user data. It leaves the enclave only sealed. When the sealed record cannot be loaded the enclave waits: never fall back to a fresh key, which would break the CAA records and the certificate history that verifiers check.
- Log lines leave the enclave (ADR 0015). Before adding one, read it as if it were public; never put an address, a subject, content, a token or a key in it, and keep `TestMailEndToEnd`'s log check passing.
- Nothing outside the enclave sees plaintext mail: seal with `envelope` under the mailbox data key before calling `store`; never log addresses, subjects or content.
- Every KMS response is checked against the key that was asked (`keys.ErrKeyMismatch`), every key must be in the pinned account and region, and no key is used before `kmspolicy.Sealed` accepts it. Never use KMS without the Recipient parameter.
- A record whose going back to an earlier copy would matter (the TLS identity, later permissions) is stored through `store.Anchored`, never straight into the table. Decide for each what `store.ErrLost` means: made again, or refused (ADR 0016).
- Layers (F2): `contracts` ← `domain` ← `services` ← adapters. A rule that needs no storage goes in `domain`; an adapter holds no rule. `depguard` fails a service that imports `net/http`, an adapter or the AWS SDK.
- Whoever runs the storage must not get into an account by writing to the table. An account's record is signed (`envelope.Sign`) and read through `store.Anchored`; a session or an invitation is an entry signed with its partition and key, kept by the hash of its secret; how long one lasts is read from what was signed, never from the entry's own expiry. What must not be set back at all (the count of wrong sign-in codes) lives only in the enclave's memory.
- An account that cannot be read (a record that does not verify, or whose committed version is gone) is an error, never "no such account": a sign-up would replace it.
- The answer to starting a sign-in is the same, and as fast, whether the address has an account or not.
- Account, session and sign-in identifiers stay out of logs too: `httpapi` strips them from the errors it logs, and a mail system's errors, which carry addresses, are never logged.
- The root secret never leaves the enclave in the clear, not even to KMS. Never create one when a record cannot be read or opened: an image without its record waits. Never accept a root record, or hand the secret over, on anything but the checks in `keys.Root`; who asks is not one of them.
- Which builds are releases (repository, workflow, ref) and Sigstore's trust root are compiled into the image (`deploy/enclave.json`, `pkg/provenance`). A change to either changes who can receive the root secret: it needs an ADR. A running enclave takes a newer trust root only through `provenance.TrustRoot.Update`, which never changes who issues signing certificates; keep it that way, and keep nothing of it across a restart.

## Commands

```sh
go test ./...
go run -tags dev ./cmd/permaily-enclave -port 8443   # needs web/dist copied to internal/webui/dist for the SPA
go run -tags dev ./cmd/permaily-enclave -port 8443 -mailbox-id dev -mailbox-address u-dev@inbox.permaily.com -mailbox-token-sha256 <hex>   # SMTP on :2525
go test -tags dev ./cmd/permaily-enclave             # whole service end to end
# Dev mode has accounts on: make an invitation with scripts/invite.sh (PERMAILY_API, PERMAILY_ADMIN_TOKEN, -admin-token-sha256),
# and read the sign-in message, which is kept instead of sent, at https://app.<domain>:8443/dev/outbox
```
