# Threat model

## What verification proves

That the service answering on the Permaily hosts runs an enclave image built from a specific public commit, and that your TLS connection terminates inside that enclave. It does not prove that the code is correct or free of bugs; it proves that the code you can read is the code that runs.

## Trusted

- **AWS Nitro hardware and hypervisor**, the Nitro Security Module and the AWS Nitro Enclaves root certificate (pinned by fingerprint in `pkg/attestation`).
- **AWS prebuilt enclave kernel, init and nsm.ko** from aws-nitro-enclaves-cli (pinned by hash in `nix/eif.nix`).
- **The source code and its pinned dependencies**: Go modules (`go.sum`, `vendorHash`), npm packages (`bun.lock` and the `node_modules` hash), nixpkgs (`flake.lock`).
- **Let's Encrypt** for issuing certificates to the enclave's key, and the public WebPKI for the verifier's TLS checks.
- In later phases: the LLM provider used for classification (model and prompts are in the image).

## Not trusted

- **The parent EC2 instance and its operator.** It sees only ciphertext and destination hostnames. It can drop, delay or replay traffic, and stop the service; it cannot read or alter content without the verifier detecting it.
- **DNS (Cloudflare) and the network.** A hijacked record points clients elsewhere; the verifier detects it because no other server can present a TLS key bound into a valid attestation.
- **Anything the parent supplies at runtime.** The enclave takes no configuration from the parent; hosts and the ACME directory are compiled into the image.

## Mechanisms

| Threat | Mitigation |
|---|---|
| Service runs different code | PCR0-2 in a fresh, AWS-signed attestation must equal the PCRs reproduced from source |
| Debug mode (console access, PCRs zero) | the verifier rejects all-zero PCRs |
| Replay of an old attestation | random 32-byte nonce per verification, 5-minute freshness window |
| TLS terminated outside the enclave (MITM, parent, CDN) | attestation `user_data = sha256(SPKI)` of the certificate on the same connection |
| Certificate for the hosts issued to someone else | detected per connection by the binding above, so only for people who verify: a browser accepts any valid certificate. CAA restricts the hosts to Let's Encrypt and forbids wildcards for the domain; Cloudflare Universal SSL is off and the site has an exact-name certificate (ADR 0012). CAA names the enclave's ACME account, whose key is sealed, and `permaily-verify` reads Certificate Transparency and fails on a certificate for the hosts with another key issued since the enclave's key exists (`pkg/certwatch`). Four wildcards issued before 2026-11-10 with keys at Cloudflare and AWS stay valid until 2027-05-26 at the latest. DNS is the operator's, so CAA can change between checks; Certificate Transparency shows it afterwards |
| Build tampering in CI | anyone can rebuild; CI builds on x86_64 and arm64 and compares; releases carry Sigstore provenance |
| Supply chain of dependencies | all inputs pinned by hash; review dependency changes |

## Known limits (F0)

- The TLS key and the ACME account key are created once inside an enclave and stored sealed with KMS (ADR 0012), so restarts and upgrades keep the same key, certificate and account. Whoever the KMS key policy lets in can open them, like stored mail.
- The enclave has no persistent state yet.
- Availability is not protected: the operator can always take the service down.
- Metadata visible to the operator: connection timing, sizes, destination hostnames of egress.

## Egress allowlist

The host proxy only tunnels to:

| Destination | Why |
|---|---|
| `acme-v02.api.letsencrypt.org:443` | certificate issuance |
| `acme-staging-v02.api.letsencrypt.org:443` | test deployments |
| `kms.us-east-2.amazonaws.com:443` | mailbox data keys (Recipient with attestation) |
| `dynamodb.us-east-2.amazonaws.com:443` | sealed mailbox and message records |
| `s3.us-east-2.amazonaws.com:443` | sealed raw messages (path-style requests) |
| `cloudflare-dns.com:443` | DNS-over-HTTPS for DKIM lookups, so the host cannot forge DNS answers |

