# Deploy

Everything lives in AWS account profile `permaily`, region `us-east-2` (plus the public site's certificate, in `us-east-1`), and DNS records in the Cloudflare zone `permaily.com`.

The Cloudflare zone must keep **Universal SSL disabled** (SSL/TLS > Edge Certificates) and every record DNS only: with it enabled Cloudflare issues `*.permaily.com` certificates, valid for the enclave hosts, with keys it holds (ADR 0012). It is a dashboard setting; the OpenTofu token cannot change it.

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

Creates a VPC with one public subnet, a security group with only 443 and 25 open, an Elastic IP, a `c7g.large` with Nitro Enclaves enabled (Amazon Linux 2023 arm64, SSM access, no SSH), an S3 bucket for deploy artifacts, and in Cloudflare the A and CAA records for `app`, `api`, `mcp`, `inbox` and `mta-sts.inbox`, always DNS only (grey cloud), plus the MX, MTA-STS and TLS-RPT records of `inbox`.

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

The site in `site/` is hosted on AWS Amplify, outside the enclave, managed by OpenTofu (`deploy/tofu/site.tf`, app `permaily-site` in us-east-2):

| Branch | URL | Notes |
|---|---|---|
| `main` | `permaily.com`, `www.permaily.com` | Production, deployed on every release |
| `develop` | `beta.permaily.com` | Every push to `develop` |
| Pull requests into `develop` | `pr-<n>.<app id>.amplifyapp.com` | Built only when `site/` changes (`AMPLIFY_DIFF_DEPLOY`); a change only in `packages/ui/` or `amplify.yml` is still checked by the `site` job in CI |

- The build is `amplify.yml` at the repo root (monorepo, app root `site`); security headers are set on the app (`custom_headers` in `site.tf`), because Amplify ignores them in `amplify.yml` for monorepo apps.
- Repository access: the Amplify GitHub App for us-east-2 is installed on `permaily-org`, limited to this repository. Creating the app once needed a GitHub token with `admin:repo_hook` (`TF_VAR_github_access_token`); Amplify does not store it and later applies run without it.
- Certificate: an ACM certificate for exactly `permaily.com`, `www.` and `beta.` (`aws_acm_certificate.site`, in `us-east-1`, which is where CloudFront takes certificates from), not Amplify's managed one, which also covers `*.permaily.com`.
- DNS in Cloudflare (DNS only): CNAMEs for the apex, `www` and `beta`, the three ACM validation records, and CAA at the apex: `issue` for Amazon and `issuewild ";"`, so nobody can issue a wildcard for the domain. The enclave subdomains keep their own CAA (`letsencrypt.org`).
- The footer shows the latest release's PCR0, read at build time. A release is published after `main` was built, so `release.yml` calls an Amplify webhook (`aws_amplify_webhook.release`, URL in the `AMPLIFY_RELEASE_WEBHOOK` secret) to rebuild `main` once `pcrs.json` exists.
- `.github/workflows/site-smoke.yml` checks the live site after each push to `main`.

## Teardown

```sh
cd deploy/tofu && tofu destroy
```

Removes all AWS resources (including the artifacts bucket) and the Cloudflare records.
