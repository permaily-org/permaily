#!/usr/bin/env bash
# While Sigstore's change of who issues signing certificates is not deployed,
# pull requests into develop wait: the one thing to do is to release and
# deploy in time (docs/operations.md, "Sigstore changed"). The pull request
# that compiles in the new trust root goes through, and so does the release
# pull request, which is into main.
#
# The state is an open issue with the label sigstore-urgent, which
# scripts/sigstore-alarm.sh opens and closes. Removing that label from the
# issue lets pull requests through again.
#
#   scripts/check-sigstore-deadline.sh <head branch of the pull request>
#
# Needs gh (with GH_TOKEN).
set -euo pipefail

head=${1:-}
if [[ $head == build/sigstore-trust-root ]]; then
  echo "This is the pull request that compiles in the new trust root."
  exit 0
fi
urgent=$(gh issue list --repo "$GITHUB_REPOSITORY" --label sigstore-urgent --state open --json url --jq '.[0].url // empty')
if [[ -z $urgent ]]; then
  echo "No Sigstore deadline is open."
  exit 0
fi
echo "::error::A release has to be deployed before a Sigstore deadline: $urgent. Pull requests into develop wait until then."
exit 1
