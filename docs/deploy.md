# Deploy

Everything lives in AWS account profile `permaily`, region `us-east-2` (plus the public site's certificate, in `us-east-1`), and DNS records in the Cloudflare zone `permaily.com`.

The Cloudflare zone must keep **Universal SSL disabled** (SSL/TLS > Edge Certificates) and every record DNS only: with it enabled Cloudflare issues `*.permaily.com` certificates, valid for the enclave hosts, with keys it holds (ADR 0012). It is a dashboard setting; the OpenTofu token cannot change it.

## Prerequisites

- AWS CLI with the `permaily` profile (`export AWS_PROFILE=permaily`; there is no default profile).
- `CLOUDFLARE_API_TOKEN` with DNS edit permission on the `permaily.com` zone only.
- For the first apply only, `TF_VAR_github_dispatch_token`: a fine-grained GitHub token for this repository with Actions read and write, which EventBridge uses to start the scheduled verification (docs/operations.md).
- OpenTofu, Nix (or Docker), jq.

## Infrastructure

```sh
cd deploy/tofu
tofu init
tofu apply
```

The state is in S3 (`permaily-tofu-state-<account>`, versioned, with a lock file so two applies cannot run at once) and OpenTofu encrypts it with the KMS key `alias/permaily-tofu-state` before uploading, so the bucket holds only ciphertext (ADR 0014). Operators need S3 access to that bucket and `kms:GenerateDataKey` and `kms:Decrypt` on that key. The bucket and the key come from `deploy/tofu/bootstrap`, applied once; its small state has no secrets and is committed.

What production runs is in `deploy/tofu/production.auto.tfvars` (the releases that have a KMS key, by PCR0) and in the defaults of `variables.tf` (the ACME account in the CAA records). Both are public values. That file overrides a local `terraform.tfvars`; for a test, edit it without committing or pass `-var`.

Creates a VPC with one public subnet, a security group with only 443 and 25 open, an Elastic IP, a `c7g.large` with Nitro Enclaves enabled (Amazon Linux 2023 arm64, SSM access, no SSH), an S3 bucket for deploy artifacts, and in Cloudflare the A and CAA records for `app`, `api`, `mcp`, `inbox` and `mta-sts.inbox`, always DNS only (grey cloud), plus the MX, MTA-STS and TLS-RPT records of `inbox`. It creates two buckets with Object Lock for the commits of the records that cannot go back (ADR 0016), one for production and one for staging images; the first cannot be deleted once the enclave has written to it. It also creates the EventBridge rules that start the `verify-production` and `sigstore-watch` workflows, and the SNS topic that tells the maintainers about a Sigstore deadline (docs/operations.md).

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
