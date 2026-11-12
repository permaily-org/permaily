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
| 2 | Mailboxes per account, moving the test mailbox into the first account; the inbox in the app. Made by a write that only creates, and every change written at the version read, never with a plain write: two enclaves may run at once (ADR 0024 did this for the test mailbox) | 1.5 |
| 3 | The query language: grammar, simplification, whether one formula fits inside another, the sentence, and evaluating exact conditions over stored mail | 2 |
| 4 | Spike, then the piece that asks OpenAI: `about`, kept decisions, the spending alarm | 1.5 |
| 5 | Spike of the authorization server (ADR 0007), then connections, the consent screen, the API and MCP | 3.5 |
| 6 | Delivery on new mail and on a schedule: webhook queue and scheduler | 1.5 |
| 7 | Rules: written in plain words, holding back, removing details | 1.5 |
| 8 | Receipts chained and anchored, and the activity screen | 1 |
| 9 | Notices by mail, the threat model and the public pages brought up to date, a round of tests with staging images | 1 |

Step 1, in the order it is built (all of it released in v0.10.0 on 2026-11-11):

- [x] The layers, and the core of accounts: invitations made by the operator (`scripts/invite.sh`), signing up and signing in by email with a code or a link, sessions. On in dev mode only (2026-11-11).
- [x] Outgoing mail through SES, which turns accounts on in the image: the SES identity and its DNS records, the host's allowlist, the watch on who else gets a copy of what is sent (ADR 0021), the threat model, a staging image (2026-11-11). Asking AWS to leave the sandbox is under "Before real users".
- [x] The sign-in and sign-up screens of the web app, and the app behind a session (2026-11-11). Made without a design of their own: layout and copy are for the designer to review.
- [x] Passkeys, the "passkey only" switch and recovery codes, with their screens and the real list of sessions in Settings (2026-11-11). Not yet tried with a real authenticator in a real browser: that needs a certificate browsers trust, so it waits for the release.
- [x] Google, GitHub, Meta and Microsoft, with their screens (2026-11-11). Built and tried against providers played by the tests. Tried against the real Google and GitHub in production on 2026-11-12 (v0.10.0): sign-up with Google by invitation, linking GitHub from Settings, signing in with each to the same account, the Back button at Google and opening a used answer again. Not against Meta and Microsoft, which have no application registered (docs/operations.md); cancelling at GitHub could not be tried, since GitHub sends a browser that authorized the application straight back.
- [x] Agreeing to the terms of service and the privacy policy at sign-up, and again when they change (ADR 0017, 2026-11-12). Needed before the site publishes them.
- [x] Settings: name, sessions per device, deleting the account. The operator's tool to see accounts (`scripts/accounts.sh`) (2026-11-11).

After v0.10.0, built on 2026-11-12 and released in v0.11.0, in production since 2026-11-12 (ADR 0017):

- [x] What the operator can do to an account through the enclave: block and unblock, ask to delete and cancel, sign devices out, find by address, list and revoke invitations (`scripts/accounts.sh`, `scripts/invite.sh`), with every action counted on `/status` and printed by `permaily-verify`. Not what would be a way in: no address, no way to sign in.
- [x] Changing the contact address, with a code sent to the new one (server side; the screen is in the web app).
- [x] The notices by mail that need only accounts, brought forward from step 9: a sign-in from a new device, a change in how the account is signed into, the address changed, deleting asked or cancelled, blocked and unblocked.
- [x] Only the person cancels a deletion they asked for; the operator cancels only one the operator asked for (2026-11-12).
- [x] Confirming again before a sensitive change, and admins in the web app with a super admin named by the image, an admin log and counts (ADR 0022, server side; the screens are in the web app).
- [x] Move what sign-in keeps in memory (attempts, ceremonies, trips) to the storage, so that two enclaves can run at once (ADR 0017, "Two enclaves at once"), with the races of accounts that two enclaves had: the providers' applications, linking and unlinking, a removal against a cancel, signing devices out, and the stale pointer of an address. What `/status` says stays one enclave's.
- [x] The TLS identity and the test mailbox ready for two enclaves: one order, the CA's check answered by either, the mailbox made once (ADR 0024, 2026-11-12).
- [ ] A staging image with all of it: the notices through SES, and the counts on `/status` read by `permaily-verify`.

