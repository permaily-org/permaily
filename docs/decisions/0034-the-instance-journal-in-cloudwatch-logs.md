# 0034 Keep the instance's journal in CloudWatch Logs for 90 days

Status: Accepted.
Date: 2026-11-17

## Context

Since ADR 0015 the enclave's log lines reach the parent instance over vsock 8003 and `permaily-host` prints them into the journal, with its own lines. The journal lives on the instance and is lost with it, which is when it is wanted most: an instance replaced after an incident takes the reason with it. The owner decided on 2026-11-17 to keep the lines in CloudWatch Logs for 90 days.

## Options

1. **The host program writes to CloudWatch Logs itself.** Adds the AWS SDK and code to the untrusted host, which is kept to forwarding on purpose.
2. **The CloudWatch agent reads the journal.** No code of ours on the host; the agent reads journald natively since 1.300070 (August 2026). The agent of the Amazon Linux 2023 repository is older (1.300069), so the RPM comes from AWS's bucket.
3. **rsyslog or Fluent Bit to a file, then the agent.** Two programs and a file to rotate for the same result.

## Decision

2.

- The group `permaily-host` (`deploy/tofu/logs.tf`) keeps the lines 90 days, one stream per instance. OpenTofu creates it and sets its retention; the agent's configuration names no retention and the role cannot create groups, so nothing on the instance changes either.
- The agent reads the journal of `permaily-host` and `permaily-enclave` and nothing else (`deploy/host/cloudwatch-agent.json`). Each event is the journal entry as JSON, the line in its `MESSAGE`.
- The instance's role may create streams and add lines in that group (`logs:CreateLogStream`, `logs:PutLogEvents`, `logs:DescribeLogStreams`), and nothing else: it cannot read, delete or change the retention.
- `scripts/deploy.sh` installs the agent on every deploy (`deploy/host/install-logs.sh`), the RPM pinned by version and SHA-256, before it restarts anything; `--logs-only` installs it alone. Not in cloud-init: the instance has `user_data_replace_on_change`, so changing cloud-init would replace the running instance, and every instance gets a deploy before it serves anyway. A failed install warns and does not stop a deploy.

## Consequences

- The lines outlive the instance by 90 days. AWS stores them, as a processor, readable by whoever has CloudWatch Logs read permissions in the account: its administrators and the operators, never the public. They hold kinds only (docs/threat-model.md, "Logging"), so nothing of a person is kept there (docs/data.md, "Logs").
- The host is untrusted and can add lines the enclave never wrote: a line in the group is a clue, not evidence.
- Ingestion and storage are billed by CloudWatch Logs; at today's volume, cents a month.
- Updating the agent is a change of two values in `deploy/host/install-logs.sh`.
