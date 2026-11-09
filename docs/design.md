# Design

## Goal

Apps such as travel assistants ask for full Gmail access to read a few messages. Permaily sits in between: the user connects their mailbox once, and each app or AI agent receives only the subset of messages the user authorized. Rules are enforced on every delivery, with an audit log and revocation.

What sets Permaily apart is verifiability: the whole service runs inside an AWS Nitro Enclave built reproducibly from this repository, so anyone can check that the code running is the published code. See [verification.md](verification.md) and [security.md](security.md).

## Components (F0)

```
client / verifier ──TLS──▶ EIP:443 ─▶ permaily-host (parent EC2, untrusted) ─vsock 443─▶ permaily-enclave
                                                                                         ├─ ACME TLS-ALPN-01, key only in RAM
                         permaily-host vsock 8000 (CONNECT allowlist) ◀──────────────────┤  egress (Let's Encrypt)
                                                                                         └─ /dev/nsm → attestation(nonce, sha256(SPKI))
```

- **permaily-enclave** (`cmd/permaily-enclave`): one Go binary. Terminates TLS, obtains one certificate for all hosts, dispatches by Host:

  | Host | Serves |
  |---|---|
  | `app.permaily.com` | web app (see [web.md](web.md)), embedded with `go:embed` |
  | `api.permaily.com` | API for integrated apps (F2) |
  | `mcp.permaily.com` | remote MCP server for ChatGPT and Claude (F2) |
  | `inbox.permaily.com` | MX for forwarded mail (F1) |

  Every host serves `/attestation?nonce=<hex>` and `/version`.
- **permaily-host** (`cmd/permaily-host`): forwards TCP 443 to vsock and exposes an allowlisted HTTP CONNECT proxy on vsock 8000 for the enclave's outbound TLS.
- **permaily-verify** (`cmd/permaily-verify`, `pkg/attestation`): verifier CLI and library.
- **Build** (`flake.nix`, `nix/`): Nix with [monzo/aws-nitro-util](https://github.com/monzo/aws-nitro-util) and AWS's prebuilt kernel/init blobs; PCR0 is identical from x86_64 and arm64 builders.
- **Infrastructure** (`deploy/`): OpenTofu; one Graviton `c7g.large` in `us-east-1` (enclave: 1 vCPU, 2 GiB), Cloudflare DNS (DNS only), S3 for deploy artifacts, SSM for administration.

The landing page at `permaily.com` (apex) is a separate project hosted on Amplify and is not part of the verifiable surface. Rule for F2: session cookies on `app.` use the `__Host-` prefix (no `Domain` attribute) so the apex cannot read or overwrite them.

## Roadmap

- **F0 (current)**: verifiable hello end to end.
- **F1 inbox**: MX inside the enclave, encrypted storage, KMS bound to attestation.
- **F2**: users (passkeys, Google sign-in), grants, API for apps, remote MCP, audit log.
- **F3**: LLM classification (model and prompts embedded in the image).
- **F4**: per-agent inboxes, a private inbox, scaling (NLB TCP passthrough, ASG), CT and Rekor monitoring in the verifier.

## Inbox design (F1, planned)

1. Each user gets `u-<128-bit random token>@inbox.permaily.com` and creates a Gmail rule forwarding all mail there. Gmail's forwarding confirmation code is shown in the user's dashboard; links are never followed automatically.
2. `MX inbox.permaily.com` points at the same IP; port 25 is forwarded to the enclave, which runs an SMTP server (`emersion/go-smtp`) with STARTTLS on the same certificate. MTA-STS (served from the enclave at `mta-sts.inbox.permaily.com`) and TLS-RPT make senders require TLS.
3. ARC/DKIM are checked inside the enclave (`emersion/go-msgauth`) with DNS over HTTPS, so the parent cannot spoof DNS answers.
4. In memory: parse (`emersion/go-message`), extract metadata, classify, encrypt, store, evaluate grants, deliver.
5. Storage outside the enclave: bodies in S3, encrypted metadata in DynamoDB, AES-256-GCM with AAD `user|message|version`. Per-user data keys are wrapped by a KMS key that only enclaves with an allowlisted PCR0 can use (`kms:RecipientAttestation:ImageSha384`). Deleting a user destroys their key (crypto-shredding).
6. KMS key policy: no `kms:*` for the account root, `Encrypt`/`GenerateDataKey` only for the enclave role, a deny on `PutKeyPolicy` except for an MFA-protected release role. The enclave publishes the hash of the live key policy in an attested `/status`, and the verifier compares it with `deploy/kms-policy.json`.
7. Grants are evaluated inside the enclave at ingest and again on every read, so revocation is immediate. The audit log is append-only and hash-chained, encrypted per user.

## Technology choices

| Area | Choice |
|---|---|
| Language | Go, static binaries, no CGO |
| Image build | Nix + monzo/aws-nitro-util, AWS prebuilt blobs (aws-nitro-enclaves-cli v1.5.2) |
| TLS | ACME TLS-ALPN-01 inside the enclave (`golang.org/x/crypto/acme`), Let's Encrypt |
| Attestation (enclave) | `hf/nsm` |
| Attestation (verifier) | own implementation on `fxamacker/cbor` and the standard library |
| vsock | `mdlayher/vsock` |
| SPA | Vite + React + TypeScript with bun |
| IaC | OpenTofu (`aws`, `cloudflare` providers) |
| Releases | GitHub Actions, build provenance attestations (Sigstore) |
| MCP (F2) | `modelcontextprotocol/go-sdk` |
| Mail (F1) | `emersion/go-smtp`, `go-message`, `go-msgauth` |
