# Decisions

Architecture Decision Records. Each one records the context, the options considered, the decision, its consequences and what remains to verify. They are never deleted: a reversed decision gets a new ADR that supersedes the old one. When a decision needed a prototype, it lives in `spikes/<name>/` with its own readme and tests, is never deployed or imported, and the ADR links to it.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-reproducible-image-with-nix.md) | Build the enclave image reproducibly with Nix and monzo/aws-nitro-util | Accepted |
| [0002](0002-tls-inside-the-enclave.md) | Terminate TLS inside the enclave with ACME TLS-ALPN-01 | Accepted |
| [0003](0003-official-go-toolchain.md) | Build Go with the official toolchain, not nixpkgs' Go | Accepted |
| [0004](0004-opentofu-and-region.md) | OpenTofu for infrastructure, AWS us-east-2, Cloudflare DNS | Accepted |
| [0005](0005-monorepo-with-the-site.md) | One repository for the service, the web app and the public site | Accepted |
| [0006](0006-personal-use-license.md) | Source-available personal use license with a CLA | Accepted, pending legal review |
| [0007](0007-oauth-server.md) | OAuth authorization server for apps and MCP clients | Superseded by 0030 |
| [0008](0008-mailbox-keys-with-kms-attestation.md) | Mailbox data keys from KMS with attestation | Accepted |
| [0009](0009-f1-test-mailbox.md) | One test mailbox in F1 | Accepted |
| [0008](0008-org-community-health-files.md) | Community health files live in the organization's .github repository | Accepted |
| [0010](0010-kms-policy-over-the-attested-tls-key.md) | Publish the KMS key policy over the attested TLS key | Accepted |
| [0011](0011-mail-only-over-tls.md) | Accept mail only over TLS and tell senders to require it | Accepted |
| [0012](0012-no-certificate-outside-the-enclave.md) | No certificate valid for the enclave hosts outside the enclave | Accepted |
| [0013](0013-authorize-releases-without-editing-the-key-policy.md) | A KMS key per release that nobody can change, and a root secret that only public releases receive | Accepted |
| [0014](0014-opentofu-state-in-s3.md) | OpenTofu state in S3, encrypted by OpenTofu | Accepted |
| [0015](0015-enclave-logs-through-the-host.md) | Send the enclave's logs to the parent instance | Accepted |
| [0016](0016-refuse-earlier-copies-of-stored-records.md) | Refuse earlier copies of stored records | Accepted |
| [0017](0017-accounts-and-sign-in.md) | Accounts, sign-in and how account data is kept | Accepted, built in part |
| [0018](0018-mailboxes-by-forwarding.md) | Mailboxes: forwarding only, several per account | Accepted, not built; how in 0026 |
| [0019](0019-connections-queries-rules-and-receipts.md) | Connections, queries, rules and receipts | Accepted, not built |
| [0020](0020-openai-models-for-meaning.md) | OpenAI's models for what depends on meaning | Accepted, not built; how in 0028, which amends it |
| [0021](0021-watch-the-copies-of-outgoing-mail.md) | Watch who else gets a copy of the mail the enclave sends | Accepted |
| [0022](0022-admins-and-confirming-again.md) | Admins, and confirming again before a sensitive change | Accepted, in production since v0.11.0 |
| [0023](0023-releases-publish-the-measurements-not-the-image.md) | Releases publish the image's measurements, not the image | Accepted |
| [0024](0024-two-enclaves-share-one-tls-identity.md) | Two enclaves share one TLS identity and answer each other's ACME challenge | Accepted, in production since v0.11.0 (one enclave runs) |
| [0025](0025-mail-with-a-design-written-once.md) | Mail with a design, written once and sent as text and HTML | Accepted, not released |
| [0026](0026-mailboxes-per-account-how.md) | Mailboxes per account: how they are built (records and their state machines, keys, delivery, the inbox and showing HTML safely, the test mailbox retired, the API, the seams); amends 0016 | Proposed, not built |
| [0027](0027-the-query-language-how.md) | The query language: how it is built (variables and operators, the JSON vendors send, three values, simplification, containment, the sentence, evaluation over stored mail, the seams); amends 0019, 0020 and 0026 | Proposed, not built |
| [0028](0028-the-model-how.md) | The model: how it is built (the spike's numbers, one interface, the request and its prompt, failures, the egress, the key, kept decisions, asking from the matcher, consent, spend limits and the alarm, the canary, the seams); amends 0020, 0026 and 0027 | Proposed, not built |
| [0029](0029-operator-scripts-send-only-to-the-attested-key.md) | The operator's scripts send the admin token and secrets only to the TLS key verification attested in the same run | Accepted |
| [0030](0030-connections-and-the-authorization-server-how.md) | Connections, the authorization server, the API and MCP: how they are built (the spike's numbers, our own server with CIMD, PKCE and resources, the vendor egress, the verified list, the connection, its devices and its replacement, codes and token families in strict records, revocation in five seconds for every byte under a lease read again while an answer is sent, assertions decided by strict records, the account's list in epochs, the consent snapshot, the consent screen, the tools, the seams; revised after its two design reviews); supersedes 0007, amends 0017, 0019 and 0027 | Proposed, not built |
| [0031](0031-the-default-rule-and-the-record-of-what-left-how.md) | The default rule and the record of what left: how they are built (the rule that holds sign-in codes and password resets, nothing leaving while the model is off, decisions begun before the model is asked in strict records of a mailbox's day in blocks, abandoned only on positive evidence and kept with their evidence, sealed receipts within one item that step 8 chains; revised after its two design reviews) | Proposed, not built |
| [0032](0032-drawings-made-of-sample-mail.md) | Drawings made of sample mail on the public site, and motion that keeps running | Accepted, built, not released |
| [0033](0033-sign-up-with-google-on-the-site.md) | "Sign up with Google" on the public site's hero, with Google's colours kept in the web app's folder | Accepted, built, not in use |
| [0034](0034-the-instance-journal-in-cloudwatch-logs.md) | Keep the instance's journal, the enclave's log lines included, in CloudWatch Logs for 90 days, shipped by the CloudWatch agent; amends 0015 | Accepted |
| [0035](0035-the-hourly-drift-check.md) | An hourly `tofu plan` on Lambda, read-only, that mails when production drifts; the port 25 check of `permaily-verify` by cron on the owner's server | Accepted |
| [0037](0037-chain-the-receipts-how.md) | Chaining the receipts: how (numbered links, each a strict anchored record made once and naming the link before it by digest, written after the receipt and before the answer, an account's commits kept together so every link is read with its full history; two enclaves meeting at a number; the head found by listing; a catalogue of chains the daily audit reads, its passes strict records `/status` reports; a record of incidents that keeps every deletion found; the receipts of before adopted and those left without a link recovered within three days; links deleted with the account and their commits kept for their lock; `permaily-verify` and an export checked offline against heads the person noted; the owner's three choices; revised after its two design reviews); brings step 8's chaining forward, amends 0031 | Proposed, not built |
