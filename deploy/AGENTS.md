# deploy/AGENTS.md: infrastructure

OpenTofu for AWS (`us-east-2`, profile `permaily`) and Cloudflare DNS, plus the parent instance setup in `host/`. Procedure: [../docs/deploy.md](../docs/deploy.md).

## Rules

- Always pass `AWS_PROFILE=permaily`; there is no default profile.
- DNS records must stay `proxied = false`. `main.tf` owns the enclave subdomains; `site.tf` owns the public site (apex, `www`, `beta`) on Amplify. Do not touch other records (mail).
- The parent instance is untrusted: no user data, secrets or business logic there. Only nitro-cli, `permaily-host` and SSM.
- Do not open ports beyond what the enclave serves (443 in F0).
- Never deploy with `--debug` to a deployment meant to be verified.
- `tofu destroy` removes everything, including the artifacts bucket.
