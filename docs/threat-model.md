# Threat model

## What verification proves

That the service answering on the Permaily hosts runs an enclave image built from a specific public commit, and that your TLS connection terminates inside that enclave. It does not prove that the code is correct or free of bugs; it proves that the code you can read is the code that runs.

## Trusted

- **AWS Nitro hardware and hypervisor**, the Nitro Security Module and the AWS Nitro Enclaves root certificate (pinned by fingerprint in `pkg/attestation`).
- **AWS prebuilt enclave kernel, init and nsm.ko** from aws-nitro-enclaves-cli (pinned by hash in `nix/eif.nix`).
- **The source code and its pinned dependencies**: Go modules (`go.sum`, `vendorHash`), npm packages (`bun.lock` and the `node_modules` hash), nixpkgs (`flake.lock`).
- **Let's Encrypt** for issuing certificates to the enclave's key, and the public WebPKI for the verifier's TLS checks and for the enclave's connections to KMS.
- **Sigstore's public instance** (Fulcio, the Rekor transparency logs and, for entries of Rekor v2, the timestamp authority that says when a signature existed; all pinned in `pkg/provenance`; three of the five keyholders of Sigstore's TUF root can give a running enclave other logs and timestamp authorities, never another issuer of certificates, ADR 0013) and **GitHub Actions** (its token issuer, its hosted runners, and the rule that `main` only changes through reviewed merges): together they say which images are public releases, and a running enclave hands the root secret only to those.
- In later phases: the LLM provider used for classification (model and prompts are in the image).

## Not trusted

- **The parent EC2 instance and its operator.** It sees only ciphertext and destination hostnames. It can drop, delay or replay traffic, and stop the service; it cannot read or alter content without the verifier detecting it.
- **DNS (Cloudflare) and the network.** A hijacked record points clients elsewhere; the verifier detects it because no other server can present a TLS key bound into a valid attestation. The zone is signed with DNSSEC, so a resolver that validates refuses records forged on the way; whoever controls the Cloudflare account controls the zone all the same.
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

- The TLS key and the ACME account key are created once inside an enclave and stored sealed under the root secret (ADR 0012, ADR 0013), so restarts and upgrades keep the same key, certificate and account. Only what can get the root secret can open them, like stored mail.
- The enclave keeps nothing itself: what it stores is sealed and lives in DynamoDB and S3, which the operator can delete, not read. Putting back an earlier copy is refused for the records that go through `store.Anchored` (ADR 0016): today the TLS identity. The mailbox's state and the messages can still be put back to an earlier copy, or removed; they get the same protection, and an audit log, with the data model.
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

