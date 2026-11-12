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
| [0007](0007-oauth-server.md) | OAuth authorization server for apps and MCP clients | Proposed, needs a spike |
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
| [0018](0018-mailboxes-by-forwarding.md) | Mailboxes: forwarding only, several per account | Accepted, not built |
| [0019](0019-connections-queries-rules-and-receipts.md) | Connections, queries, rules and receipts | Accepted, not built |
| [0020](0020-openai-models-for-meaning.md) | OpenAI's models for what depends on meaning | Accepted, not built |
| [0021](0021-watch-the-copies-of-outgoing-mail.md) | Watch who else gets a copy of the mail the enclave sends | Accepted |
| [0022](0022-publish-the-source-of-the-gpl-parts-of-the-image.md) | Publish the source of the GPL parts of the image with each release | Accepted, building from source pending |
