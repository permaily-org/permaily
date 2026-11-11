#!/usr/bin/env bash
# Tells the maintainers that the workflow sigstore-watch failed, or that it
# passes again. Run by that workflow's last job.
#
# A failure there is a deadline (docs/operations.md, "Sigstore changed"): the
# running enclave will stop accepting releases at some point unless someone
# acts. So it must not be one more red mark in a list. While any check fails
# there is one open issue with the label sigstore-deadline, assigned to the
# logins in .github/sigstore-maintainers, and every daily run adds a comment
# that names them again. When everything passes, the issue is closed.
#
# Environment:
#   TRUST_ROOT, CANARY, CANARY_REKOR2   result of each job (success, failure...)
#   URGENT        "true" when the newer trust root changes who issues certificates
#   PULL_REQUEST  URL of the pull request that compiles in the newer trust root
#   RUN_URL       URL of the workflow run
#
# Needs gh (with GH_TOKEN) and jq.
set -euo pipefail

cd "$(dirname "$0")/.."
label=sigstore-deadline
runbook="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/blob/develop/docs/operations.md#sigstore-changed"
open=$(gh issue list --repo "$GITHUB_REPOSITORY" --label "$label" --state open --json number --jq '.[0].number // empty')

failed=()
[[ ${TRUST_ROOT:-} == success ]] || failed+=(trust-root)
[[ ${CANARY:-} == success ]] || failed+=(canary)
[[ ${CANARY_REKOR2:-} == success ]] || failed+=(canary-rekor2)

if [[ ${#failed[@]} -eq 0 ]]; then
  if [[ -n $open ]]; then
    gh issue close "$open" --repo "$GITHUB_REPOSITORY" --comment "Every check of sigstore-watch passes again: $RUN_URL"
  fi
  exit 0
fi

mapfile -t maintainers < <(grep -vE '^\s*(#|$)' .github/sigstore-maintainers)
mentions=$(printf '@%s ' "${maintainers[@]}")
assignees=$(IFS=,; echo "${maintainers[*]}")

{
  echo "$mentions"
  echo
  echo "The workflow \`sigstore-watch\` failed: ${failed[*]}. Run: $RUN_URL"
  echo
  echo "This is a deadline, not an incident: production keeps working. If nobody acts, the running enclave will at some point refuse every new release, and then it cannot be upgraded without losing what it stores."
  echo
  for job in "${failed[@]}"; do
    case $job in
      trust-root)
        if [[ ${URGENT:-} == true ]]; then
          echo "- **trust-root, urgent**: Sigstore's new trust root changes who issues signing certificates. A running enclave does not take that from an update: a release with the new trust root has to be published **and deployed** while the old issuer still signs."
        else
          echo "- **trust-root**: Sigstore published a newer trust root, or one \`pkg/provenance\` refuses (the run says which). For a new log or timestamp authority, merging the pull request is enough: the running enclave is given the new trust root before each handoff."
        fi
        [[ -z ${PULL_REQUEST:-} ]] || echo "  Pull request that compiles it in: $PULL_REQUEST"
        ;;
      canary)
        echo "- **canary**: \`pkg/provenance\` refuses what GitHub attests today. Do not merge into \`main\` until it is fixed and a release with the fix is deployed."
        ;;
      canary-rekor2)
        echo "- **canary-rekor2**: a signature recorded today in Sigstore's Rekor v2 log is refused, or could not be made. Read which step failed: the log being down is not the alarm."
        ;;
    esac
  done
  echo
  echo "What to do: $runbook"
} > "${RUNNER_TEMP:-/tmp}/sigstore-alarm.md"

title="Sigstore changed: the running enclave needs attention before a deadline"
[[ ${URGENT:-} != true ]] || title="URGENT: Sigstore changed who issues certificates; release and deploy in time"
if [[ -z $open ]]; then
  url=$(gh issue create --repo "$GITHUB_REPOSITORY" --title "$title" --label "$label" --assignee "$assignees" --body-file "${RUNNER_TEMP:-/tmp}/sigstore-alarm.md")
else
  url=$(gh issue comment "$open" --repo "$GITHUB_REPOSITORY" --body-file "${RUNNER_TEMP:-/tmp}/sigstore-alarm.md")
  [[ ${URGENT:-} != true ]] || gh issue edit "$open" --repo "$GITHUB_REPOSITORY" --title "$title" >/dev/null
fi
echo "Told the maintainers: $url"
