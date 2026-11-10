# Plan

What is built, what comes next and how the planned parts are designed. Current architecture: [architecture.md](architecture.md). Decisions will be recorded as ADRs in `docs/decisions/`.

## Phases

- **F0** (deployed): verifiable hello end to end.
- **F1 inbox** (current, deployed with v0.2.0 on 2026-11-10): MX inside the enclave, encrypted storage, KMS bound to attestation, one test mailbox (ADR 0009).
- **F2**: users (passkeys, Google sign-in), grants, API for apps, remote MCP, audit log.
- **F3**: LLM classification (model and prompts embedded in the image).
- **F4**: per-agent inboxes, a private inbox, scaling (NLB TCP passthrough, ASG), CT and Rekor monitoring in the verifier.

## F1: inbox

Implemented in `internal/smtpd`, `internal/inbox`, `internal/msgauth`, `internal/keys`, `internal/envelope` and `internal/store`; infrastructure in `deploy/tofu/mail.tf` (KMS key, DynamoDB table and S3 bucket already created). Decisions: ADR 0008 and 0009.

1. Mail to `u-<token>@inbox.permaily.com` arrives at the MX (`inbox.permaily.com`, port 25 forwarded by the host over vsock) with STARTTLS on the enclave's certificate; MTA-STS (`mta-sts.inbox.permaily.com`) and TLS-RPT are published with the deployment.
2. The enclave parses it (`go-message`), checks DKIM with DNS-over-HTTPS, detects Gmail's forwarding confirmation, seals metadata and raw message with the mailbox data key, and stores them in DynamoDB and S3.
3. The owners read it through `api.permaily.com/v1/mailbox` with the test token.

### Still to do in F1

- [x] No more wildcard certificates for the domain: Cloudflare Universal SSL off, exact-name certificate for the site, CAA `issuewild ";"` (ADR 0012).
- [ ] Seal the ACME account key and the TLS key with KMS, so restarts reuse the certificate; then publish CAA `accounturi` and add the Certificate Transparency check to `permaily-verify` (ADR 0012).
- [x] `/status` with the live KMS key policy, checked by `permaily-verify` (ADR 0010).
- [x] MTA-STS `enforce`, an MX that refuses clear text, and the mail transport checked by `permaily-verify` (ADR 0011).
- [x] Verify the KMS flow against production (2026-11-10, [acceptance.md](acceptance.md)).
- [x] Publish the MX, MTA-STS and TLS-RPT records (2026-11-10).
- [ ] Forward a Gmail account to the test mailbox with the release image.

## F2: lessons to apply

From Portifact's production experience with the same MCP clients (recorded in [decisions/0007-oauth-server.md](decisions/0007-oauth-server.md) and [mcp-compatibility.md](mcp-compatibility.md)):

- One authorization server inside the enclave for the API and MCP: opaque tokens checked on every request (immediate revocation), audience per resource, scope step-up per tool, CIMD first, device flow for a future `permaily` CLI.
- Layers: `contracts` (types and schemas) → `domain` (pure rules: grants, withheld rules) → `services` → adapters (API, MCP, CLI, web), with parity tests so the API, MCP and CLI behave the same. `depguard` enforces the layering.
- Contracts defined once in Go, with OpenAPI 3.1 generated for the web app's client and MCP tool schemas derived from the same structs. Errors as `{error:{code,message,details}}`; `Idempotency-Key` on writes.
- Transactional outbox for webhook deliveries (DynamoDB transactions), idempotent jobs.
- Audit log modeled on Portifact's access log: separate store, owner-only, exportable (CSV guarded against formula injection, JSON), plus a hash chain.
- Rate limits with atomic counters and a budget guard.
- Session cookies with the `__Host-` prefix, `Sec-Fetch-Site` and `Origin` checks, and an allowlist of auth endpoints.
- MCP server instructions state that email content is data, never instructions; tools carry `readOnlyHint` and `destructiveHint`.
- The agent skill in `skills/permaily/` grows with the CLI and MCP, in the same change, describing only released features.