- **Root secret.** Everything stored is sealed under data keys, and every data key is wrapped under one root secret created inside an enclave (ADR 0013). Outside enclaves the root secret exists only in records, one per image, each locked by a KMS key that belongs to that image alone.
- **Keys nobody in the account can open or change.** The policy of an image's KMS key lets only an enclave that proves that image (PCR0) decrypt, and gives nobody `kms:PutKeyPolicy` or `kms:CreateGrant`, so KMS refuses to change it: account administrators were denied `PutKeyPolicy`, `CreateGrant`, `Decrypt`, `Encrypt`, `DisableKey`, `ReplicateKey` and more on such a key (checked 2026-11-10). Anyone in the account can create a data key under it, which gives them nothing: see the next point.
- **Only public releases receive the root secret.** An image accepts a record only with an AWS-signed attestation document from the enclave that wrote it, which must have run the same image or a public release, proved by that release's Sigstore bundle. The running enclave writes a record only for a release that was published, in the public transparency log, after itself, and whose key passes the check above. So neither a private image nor a record made by the operator gets the root secret, whoever asks.
- **Pinned account.** The AWS account and region are compiled into the image and every key must be in them; every KMS answer must name the key that was asked (`keys.ErrKeyMismatch`). The host supplies the AWS credentials (its own instance role); credentials for another account cannot make the enclave use a key elsewhere.
- **Encryption.** Message metadata, raw message and mailbox state are sealed with AES-256-GCM plus a key commitment, bound to mailbox, message id and purpose (`internal/envelope`).
- **What the operator sees.** Message sizes and arrival times, retention period, the number of messages, and (until accounts exist) the single test mailbox. Not senders, subjects, bodies or attachments.
- **Transport.** SMTP STARTTLS terminates in the enclave with the attested certificate, and the MX refuses `MAIL` on a connection without TLS. MTA-STS (`mode: enforce`) tells senders to deliver only to `inbox.permaily.com` with a valid certificate; TLS-RPT reports go to the test mailbox. `permaily-verify` checks the policy (served with the attested key), the `_mta-sts` and MX records, and that port 25 answers with the attested key (ADR 0011).
- **Gap: senders without MTA-STS, and DNS.** A sender that does not implement MTA-STS can be made to deliver in clear to whoever answers on port 25. DNS is the operator's: removing the `_mta-sts` record stops new senders from applying the policy (cached policies last a week); the verifier fails when it is missing.
- **Port 25 between manual checks.** The scheduled verification cannot reach port 25 (GitHub's runners block it), so the certificate served there is only checked by hand, from a server, with `-require-smtp`. Decided on 2026-11-11 to leave it so: a certificate for the host with a key outside the enclave shows in the certificate check, which runs every half hour, and what stays unwatched is the gap above, for senders without MTA-STS.
- **Senders.** Any sender may deliver to the mailbox (decision for F1); DKIM results are recorded with each message. A Gmail forwarding confirmation is only marked verified when it carries a valid `google.com` DKIM signature.
- **Who can use the key is public.** The enclave reads the policy, the grants and the properties of its image's key from KMS and serves them on `/status`; `permaily-verify` accepts that only over the attested TLS key and applies the same rule the enclave does (`kmspolicy.Sealed`): this image only, no way to change the policy from the account, no grants, key material that never left KMS.
- **The policy between checks.** AWS documents that its Support can restore the account owner's access to a key that nobody can manage. So the enclave reads the policy and the grants of its key once a minute and reports every state it has seen since it started; the verifier requires a reading made just now and checks every state, and a public job runs the verifier every half hour (ADR 0013). That job is started from the operator's AWS account, with a GitHub token that can start, cancel and delete workflow runs of this repository and nothing else; so the operator chooses when it runs, and GitHub's own schedule, which the operator does not time, starts it a few times a day as well. What remains: a change made while the enclave is cut off from KMS shows only as a gap, and restarting the enclave starts the history again (the start time is printed).
- **What remains.** An older release can still open its own record until its key is deleted, so a flaw in an old image stays a way in for that long (docs/operations.md). Whoever controls Sigstore's signing or GitHub's runners for this repository can publish an image that is accepted; it would be public in the transparency log and in the repository. The account's owner, with AWS Support, could regain control of a key and then open the root secret; it takes a support case and shows in the enclave's report from then on. The operator can still delete keys or records, which destroys data and reads nothing.
- **The old mail key.** Releases up to v0.6.0 wrapped data keys with `alias/permaily-mail`, whose policy administrators could replace. v0.7.0 moved those data keys under the root secret, the key was retired on 2026-11-10 with a policy that lets nobody use it, and it is scheduled for deletion.

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
| Keys | A key is used only if this image alone can use it and nobody can change that | `TestSealed`, `TestHandoffRefuses`, `TestRootNeedsItsOwnKey` |
| Storage | An earlier copy of an anchored record is never read: lost, not rolled back | `TestAnchoredRefusesEarlierCopies`, `TestS3CommitsWithAnAdversary`, `TestS3CommitsAgainstS3` (a real bucket), `TestAnchoredNoticesCommitsGoingBack`, `TestCheckAnchors` |
| Keys | The root secret is handed only to a public release newer than the running one | `TestHandoffRefuses`, `TestVerifyIdentity`, `TestVerifyARealRelease`, `TestVerifyRekor2`, `TestTimestamps`, `TestSigstoreHistory`, `TestUpdateKeepsTheAuthorities`, `TestTUFSignatures`, `TestRootFollowsSigstore`, `FuzzVerify`, `FuzzVerifyRekor2`, `FuzzTimestamp`, `FuzzTrustUpdate`, `tests/sigstore-differential` |
| Keys | A record not written by an enclave of the same image or of a public release is refused | `TestRootRefusesPlantedRecords`, `TestRootChecksWhichKeyAnswered` |
| Keys | A debug enclave, a failing store or a deployment that already has a root secret never leads to a new one | `TestRootInDebugMode`, `TestRootDoesNotCreateWhenTheStoreFails`, `TestRootHandoff` |
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

## Reporting vulnerabilities

Follow the [security policy](https://github.com/permaily-org/permaily/security/policy): report privately through the repository's Security tab (private vulnerability reporting), never in a public issue. The policy lives in [permaily-org/.github](https://github.com/permaily-org/.github/blob/main/security.md) and applies to every repository in the organization.