## Mail (F1)

- **Keys.** Each mailbox has an AES-256 data key, wrapped by the KMS key `alias/permaily-mail`. The key policy grants `GenerateDataKey` and `Decrypt` only to the instance role **and** only with an attestation document whose PCR0 is in the allowlist (`kms:RecipientAttestation:ImageSha384`); KMS then encrypts the key to an RSA key that exists only in the enclave (CMS, RSAES-OAEP-SHA-256). The account's administrators have no data-key permissions: an attempt with the account admin role returns `AccessDenied` (checked 2026-11-09).
- **Pinned key.** The KMS key ARN is compiled into the image and every KMS response's `KeyId` must match it (`keys.ErrKeyMismatch`). The host supplies the AWS credentials (its own instance role); credentials for another account cannot make the enclave use a key the operator controls.
- **Encryption.** Message metadata, raw message and mailbox state are sealed with AES-256-GCM plus a key commitment, bound to mailbox, message id and purpose (`internal/envelope`).
- **What the operator sees.** Message sizes and arrival times, retention period, the number of messages, and (until accounts exist) the single test mailbox. Not senders, subjects, bodies or attachments.
- **Transport.** SMTP STARTTLS terminates in the enclave with the attested certificate, and the MX refuses `MAIL` on a connection without TLS. MTA-STS (`mode: enforce`) tells senders to deliver only to `inbox.permaily.com` with a valid certificate; TLS-RPT reports go to the test mailbox. `permaily-verify` checks the policy (served with the attested key), the `_mta-sts` and MX records, and that port 25 answers with the attested key (ADR 0011).
- **Gap: senders without MTA-STS, and DNS.** A sender that does not implement MTA-STS can be made to deliver in clear to whoever answers on port 25. DNS is the operator's: removing the `_mta-sts` record stops new senders from applying the policy (cached policies last a week); the verifier fails when it is missing.
- **Senders.** Any sender may deliver to the mailbox (decision for F1); DKIM results are recorded with each message. A Gmail forwarding confirmation is only marked verified when it carries a valid `google.com` DKIM signature.
- **Who can use the key is public.** The enclave reads the key's policy and grants from KMS and serves them on `/status`; `permaily-verify` accepts that only over the attested TLS key and fails if any image other than the verified one can get data keys, if a statement skips the image check, or if a grant exists (ADR 0010, `pkg/kmspolicy`).
- **The policy between checks.** An administrator can still replace the key policy (`kms:PutKeyPolicy`), allow another image or themselves, and restore it. The enclave reads the policy every minute and reports every state it has seen since it started; the verifier checks all of them, and a public job runs the verifier twice an hour (ADR 0013). What remains: a change made while the enclave is cut off from KMS shows only as a gap, and restarting the enclave starts the history again (the start time is printed). Planned: a policy nobody can change, once releases can be authorized without editing it.

## Invariants

Each one has, or must get, a test that tries to break it.

