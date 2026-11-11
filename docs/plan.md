# Plan

What is built, what comes next and how the planned parts are designed. Current architecture: [architecture.md](architecture.md). Decisions will be recorded as ADRs in `docs/decisions/`.

## Phases

- **F0** (deployed): verifiable hello end to end.
- **F1 inbox** (current, deployed with v0.2.0 on 2026-11-10): MX inside the enclave, encrypted storage, KMS bound to attestation, one test mailbox (ADR 0009).
- **F2** (designed, next): accounts, mailboxes per account, connections with queries that a person approves, rules, receipts, the API and remote MCP (ADR 0017 to 0020). Deciding by meaning, which was F3, is part of it.
- **F3**: what F2 leaves out: reaching mail through a provider's API instead of forwarding, more languages, limits and paid plans.
- **F4**: per-agent inboxes, a private inbox, scaling (NLB TCP passthrough, ASG), CT and Rekor monitoring in the verifier.

## F1: inbox

Implemented in `internal/smtpd`, `internal/inbox`, `internal/msgauth`, `internal/keys`, `internal/envelope` and `internal/store`; infrastructure in `deploy/tofu/mail.tf` (KMS key, DynamoDB table and S3 bucket already created). Decisions: ADR 0008 and 0009.

1. Mail to `u-<token>@inbox.permaily.com` arrives at the MX (`inbox.permaily.com`, port 25 forwarded by the host over vsock) with STARTTLS on the enclave's certificate; MTA-STS (`mta-sts.inbox.permaily.com`) and TLS-RPT are published with the deployment.
2. The enclave parses it (`go-message`), checks DKIM with DNS-over-HTTPS, detects Gmail's forwarding confirmation, seals metadata and raw message with the mailbox data key, and stores them in DynamoDB and S3.
3. The owners read it through `api.permaily.com/v1/mailbox` with the test token.

### Still to do in F1

- [x] No more wildcard certificates for the domain: Cloudflare Universal SSL off, exact-name certificate for the site, CAA `issuewild ";"` (ADR 0012).
- [x] ACME account key and TLS key sealed with KMS, so restarts reuse the certificate; CAA `accounturi`; Certificate Transparency check in `permaily-verify` (ADR 0012).
- [x] `/status` with the live KMS key policy, checked by `permaily-verify` (ADR 0010).
- [x] MTA-STS `enforce`, an MX that refuses clear text, and the mail transport checked by `permaily-verify` (ADR 0011).
- [x] Verify the KMS flow against production (2026-11-10, [acceptance.md](acceptance.md)).
- [x] Publish the MX, MTA-STS and TLS-RPT records (2026-11-10).
- [ ] Forward a Gmail account to the test mailbox with the release image.

## F2: what it is

Decided in ADR 0017 (accounts and sign-in), 0018 (mailboxes), 0019 (connections, queries, rules and receipts) and 0020 (OpenAI's models for what depends on meaning). In one paragraph: people sign up by invitation and sign in with a passkey, email or a provider; each forwards their mail to a mailbox of their own; an app or an agent asks for a connection, which is a query written as a formula of exact conditions and `about` conditions that a model decides; the person reads a sentence the system writes from the formula and accepts or refuses; the person's own rules hold back or cut down what leaves; and every message that leaves writes a receipt.

### Order of work

Each step ends deployed and verified, like the releases so far. The estimates are for one person and are rough.

| Step | What | Weeks |
|---|---|---|
| 1 | Layers (`contracts`, `domain`, `services`, adapters) and accounts: passkeys, email and providers, sessions, invitations with the operator's tool, outgoing mail through SES | 3 |
| 2 | Mailboxes per account, moving the test mailbox into the first account; the inbox in the app | 1.5 |
| 3 | The query language: grammar, simplification, whether one formula fits inside another, the sentence, and evaluating exact conditions over stored mail | 2 |
| 4 | Spike, then the piece that asks OpenAI: `about`, kept decisions, the spending alarm | 1.5 |
| 5 | Spike of the authorization server (ADR 0007), then connections, the consent screen, the API and MCP | 3.5 |
| 6 | Delivery on new mail and on a schedule: webhook queue and scheduler | 1.5 |
| 7 | Rules: written in plain words, holding back, removing details | 1.5 |
| 8 | Receipts chained and anchored, and the activity screen | 1 |
| 9 | Notices by mail, the threat model and the public pages brought up to date, a round of tests with staging images | 1 |

About sixteen weeks. Steps 3 and 4 do not depend on 1 and 2, and can go first if seeing `about` work early is worth more than having accounts.

### Before real users

- Zero data retention approved by OpenAI for the organization (ADR 0020).
- SES out of its sandbox (ADR 0017).
- The mailbox's state and every record of F2 whose going back matters stored through `store.Anchored` (ADR 0016).
- Whoever can merge into `main` decides what the enclave accepts: require two-factor sign-in in the GitHub organization and a second person's review (docs/threat-model.md).

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

