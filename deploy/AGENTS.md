# deploy/AGENTS.md: infrastructure

OpenTofu for AWS (`us-east-2`, profile `permaily`; `us-east-1` only for the site's certificate) and Cloudflare DNS, plus the parent instance setup in `host/`. Procedure: [../docs/deploy.md](../docs/deploy.md).

## Rules

- Always pass `AWS_PROFILE=permaily`; there is no default profile.
- The state is in S3, encrypted by OpenTofu (ADR 0014); never keep or commit a state file of `tofu/`. `tofu/bootstrap` is the exception: it only creates the state bucket and its key, and its state is committed.
- `tofu/production.auto.tfvars` says what production runs and is committed; update it in the pull request that follows a release. Never put a secret in it.
- The CAA records of the enclave hosts name the enclave's ACME account (`acme_account_uris`); `permaily-verify` compares them with what the enclave reports. Changing the TLS key or the CA needs the steps in docs/operations.md, or Let's Encrypt refuses every certificate.
- After every `tofu apply`, `tofu plan` must show no changes: a half-applied CAA change stops certificate issuance.
- No certificate for `*.permaily.com`, ever: it would be valid for the enclave hosts with a key outside the enclave (ADR 0012). Keep Cloudflare Universal SSL disabled, keep `issuewild ";"` at the apex, and give the site an exact-name certificate.
- DNS records must stay `proxied = false`. `main.tf` owns the enclave subdomains; `site.tf` owns the public site (apex, `www`, `beta`) on Amplify. Do not touch other records (mail).
- The parent instance is untrusted: no user data, secrets or business logic there. Only nitro-cli, `permaily-host` and SSM.
- Do not open ports beyond what the enclave serves (443 and 25). Declare them inline in `aws_security_group.enclave_host`: a separate rule resource is removed on the next apply.
- Never deploy with `--debug` to a deployment meant to be verified.
- `tofu destroy` removes everything except the mail table (deletion protection) and the mail bucket (not empty); never destroy mail data by accident.
- `mail.tf`: `permaily-verify` reads the live key policy and fails on any statement it does not consider safe. Before adding an action or a statement to the key policy, add it to `pkg/kmspolicy` with a test. Never create grants on the key.
- `mail.tf`: the KMS key policy gives data keys only to allowlisted PCR0s; add a release's PCR0 to `enclave_pcr0_allowlist` before deploying it (docs/operations.md).
- `permaily-verify` checks the MX and `_mta-sts` records. When the MTA-STS policy changes (`mta_sts_mode` in `../enclave.json`), change the `id` of the `_mta-sts` record in `mail.tf` in the same change.
- `../enclave.json` is the enclave's runtime configuration, compiled into the image (PCR0). Changing it changes the image.
