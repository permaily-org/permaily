# 0035 An hourly drift check on Lambda, and the port 25 check from a server

Status: Accepted.
Date: 2026-11-17

## Context

`tofu plan` showed whether production matched `deploy/tofu` only when an operator ran it. Someone with access to the account or the zone could change a record, a role or a key's alias, and nothing would say so until the next apply. The owner decided on 2026-11-17 to run the plan every hour and mail like the other alarms. The same decision moved the check of the enclave's SMTP certificate (`permaily-verify -require-smtp`) to a server: AWS blocks outbound port 25 from Lambda and from EC2 by default, and GitHub's runners cannot use it.

## Options

1. **A GitHub workflow.** It would need AWS credentials for the state and every resource, kept as repository secrets: the account's organization forbids GitHub's identity provider (`alarms.tf`), so they would be long-lived keys, and the schedule is unreliable (docs/operations.md, "Scheduled verification").
2. **Lambda, with a container image.** A role instead of keys, EventBridge's schedule, CloudWatch for its own failures. OpenTofu is not a Lambda runtime, so the image carries it.
3. **The owner's server.** Long-lived AWS keys on a machine outside the account.

## Decision

2 for the plan; the owner's Contabo server for port 25, which needs no AWS credentials.

- **The image** (`deploy/drift`): AWS's `provided:al2023` base and OpenTofu's binary from its official `-minimal` image, both pinned by digest, and two shell scripts: a custom runtime and the check. The build downloads nothing else. It lives in the ECR repository `permaily-drift` (immutable tags); the function names it by digest (`drift_image_digest`). Only untagged images expire: a count of images would sooner or later expire the one the function runs, so old tagged images are removed by hand, never that one.
- **What it plans**: `deploy/` at the newest commit of `develop`, fetched on each run from GitHub, with the providers its lock file names, downloaded and checked against the lock's hashes. So the image changes only with OpenTofu or the scripts, and the check follows the configuration without a rebuild. `develop` changes only through reviewed pull requests; the code fetched runs with the role below and nothing more.
- **Read-only, and narrow.** The role reads the state object and decrypts it with the state's key (and asks it for a data key, which OpenTofu's key provider does on every run and which writes nothing); plans with `-lock=false`, so it never writes the lock file; and refreshes with read actions listed by service, configuration and metadata only: no object but the state, no DynamoDB item, no SES suppression list, no secret but its token. `s3:ListBucket`, which `HeadBucket` needs, also lists object keys. The Cloudflare token is a separate one with Zone Read and DNS Read, put by the operator into Secrets Manager; it is in neither the repository nor the state. Nothing in the image applies, and the role could not.
- **Mail**: the SNS topic of the Sigstore alarm, whose subscribers are the maintainers, when the plan is not clean or fails. A clean plan sends nothing. CloudWatch alarms on the function's errors and on missing invocations mail the same topic when the check cannot.
- **Port 25**: `scripts/verify-cron.sh`, by cron hourly on the owner's server, runs `scripts/verify-production.sh -require-smtp`: the release's own verifier, checked against its Sigstore provenance and never run when that fails. Each run has a deadline under the hour, after which it and its children are stopped and it fails; a lock held past the deadline also fails, so a run that hangs cannot silence the following ones. It prints and exits 1 on failure, for cron's mail, and runs an alert command when one is set, after releasing the lock and within a deadline of its own, so a hung mailer cannot hold back the next run either.

## Consequences

- A change made outside OpenTofu is mailed within the hour, and so is the window between a release's apply and the pull request that commits it: merge that pull request promptly.
- The role can decrypt the state, which holds what the state holds: among others the GitHub dispatch token (`verify.tf`) and the Amplify webhook's address. That is what any operator who plans can read; the function adds no reach beyond it, and its log never prints a sensitive value (OpenTofu masks them).
- Adding a resource type to `deploy/tofu` needs its read actions in `drift_refresh`, or the next run fails and mails.
- Cost: an hourly run of a minute or two at 2 GiB, inside Lambda's free tier, and an image of about 100 MB in ECR.
- The port 25 check depends on the owner's server and its provider's policy on port 25; it trusts the server as far as alarms go, and nothing it does is sent anywhere but to its operator. Nothing mails when the server or its cron stops.
