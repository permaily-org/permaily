# deploy/AGENTS.md: infrastructure

OpenTofu for AWS (`us-east-2`, profile `permaily`) and Cloudflare DNS, plus the parent instance setup in `host/`. Procedure: [../docs/deploy.md](../docs/deploy.md).

## Rules

- Always pass `AWS_PROFILE=permaily`; there is no default profile.
- DNS records must stay `proxied = false`. `main.tf` owns the enclave subdomains; `site.tf` owns the public site (apex, `www`, `beta`) on Amplify. Do not touch other records (mail).
- The parent instance is untrusted: no user data, secrets or business logic there. Only nitro-cli, `permaily-host` and SSM.
- Do not open ports beyond what the enclave serves (443 and 25). Declare them inline in `aws_security_group.enclave_host`: a separate rule resource is removed on the next apply.
- Never deploy with `--debug` to a deployment meant to be verified.
- `tofu destroy` removes everything except the mail table (deletion protection) and the mail bucket (not empty); never destroy mail data by accident.
- `mail.tf`: `permaily-verify` reads the live key policy and fails on any statement it does not consider safe. Before adding an action or a statement to the key policy, add it to `pkg/kmspolicy` with a test. Never create grants on the key.
- `mail.tf`: the KMS key policy gives data keys only to allowlisted PCR0s; add a release's PCR0 to `enclave_pcr0_allowlist` before deploying it (docs/operations.md).
- `../enclave.json` is the enclave's runtime configuration, compiled into the image (PCR0). Changing it changes the image.
