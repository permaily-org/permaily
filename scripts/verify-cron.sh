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
# provenance (gh attestation verify), and the previous release's while the
# latest is not deployed. No AWS credentials.
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
mkdir -p "$state"

# One run at a time: a run that outlasts the hour is not started twice.
exec 9>"$state/lock"
flock -n 9 || { echo "permaily-verify-cron: the previous run is still going" >&2; exit 0; }

report=$state/last.txt
if "$here/verify-production.sh" -require-smtp >"$report" 2>&1; then
  exit 0
fi

cat "$report"
if [[ -n ${PERMAILY_ALERT_CMD:-} ]]; then
  bash -c "$PERMAILY_ALERT_CMD" <"$report" || echo "permaily-verify-cron: the alert command failed" >&2
fi
exit 1