Step 2, designed in ADR 0026 (2026-11-12), in the order it is built. Five pull requests: the first four at the same time once the design merges, the fifth (showing HTML) once 2 and 3 have merged; their scope, files, seams and tests are in ADR 0026, "Implementation pull requests" and "Its pull request: the fifth". The server core goes first; the others are built against its interfaces and merged after it, and a release takes all five: the owner decided on 2026-11-12 that the inbox shows HTML from the start.

- [x] The design and the contract: ADR 0026, `internal/contracts/mailboxes.go`, the same types in `web/src/lib/api.ts`, docs/web.md, docs/data.md, docs/threat-model.md (2026-11-12).
- [ ] **1. Server core.** `internal/services/mailboxes`: the mailbox record and the account's list, both anchored, the pointer named by an HMAC of the address, a data key per mailbox, making (with the limit of five), label and retention, the forwarding confirmation and the verified Gmail account, disconnecting (record first, then slot and pointer) and the sweep that deletes the mail, pausing, an account's mailboxes removed with it; `RCPT TO` and ingest through it, with the mark that stores a message once; the test mailbox moved into the owner's account at the first start, and `/v1/mailbox` answering 410; the two-enclave tests of ADR 0026.
- [ ] **2. Server messages.** The inbox: list, one message's text (from HTML inside the enclave when it has no text part), attachments and the raw message as files, deleting one and many, search by a bounded scan of sealed `txt#` entries; every route under `/api/v1/mailboxes`.
- [ ] **3. Web.** Accounts on real mailboxes (list, connect, the forwarding set-up with the code as it arrives, verified or not, disconnect), and the new Inbox (list, search, one message, delete, empty states, "nothing from before forwarding started").
- [ ] **4. Operator, admin panel and notices.** Counts and records without addresses, finding a mailbox by its address, pause and resume (token, panel, `/status` kinds, admin log), the five notices by mail.
- [ ] **5. Showing HTML.** `internal/mailhtml`: the sanitiser (elements, attributes, URL schemes and CSS by allowlist, `cid:` and `data:` images checked by their bytes, links rewritten to the link page, the output parsed back to itself, the limits), its corpus of hostile messages and fuzz target; the routes `.../html` (the frame's document and headers) and `.../links/{n}`; `Message.html`; `frame-src 'self'` in the app's CSP; the frame, the switch, the notes and the link page in the web app; Playwright tests in Chromium, Firefox and WebKit that no request leaves, no script runs and the frame does not navigate.
- [ ] A staging image with all five: a Gmail account forwarding to a new mailbox through both steps of Gmail's set-up, the test mailbox moved, a disconnect deleting its mail; then the release.
- [ ] The release after: delete F1's mailbox item (`pk=mailbox#test`, `sk=mailbox`), the `/v1/mailbox` route and `TokenAuth` if nothing else uses it.

Step 9 keeps the notices about connections (one approved, one about to end), which need step 5, and the wording of every message reviewed with the designer.

About sixteen weeks. Steps 3 and 4 do not depend on 1 and 2, and can go first if seeing `about` work early is worth more than having accounts.

### Before real users

- ~~Zero data retention approved by OpenAI for the organization (ADR 0020).~~ Not a condition for real users: decided on 2026-11-12 that real mail goes to OpenAI before it is approved, under OpenAI's ordinary retention, disclosed on the privacy page; zero data retention is asked for and stays a residual risk until approved (docs/legal.md).
- ~~SES out of its sandbox (ADR 0017).~~ Done on 2026-11-11: AWS granted production access in `us-east-2`, 50,000 messages a day and 14 a second.
- The mailbox's state and every record of F2 whose going back matters stored through `store.Anchored` (ADR 0016). For mailboxes: step 2 (ADR 0026).
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

