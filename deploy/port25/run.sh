#!/usr/bin/env bash
# The hourly port 25 check on its own instance (deploy/tofu/port25.tf, ADR 0038),
# started by permaily-port25.timer as the user permaily-port25. It reads the
# GitHub token from Secrets Manager into this run's environment, never onto a
# command line or a disk, and hands over to verify-cron.sh: the latest
# release's permaily-verify and pcrs.json, checked against their provenance
# before anything of them runs, then `permaily-verify -require-smtp`, within
# its deadline. A failure goes to the alarms' SNS topic through alert.sh.
#
# Its exit is verify-cron.sh's, or 1 when the token cannot be read. When the
# unit fails, permaily-port25-failed.service (on-failure.sh) mails as well,
# unless this run's alert already reached the topic.
#
# Environment (/etc/permaily-port25.env and the unit): PERMAILY_TOPIC_ARN,
# PERMAILY_TOKEN_SECRET, AWS_REGION, STATE_DIRECTORY.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
state=${STATE_DIRECTORY:?}
rm -f "$state/alerted"

# fail TEXT mails TEXT and exits 1; nothing of a release runs.
fail() {
  printf '%s\n' "$1"
  printf '%s\n' "$1" | timeout 120 "$here/alert.sh" || echo "permaily-port25: the alert could not be sent" >&2
  exit 1
}

token=$(timeout 60 aws secretsmanager get-secret-value --secret-id "$PERMAILY_TOKEN_SECRET" \
  --query SecretString --output text --cli-connect-timeout 10 --cli-read-timeout 30) ||
  fail "permaily-port25: the GitHub token could not be read from Secrets Manager ($PERMAILY_TOKEN_SECRET); no verification ran (docs/operations.md, \"Port 25, from its instance\")"
[[ $token =~ ^github_pat_[A-Za-z0-9_]+$ ]] ||
  fail "permaily-port25: the secret $PERMAILY_TOKEN_SECRET does not hold a fine-grained GitHub token (github_pat_...); no verification ran (docs/operations.md, \"Port 25, from its instance\")"

export GH_TOKEN=$token GH_PROMPT_DISABLED=1 GH_NO_UPDATE_NOTIFIER=1 XDG_STATE_HOME=$state
unset token
PERMAILY_ALERT_CMD="exec $(printf %q "$here/alert.sh")"
export PERMAILY_ALERT_CMD
exec "$here/verify-cron.sh"
