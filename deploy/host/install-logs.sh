#!/bin/bash
# Runs on the parent instance, as root, over SSM (scripts/deploy.sh): installs
# the CloudWatch agent and points it at the journal of permaily-host and
# permaily-enclave, which it ships to the log group `permaily-host` (ADR 0034).
# Takes the agent's configuration (deploy/host/cloudwatch-agent.json) as its
# argument. Running it again changes nothing.
#
# The agent of the Amazon Linux 2023 repository (1.300069) predates journald
# support (1.300070), so the RPM comes from AWS's bucket, pinned by version and
# SHA-256. To update it, download the new version's RPM, check it, and change
# both values together.
set -euo pipefail

config=${1:?usage: install-logs.sh <cloudwatch-agent.json>}
version=1.300074.1b2005
sha256=eef6dc64d6abe70a861d0c1c071477631fc001b004b0a498d4bfad68eb4cae7c
url=https://amazoncloudwatch-agent-us-east-2.s3.us-east-2.amazonaws.com/amazon_linux/arm64/$version/amazon-cloudwatch-agent.rpm
agent=/opt/aws/amazon-cloudwatch-agent

if [[ "$(rpm -q --qf '%{VERSION}' amazon-cloudwatch-agent 2>/dev/null || true)" != "$version" ]]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  curl -fsSL --proto '=https' -o "$tmp/amazon-cloudwatch-agent.rpm" "$url"
  echo "$sha256  $tmp/amazon-cloudwatch-agent.rpm" | sha256sum -c -
  dnf install -y "$tmp/amazon-cloudwatch-agent.rpm"
fi

install -m 0644 "$config" "$agent/etc/permaily.json"
"$agent/bin/amazon-cloudwatch-agent-ctl" -a fetch-config -m ec2 -s -c "file:$agent/etc/permaily.json"
systemctl enable amazon-cloudwatch-agent.service
systemctl is-active amazon-cloudwatch-agent.service
