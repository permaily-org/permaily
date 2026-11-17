#!/usr/bin/env bash
# Run by permaily-port25-failed.service when permaily-port25.service fails. A
# failed verification has mailed its report already (alert.sh marks it); this
# mails the runs that failed without that: a crash, systemd stopping a run past
# its limit, or an alert that did not reach the topic. With the unit's journal
# of the last hour, which holds reports, never the token.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
state=${STATE_DIRECTORY:?}
[[ -e $state/alerted ]] && exit 0

{
  echo "permaily-port25: the hourly port 25 check failed without mailing its report (it crashed, systemd stopped it past its limit, or its alert did not reach the topic). No verification result is known for this hour. The unit's journal:"
  echo
  journalctl --unit=permaily-port25.service --since=-70min --lines=100 --no-pager --output=short-iso 2>&1 ||
    echo "(the journal could not be read)"
} | timeout 120 "$here/alert.sh" "Permaily: the port 25 check's run failed"
