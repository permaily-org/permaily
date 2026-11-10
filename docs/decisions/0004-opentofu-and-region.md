# 0004 OpenTofu, AWS us-east-2, Cloudflare DNS

Status: Accepted.
Date: 2026-11-09

## Context

The infrastructure is small (one Graviton instance with Nitro Enclaves, an EIP, an S3 bucket, IAM, DNS records) and spans AWS and Cloudflare. The AWS account is a project in the new AWS experience ("Blank Slate"): its region is fixed by AWS to us-east-2 and an organization policy denies other regions; its Free Tier plan only allows free-tier instance types, none of which supports Nitro Enclaves.

## Options

- Tool: OpenTofu, Pulumi (Go), AWS CDK (AWS only), CDK for Terraform (archived by HashiCorp in December 2025).
- Region: us-east-2 now, or activate the account's advanced features to choose us-east-1.

## Decision

OpenTofu (declarative and easy to audit, provider hashes pinned in `.terraform.lock.hcl`, neutral governance), us-east-2, and DNS in Cloudflare (DNS only, never proxied).

Verification does not depend on the infrastructure tool: the parent instance is untrusted, and what is verified is the enclave image.

## Consequences

- The project owner must switch the AWS account to the Paid Plan before the instance can be created (done; the instance exists since 2026-11-10).
- Local OpenTofu state for now; move it to S3 with state encryption before more than one person deploys.
