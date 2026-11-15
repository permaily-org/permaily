# 0028 The operator's scripts send the token and secrets only to the attested key

Status: Accepted.
Date: 2026-11-15

## Context

The operator's scripts (`scripts/provider.sh`, `accounts.sh`, `invite.sh`, `handoff.sh` and `sigstore-trust-root.sh --send`) send the admin token with every request, and `provider.sh set` also sends a provider's client secret. They did it with a plain curl: the CA and the hostname checked, nothing else. `scripts/verify.sh` was to be run first, but on another connection, so it proved nothing about the one that carried the secret: someone with another valid certificate for `api.permaily.com` (a CA made to issue one, or one from before the enclave's key existed) and a place on the network would have received the token or the secret. The verifier already binds what it reads from `/status` to the key it attested, for the same reason (docs/verification.md).

## Options

1. **Keep "verify first" as an instruction.** Nothing ties the two connections together; the operator's own step is the gap.
2. **Each script verifies, in the same run, and pins the key it attested.** It runs `permaily-verify -json` against the enclave, takes the `tls_spki_sha256` of the host it verified, and sends every request with `curl --pinnedpubkey sha256//<base64>`, the CA and hostname checks kept. A server with another key ends the TLS handshake before curl sends the request.
3. **Send through the verifier itself.** A Go command that attests and then makes the request on the same connection. Stronger by a little (one connection), but every script would change into Go, and the key does not change between connections anyway: it is sealed, kept across restarts, and Certificate Transparency is watched for any other.

## Decision

2. The shared part is `scripts/attested.sh`, sourced by the scripts: `attest` runs the verifier and sets the pin, `enclave_curl` is the only way a script reaches the enclave with the token, and `enclave_error` prints only `error.message` of an answer.

- **Nothing is sent before the enclave verifies.** The scripts need the measurements to verify against: `PERMAILY_PCRS` (the `pcrs.json` of the running release, from `scripts/release-image.sh` or a rebuild) or `PERMAILY_RELEASE` (its tag, which trusts CI as `permaily-verify -release` does). `handoff.sh` verifies against the `pcrs.json` of `<running>`, checked against its build provenance for a tag. Without them, or when the verifier fails on anything, the script stops and says nothing was sent.
- **The verifier** is `PERMAILY_VERIFY`, or the one built from the checkout with go.
- **Dev mode** has no attestation to verify. `PERMAILY_DEV_UNVERIFIED=1` sends without verifying, and is refused unless the enclave's name is `localhost`, `127.0.0.1`, `[::1]`, under `.localhost` or under `.test` (RFC 6761: never in public DNS), so it cannot lead to production.
- **curl** runs with `-q` (no `~/.curlrc`, which could turn a check off), `--proto =https`, `--max-redirs 0` and no `-L`, so a redirect is a failure; plain `http://` is refused before anything runs.
- **What an answer holds is not printed whole** after a request that carried the token or a secret: only fixed fields, without control characters, so an answer that echoes the request does not put it on the terminal.
- **A test** (`scripts/operator-scripts.test.ts`) serves the same name with a second key from the same test CA: no request reaches it. Another fails if a script sends the token without `enclave_curl` and `attest`.

## Consequences

- Each run verifies the enclave first: a few seconds, and network access to the enclave's `/attestation` and `/status`. A check that fails, even one unrelated to the token (the SES state, the KMS key's history), stops the operator's scripts until it is understood. That is on purpose: the token goes only to an enclave that verifies.
- The operator has to say which release runs. During a deployment, `handoff.sh` takes it from its own argument.
- `sigstore-trust-root.sh --send`, called by `handoff.sh`, verifies again against the same measurements: two attestations a handoff.
- Nothing changes in the image or in what the enclave accepts.
