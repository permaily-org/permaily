# Verification

## Quick start

```sh
git clone https://github.com/permaily-org/permaily && cd permaily
scripts/verify.sh v0.1.0          # replace with the release you want to check
```

The script needs Nix (with flakes) or Docker. It rebuilds the enclave image of that tag, prints the reproduced PCRs and runs `permaily-verify` against `app.`, `api.`, `mcp.` and `inbox.permaily.com`. Exit code 0 means every host passed.

## What is checked

For each host, `permaily-verify`:

1. Generates a random 32-byte nonce and calls `GET https://<host>/attestation?nonce=<hex>` with normal WebPKI TLS verification.
2. Decodes the COSE_Sign1 document and verifies its ECDSA P-384 signature.
3. Verifies the signing certificate chain up to the AWS Nitro Enclaves root G1 (SHA-256 fingerprint `641a0321a3e244efe456463195d606317ed7cdcc3c1756e09893f3c68f79bb5b`), at the document's timestamp.
4. Checks the timestamp is within 5 minutes of now and the nonce matches.
5. Rejects all-zero PCR0-2 (debug mode).
6. Compares PCR0, PCR1 and PCR2 with your reproduced build.
7. Checks that the document's `user_data` equals `sha256(SubjectPublicKeyInfo)` of the TLS certificate presented on the same connection. This proves the TLS session ends inside the attested enclave.

## Reproducing the measurements yourself

```sh
nix build .#eif        # result/permaily.eif, result/pcrs.json, result/SHA256SUMS
go run ./cmd/permaily-verify -pcrs result/pcrs.json
```

The build gives identical bytes on x86_64 and arm64 Linux (CI enforces this). Each release also publishes `pcrs.json`; `permaily-verify -release <tag>` uses it, which is weaker because it trusts CI instead of your own build.

The commit is compiled into the image, so the PCRs identify a commit. Builds from a working tree with uncommitted changes produce different PCRs.

## Attestation endpoint

`GET /attestation?nonce=<hex, 16-512 bytes>` returns:

```json
{
  "document": "<base64 COSE_Sign1 attestation document>",
  "user_data_scheme": "sha256-spki-v1",
  "commit": "<git commit, informational>"
}
```

## Test deployments

Images built with `.#eif-staging` use Let's Encrypt staging. Their PCRs differ from the production image and their certificates are not publicly trusted:

```sh
nix build .#eif-staging --out-link result-staging
curl -sO https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x1.pem
go run ./cmd/permaily-verify -pcrs result-staging/pcrs.json -ca-file letsencrypt-stg-root-x1.pem
```

## Planned

`scripts/verify.sh` also compares the reproduced PCRs with the ones published in the release and checks their Sigstore build provenance with `gh attestation verify` when `gh` is installed. Still planned: Certificate Transparency monitoring for certificates not bound to an attested key, and KMS key policy verification (F1).