| Area | Invariant | Test |
|---|---|---|
| Attestation | No document is accepted unless signed under the pinned AWS root, fresh, with the caller's nonce, non-zero PCRs equal to the rebuild, and `user_data` equal to the connection's SPKI hash | `pkg/attestation` tests and `FuzzVerify` |
| Build | The image does not depend on the build machine | CI `reproducible` |
| Host | The parent links no enclave code (NSM, TLS keys, web app) | `depguard` rules in `.golangci.yml` |
| Host | Egress only reaches allowlisted destinations | `TestConnectProxy` |
| Enclave | No configuration is read from the parent at runtime | review (entry point arguments are baked into the image) |
| Production code | Fake attesters and test chains are never linked | `depguard` |
| Web | Every response carries HSTS, CSP, `nosniff`, `no-referrer` | `TestRouter` |
| TLS | The TLS and ACME account keys leave the enclave only sealed, survive restarts, and are never replaced when storage fails | `TestSealedKeepsTheIdentity`, `TestSealedRejects`, `TestManagerSurvivesRestart`, `TestManagerWaitsForItsIdentity` |
| TLS | CAA records that let another account, CA or a wildcard in, or a logged certificate with another key, fail verification | `TestCheckPinned`, `TestCheckNoWildcard`, `TestClassify`, `TestCheckCertificates` |
| Logs | No log line names an address, a subject, content or a token | `TestMailEndToEnd` (log capture) |
| Logs | Logging never blocks or fails the enclave when the host does not read | `TestLogSinkNeverBlocks`, `TestLogSinkReconnects` |
| Mail | The MX stores nothing that arrived without TLS | `TestRefusesMailWithoutTLS` |
| Mail | An MX or an MTA-STS policy that does not hold the attested key, a policy that is not `enforce` for this MX only, or missing DNS records fail verification | `TestVerifySMTP`, `TestFetchMTASTS`, `TestCheckMail` |
| Mail | The store never holds plaintext senders, subjects, bodies or attachment names | `TestStoreNeverSeesPlaintext` |
| Mail | A sealed value cannot be opened under another key or context | `TestSealOpen` |
| Mail | KMS answers for any key other than the pinned one are rejected | `TestKMSRejectsOtherKey` |
| Mail | The CMS parser never panics or accepts garbage | `FuzzCMS`, `TestCMSRejectsGarbage` |
| Mail | The mailbox API rejects missing or wrong tokens | `TestMailboxAuth` |
| Mail | A key policy that lets anyone use the key without proving the verified image fails verification | `TestAnalyzeProblems`, `TestCheckKMS`, `FuzzAnalyze` |
| Mail | A policy that was opened and closed again while the enclave ran still fails verification | `TestPolicyReaderHistory`, `TestPolicyReaderGaps`, `TestCheckKMSHistory` |
| Mail | The status is accepted only over the attested TLS key | `TestFetchStatus` |
| Mail | Account administrators cannot obtain data keys | manual check (2026-11-09), `docs/acceptance.md` |
| Mail | KMS gives the data key to the allowlisted image and denies it in debug mode | production check (2026-11-10), `docs/acceptance.md` |
| Site | No CSP violations and a strict script policy on every page | `site/e2e/a11y.spec.ts` |

## Logging

Never log email content, email addresses, tokens, cookies, codes, presigned URLs or keys, in the enclave or on the host. Enclave logs leave through the untrusted host (vsock port 8003, printed by `permaily-host` into the instance's journal; ADR 0015), so they are public: a log line needs the same care as an API response. `TestMailEndToEnd` fails if a line logged while handling a message names an address, the subject, the body or the token.

## Review checklist

For every change that touches the enclave, the host, auth, storage or the build:

1. Does it add a way for the parent instance or a third party to read or alter user data?
2. Does it read configuration or code from outside the image at runtime?
3. Does it add an outbound destination? Is it on the allowlist and in this document?
4. Does it log anything from the list above?
5. Does every new rejection path have a negative test?
6. Does the image still build identically on x86_64 and arm64?

## TODO

- [ ] Site: move the demos' inline `style` attributes to classes and remove `style-src-attr 'unsafe-inline'` from `site/astro.config.mjs`.
- [ ] Deny `kms:PutKeyPolicy` in the key policy once a release can be authorized without editing it (ADR 0013, option 3); until then changes are detected, not prevented.
- [ ] MTA-STS `mode: enforce` once delivery from Gmail over TLS is confirmed in production.

## Reporting vulnerabilities

Follow the [security policy](https://github.com/permaily-org/permaily/security/policy): report privately through the repository's Security tab (private vulnerability reporting), never in a public issue. The policy lives in [permaily-org/.github](https://github.com/permaily-org/.github/blob/main/security.md) and applies to every repository in the organization.
