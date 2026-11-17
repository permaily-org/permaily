#!/usr/bin/env bash
# The hourly verification with port 25 required, for cron on a server whose
# provider lets it connect out on port 25 (the owner's Contabo server). AWS
# blocks outbound 25 from Lambda and EC2, and GitHub's runners cannot reach it
# either, so the scheduled checks elsewhere skip the SMTP certificate; this one
# fails without it (docs/operations.md, "Port 25, from a server").
#
#   41 * * * * /opt/permaily/scripts/verify-cron.sh
#
# It runs scripts/verify-production.sh -require-smtp: the latest release's
# permaily-verify and pcrs.json, downloaded and checked against their Sigstore
# provenance (gh attestation verify) before the verifier runs, and the
# previous release's while the latest is not deployed. No AWS credentials.
#
# A run has a deadline, PERMAILY_VERIFY_TIMEOUT seconds (default 3000, under
# the hour): past it the run and every process it started are sent TERM, then
# KILL PERMAILY_VERIFY_GRACE seconds later (default 30), and it is a failure.
# One run at a time: while a run within its deadline holds the lock, the next
# one leaves it be; a lock held past the deadline, or one that cannot be taken,
# is a failure.
#
# On success it prints nothing. On failure it prints the report and exits 1
# (cron mails it where MAILTO and a mailer are set), and when
# PERMAILY_ALERT_CMD is set it runs it with the report on standard input, for
# example: PERMAILY_ALERT_CMD='mail -s "Permaily: verification failed" ops@example.org'
#
# Needs bash, gh (signed in for this user, or GH_TOKEN), jq and flock. The last
# report is kept in ~/.local/state/permaily-verify-cron/last.txt.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
state=${XDG_STATE_HOME:-$HOME/.local/state}/permaily-verify-cron
limit=${PERMAILY_VERIFY_TIMEOUT:-3000}
grace=${PERMAILY_VERIFY_GRACE:-30}
report=$state/last.txt

# alert TEXT prints the report, runs the alert command with it on standard
# input, and exits 1. Never in a pipeline: its exit would leave only the
# pipeline's subshell.
alert() {
  printf '%s\n' "$1"
  if [[ -n ${PERMAILY_ALERT_CMD:-} ]]; then
    printf '%s\n' "$1" | bash -c "$PERMAILY_ALERT_CMD" || echo "permaily-verify-cron: the alert command failed" >&2
  fi
  exit 1
}

[[ $limit =~ ^[1-9][0-9]*$ && $grace =~ ^[1-9][0-9]*$ ]] ||
  alert "permaily-verify-cron: PERMAILY_VERIFY_TIMEOUT and PERMAILY_VERIFY_GRACE must be whole seconds"
mkdir -p "$state" || alert "permaily-verify-cron: cannot create $state"

# flock -n exits 75 (-E) only when another process holds the lock.
{ exec 9>"$state/lock"; } 2>/dev/null || alert "permaily-verify-cron: cannot open the lock $state/lock"
flock -n -E 75 9
case $? in
  0) ;;
  75)
    started=$(cat "$state/started" 2>/dev/null)
    if [[ $started =~ ^[0-9]+$ ]] && (($(date +%s) - started <= limit + grace + 60)); then
      echo "permaily-verify-cron: the previous run is still going" >&2
      exit 0
    fi
    alert "permaily-verify-cron: a run that started at $(date -u -d "@${started:-0}" 2>/dev/null || echo "an unknown time") still holds the lock, past its deadline of $limit s; no verification is running"
    ;;
  *) alert "permaily-verify-cron: cannot take the lock $state/lock" ;;
esac
date +%s >"$state/started"

# The run in a process group of its own (set -m), so that the deadline reaches
# every process it started.
set -m
"$here/verify-production.sh" -require-smtp >"$report" 2>&1 &
run=$!
set +m
deadline=$((SECONDS + limit))
while kill -0 "$run" 2>/dev/null && ((SECONDS < deadline)); do sleep 1; done
if kill -0 "$run" 2>/dev/null; then
  kill -TERM -- "-$run" 2>/dev/null
  deadline=$((SECONDS + grace))
  while kill -0 "$run" 2>/dev/null && ((SECONDS < deadline)); do sleep 1; done
  kill -KILL -- "-$run" 2>/dev/null
  wait "$run" 2>/dev/null
  alert "$(cat "$report")

RESULT: the verification did not finish within $limit s and was stopped."
fi
wait "$run" && exit 0
alert "$(cat "$report")"
