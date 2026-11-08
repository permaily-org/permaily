---
title: Self-hosting
description: Run the same public code in your own AWS account, and what that makes you responsible for.
---

Run the same image in your own AWS account. You hold the keys, and you check the running build with the same command everyone else uses.

## What you run

The image build and Terraform are in `infra/` in the [public repository](https://github.com/permaily-org/permaily).

| Part | What it does |
| --- | --- |
| **Network load balancer** | Passes TCP straight through, with no TLS termination, so it never sees plaintext |
| **EC2 parent instance** | Carries encrypted bytes to the enclave, and is treated as untrusted |
| **Nitro Enclave** | Runs everything that can see plaintext mail: TLS, the SMTP receiver, provider clients, the rule engine, the OAuth server, the MCP server, the REST API, webhooks and the web app |
| **AWS KMS** | Releases keys only to approved measurements |
| **Postgres and S3** | Store ciphertext only |

## What you take on

You're the operator now, so what the [trust model](/docs/trust-model/) says about the operator applies to you.

- **The keys.** Pin the KMS policy to the image measurement, PCR0, not the signing certificate. A signing certificate would mean trusting whoever signs.
- **The account.** Anyone who controls your AWS account can change the key policy. CloudTrail records it, so watch those records.
- **Upgrades.** Every release changes the measurement. Check that a new one is in the public release log before it receives keys.
- **AWS.** It stays in the trusted base, as it does on the hosted service.

## Connect your mailboxes

Bring your own Google or Microsoft app registration, so the connection belongs to your organisation.

- **Google Workspace.** Register an internal app in your own Workspace and point it at your deployment. Google's [user data policy](https://developers.google.com/terms/api-services-user-data-policy) says its requirements don't apply to an app used only within your own domain. Have your legal team confirm how that applies to you.
- **Microsoft 365.** [RBAC for Applications](https://learn.microsoft.com/en-us/exchange/permissions-exo/application-rbac) in Exchange Online lets an admin grant an app access to a chosen set of mailboxes. Scoped roles add to any unscoped grant in Entra ID, so remove any unscoped grant first. One left in place cancels the scoping.

:::note[Reading employee mail has legal limits]
Many countries restrict it, and works councils may have a say. Permaily narrows access and keeps a record, and it doesn't replace legal advice.
:::

## Check your deployment

It's the same image, so the same command compares it against the same public release log.

```bash
permaily verify permaily.example.com
```

Never run production in debug mode. AWS zeroes the measurements in debug mode, so the check rejects them. See [Verifying the build](/docs/verifying-the-build/).

## Cost

Companies pay to self-host, so [talk to us](/contact/) about a plan. You also pay AWS for what you run, and Nitro Enclaves add no charge beyond EC2 and related services.
