#!/usr/bin/env bash
# The hourly verification with port 25 required. GitHub's runners and Lambda
# cannot connect out on port 25, so the scheduled checks elsewhere skip the
# SMTP certificate; this one fails without it. It runs on an instance of its
# own in the account, once AWS has lifted the account's block of outbound port
# 25: deploy/port25/run.sh, by a systemd timer at minute 41 (ADR 0038,
# docs/operations.md, "Port 25, from its instance"). Any machine that can
# reach port 25 can run it too, by cron or a timer.
#
# It runs scripts/verify-production.sh -require-smtp: the latest release's
# permaily-verify and pcrs.json, downloaded and checked against their Sigstore
# provenance (gh attestation verify) before the verifier runs, and the
# previous release's while the latest is not deployed. It uses no AWS credentials.
#
# A run has a deadline, PERMAILY_VERIFY_TIMEOUT seconds (default 3000, under
# the hour): past it the run and every process it started are sent TERM, then
# KILL PERMAILY_VERIFY_GRACE seconds later (default 30), and it is a failure.
# One run at a time: while a run within its deadline holds the lock, the next
# one leaves it be; a lock held past the deadline, or one that cannot be taken,
# is a failure.
#
# On success it prints nothing. On failure it prints the report and exits 1,
# and when PERMAILY_ALERT_CMD is set it runs it with the report on standard
# input (on the instance, deploy/port25/alert.sh, which publishes to SNS).
# The lock is released before the alert command runs, which never holds it,
# and that command has its own deadline, PERMAILY_ALERT_TIMEOUT seconds
# (default 120): past it, it and what it started are sent TERM, then KILL
# PERMAILY_VERIFY_GRACE seconds later. An alert that fails or times out is
# reported on standard error; the exit is 1 either way.
#
# Needs bash, gh (signed in for this user, or GH_TOKEN) and flock. The last
# report is kept in $XDG_STATE_HOME/permaily-verify-cron/last.txt
# (~/.local/state by default).
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
state=${XDG_STATE_HOME:-$HOME/.local/state}/permaily-verify-cron
limit=${PERMAILY_VERIFY_TIMEOUT:-3000}
grace=${PERMAILY_VERIFY_GRACE:-30}
alert_limit=${PERMAILY_ALERT_TIMEOUT:-120}
report=$state/last.txt

# supervise PID LIMIT GRACE waits for PID, the leader of a process group of its
# own; past LIMIT seconds it sends the group TERM, then KILL GRACE seconds
# later. It returns PID's exit status, or 124 when PID was stopped.
supervise() {
  local pid=$1 end=$((SECONDS + $2))
  while kill -0 "$pid" 2>/dev/null && ((SECONDS < end)); do sleep 1; done
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM -- "-$pid" 2>/dev/null
    end=$((SECONDS + $3))
    while kill -0 "$pid" 2>/dev/null && ((SECONDS < end)); do sleep 1; done
    kill -KILL -- "-$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
    return 124
  fi
  wait "$pid"
}

# alert TEXT prints the report, releases the lock, runs the alert command with
# the report on standard input within its deadline, and exits 1. Never in a
# pipeline: its exit would leave only the pipeline's subshell.
alert() {
  printf '%s\n' "$1"
  exec 9>&-
  if [[ -n ${PERMAILY_ALERT_CMD:-} ]]; then
    set -m
    bash -c "$PERMAILY_ALERT_CMD" <<<"$1" &
    local pid=$!
    set +m
    supervise "$pid" "$alert_limit" "$grace"
    case $? in
      0) ;;
      124) echo "permaily-verify-cron: the alert command did not finish within $alert_limit s and was stopped" >&2 ;;
      *) echo "permaily-verify-cron: the alert command failed" >&2 ;;
    esac
  fi
  exit 1
}

if ! [[ $limit =~ ^[1-9][0-9]*$ && $grace =~ ^[1-9][0-9]*$ && $alert_limit =~ ^[1-9][0-9]*$ ]]; then
  [[ $grace =~ ^[1-9][0-9]*$ ]] || grace=30
  [[ $alert_limit =~ ^[1-9][0-9]*$ ]] || alert_limit=120
  alert "permaily-verify-cron: PERMAILY_VERIFY_TIMEOUT, PERMAILY_VERIFY_GRACE and PERMAILY_ALERT_TIMEOUT must be whole seconds"
fi
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
supervise "$run" "$limit" "$grace"
case $? in
  0) exit 0 ;;
  124) alert "$(cat "$report")

RESULT: the verification did not finish within $limit s and was stopped." ;;
  *) alert "$(cat "$report")" ;;
esac
