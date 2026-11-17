#!/usr/bin/env bash
# Mails standard input, a failed run's report, to the subscribers of the
# alarms' SNS topic (PERMAILY_TOPIC_ARN), and marks the run as alerted
# ($STATE_DIRECTORY/alerted) once the topic took it. Its callers give it a
# deadline: verify-cron.sh its alert deadline, run.sh and on-failure.sh 120 s.
#
#   alert.sh [subject]
set -uo pipefail

state=${STATE_DIRECTORY:?}
subject=${1:-Permaily: the port 25 check failed}
message=$(mktemp "$state/alert.XXXXXX") || exit 1
trap 'rm -f "$message"' EXIT

# SNS takes up to 256 KB; the end of a report says what failed.
tail -c 200000 >"$message"
[[ -s $message ]] || echo "permaily-port25: a run failed without a report" >"$message"

aws sns publish --topic-arn "$PERMAILY_TOPIC_ARN" --subject "$subject" --message "file://$message" \
  --cli-connect-timeout 10 --cli-read-timeout 30 --output text --query MessageId >/dev/null || exit 1
touch "$state/alerted"
