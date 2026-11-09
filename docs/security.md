# Security model

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
| Certificate for the hosts issued to someone else | detected per connection by the binding above; CAA restricts issuance to Let's Encrypt; F1 adds CAA `accounturi` with an ACME account key sealed by KMS, and CT monitoring |
| Build tampering in CI | anyone can rebuild; CI builds on x86_64 and arm64 and compares; releases carry Sigstore provenance |
| Supply chain of dependencies | all inputs pinned by hash; review dependency changes |

## Known limits (F0)

- The TLS key and ACME account are created at each enclave boot, so each restart issues a new certificate (visible in CT). F1 seals them with KMS.
- The enclave has no persistent state yet.
- Availability is not protected: the operator can always take the service down.
- Metadata visible to the operator: connection timing, sizes, destination hostnames of egress.

## Egress allowlist

The host proxy only tunnels to:

| Destination | Why |
|---|---|
| `acme-v02.api.letsencrypt.org:443` | certificate issuance |
| `acme-staging-v02.api.letsencrypt.org:443` | test deployments |

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
| Site | No CSP violations and a strict script policy on every page | `site/e2e/a11y.spec.ts` |

## Logging

Never log email content, email addresses, tokens, cookies, codes, presigned URLs or keys, in the enclave or on the host. Enclave logs leave through the untrusted host, so they are treated as public.

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
- [ ] CT monitoring for certificates of the enclave names not bound to an attested key (F4).
- [ ] CAA `accounturi` once the ACME account key is sealed with KMS (F1).

## Reporting vulnerabilities

Open a GitHub security advisory on the repository (private vulnerability reporting).
