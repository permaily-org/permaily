# Deploy

Everything lives in AWS account profile `permaily`, region `us-east-2`, plus DNS records in the Cloudflare zone `permaily.com`.

## Prerequisites

- AWS CLI with the `permaily` profile (`export AWS_PROFILE=permaily`; there is no default profile).
- `CLOUDFLARE_API_TOKEN` with DNS edit permission on the `permaily.com` zone only.
- OpenTofu, Nix (or Docker), jq.

## Infrastructure

```sh
cd deploy/tofu
tofu init
tofu apply
```

Creates a VPC with one public subnet, a security group with only 443 open, an Elastic IP, a `c7g.large` with Nitro Enclaves enabled (Amazon Linux 2023 arm64, SSM access, no SSH), an S3 bucket for deploy artifacts, and in Cloudflare the A and CAA records for `app`, `api`, `mcp` and `inbox`, always DNS only (grey cloud).

Approximate cost: 53 USD/month for the instance plus 3.6 USD/month for the public IPv4.

## Deploying an image

```sh
nix build .#eif --out-link result-eif               # or download a release: permaily.eif, pcrs.json
nix build .#permaily-host --out-link result-host
scripts/deploy.sh result-eif result-host/bin/permaily-host
```

The script uploads both files to S3, then over SSM the instance downloads them, checks their SHA-256, installs them in `/opt/permaily` and restarts `permaily-host` and `permaily-enclave`. On boot, the enclave obtains its certificate with TLS-ALPN-01 (a few seconds). Then verify:

```sh
go run ./cmd/permaily-verify -pcrs result-eif/pcrs.json
```

For tests, deploy `.#eif-staging` first to avoid Let's Encrypt production rate limits (5 duplicate certificates per week), and verify with `-ca-file` as described in [verification.md](verification.md).

## Troubleshooting

`scripts/deploy.sh ... --debug` starts the enclave in debug mode so `nitro-cli console --enclave-name permaily` shows its logs through SSM (`aws ssm start-session --target <instance-id>`). Debug-mode PCRs are zero and verification fails on purpose; redeploy without `--debug` afterwards.

## Public site (Amplify)

The site in `site/` is hosted on AWS Amplify, outside the enclave, from `main`:

1. In the Amplify app, connect the repository `permaily-org/permaily`, branch `main` (optionally `develop` as a preview branch).
2. Mark it as a monorepo with app root `site` (environment variable `AMPLIFY_MONOREPO_APP_ROOT=site`); Amplify reads `amplify.yml` from the repo root.
3. Keep the custom domain `permaily.com` (and `www`) on the app.
4. After the first deploy from this repo, archive `permaily-org/permaily-website`.

`amplify.yml` installs bun, builds, runs the content tests, and sets the security headers. `.github/workflows/site-smoke.yml` checks the live site after each push to `main`.

## Teardown

```sh
cd deploy/tofu && tofu destroy
```

Removes all AWS resources (including the artifacts bucket) and the Cloudflare records.
