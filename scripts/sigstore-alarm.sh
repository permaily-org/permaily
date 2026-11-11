#!/usr/bin/env bash
# Tells the maintainers that the workflow sigstore-watch failed, or that it
# passes again. Run by that workflow's last job.
#
# A failure there is a deadline (docs/operations.md, "Sigstore changed"): the
# running enclave will stop accepting releases at some point unless someone
# acts. So it must not be one more red mark in a list. While any check fails
# there is one open issue with the label sigstore-deadline, assigned to the
# logins in .github/sigstore-maintainers, and every daily run adds a comment
# that names them again and sends mail to the subscribers of an SNS topic.
# When everything passes, the issue is closed.
#
# When the change is urgent (a new issuer of signing certificates) the issue
# also gets the label sigstore-urgent, which makes pull requests into develop
# wait (scripts/check-sigstore-deadline.sh), and it stays open after the
# checks pass: until production runs a release with the new trust root
# compiled in, which is what the deadline is about.
#
# Environment:
#   TRUST_ROOT, CANARY, CANARY_REKOR2   result of each job (success, failure...)
#   URGENT        "true" when the newer trust root changes who issues certificates
#   PULL_REQUEST  URL of the pull request that compiles in the newer trust root
#   RUN_URL       URL of the workflow run
#   SIGSTORE_ALARM_TOPIC_ARN, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION
#                 optional: the SNS topic to send mail through
#   PERMAILY_API  base URL of production (default https://api.permaily.com)
#
# Needs gh (with GH_TOKEN), jq and curl, and the aws CLI to send mail.
set -euo pipefail

cd "$(dirname "$0")/.."
label=sigstore-deadline
urgent_label=sigstore-urgent
runbook="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/blob/develop/docs/operations.md#sigstore-changed"
issue=$(gh issue list --repo "$GITHUB_REPOSITORY" --label "$label" --state open --json number,labels --jq '.[0] // {}')
open=$(jq -r '.number // empty' <<<"$issue")
was_urgent=$(jq -r --arg l "$urgent_label" 'any(.labels[]?; .name == $l)' <<<"$issue")

failed=()
[[ ${TRUST_ROOT:-} == success ]] || failed+=(trust-root)
[[ ${CANARY:-} == success ]] || failed+=(canary)
[[ ${CANARY_REKOR2:-} == success ]] || failed+=(canary-rekor2)

# mail SUBJECT BODY: to the subscribers of the topic, when there is one.
mail() {
  [[ -n ${SIGSTORE_ALARM_TOPIC_ARN:-} && -n ${AWS_ACCESS_KEY_ID:-} ]] || return 0
  aws sns publish --topic-arn "$SIGSTORE_ALARM_TOPIC_ARN" --subject "$1" --message "$2" >/dev/null ||
    echo "::warning::could not send mail through SNS"
}

text=${RUNNER_TEMP:-/tmp}/sigstore-alarm.md

if [[ ${#failed[@]} -eq 0 ]]; then
  [[ -n $open ]] || exit 0
  if [[ $was_urgent == true ]]; then
    # The checks pass because develop has the new trust root. The deadline is
    # met when production runs a release that has it compiled in.
    want=$(jq -r '.signed.version' pkg/provenance/sigstore_tuf_targets.json)
    running=$(curl -sS --max-time 20 "${PERMAILY_API:-https://api.permaily.com}/status" | jq -r 'if .sigstore and (.sigstore.updated_at | not) then .sigstore.tuf_targets_version else 0 end' 2>/dev/null || echo 0)
    if ((running < want)); then
      {
        echo "The checks of sigstore-watch pass again, so \`develop\` has the new trust root. **This stays open**: production runs a release without it (TUF targets ${running}, published ${want})."
        echo
        echo "Still to do: release, and deploy with the handoff, while the old issuer of certificates still signs. $runbook"
        echo
        echo "Pull requests into \`develop\` wait meanwhile; removing the label \`$urgent_label\` from this issue lets them through."
      } > "$text"
      url=$(gh issue comment "$open" --repo "$GITHUB_REPOSITORY" --body-file "$text")
      mail "URGENT Permaily: release and deploy before the Sigstore deadline" "$(cat "$text")"$'\n\n'"$url"
      exit 0
    fi
  fi
  gh issue close "$open" --repo "$GITHUB_REPOSITORY" --comment "Every check of sigstore-watch passes again: $RUN_URL"
  mail "Permaily: the Sigstore checks pass again" "Every check of sigstore-watch passes again, and the issue is closed. $RUN_URL"
  exit 0
fi

mapfile -t maintainers < <(grep -vE '^\s*(#|$)' .github/sigstore-maintainers)
mentions=$(printf '@%s ' "${maintainers[@]}")
assignees=$(IFS=,; echo "${maintainers[*]}")

{
  echo "The workflow \`sigstore-watch\` failed: ${failed[*]}. Run: $RUN_URL"
  echo
  echo "This is a deadline, not an incident: production keeps working. If nobody acts, the running enclave will at some point refuse every new release, and then it cannot be upgraded without losing what it stores."
  echo
  for job in "${failed[@]}"; do
    case $job in
      trust-root)
        if [[ ${URGENT:-} == true ]]; then
          echo "- **trust-root, urgent**: Sigstore's new trust root changes who issues signing certificates. A running enclave does not take that from an update: a release with the new trust root has to be published **and deployed** while the old issuer still signs. Pull requests into \`develop\` wait until then, except the one below."
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
} > "$text"

title="Sigstore changed: the running enclave needs attention before a deadline"
subject="Permaily: Sigstore changed, act before the deadline"
labels=$label
if [[ ${URGENT:-} == true ]]; then
  title="URGENT: Sigstore changed who issues certificates; release and deploy in time"
  subject="URGENT Permaily: release and deploy before the Sigstore deadline"
  labels="$label,$urgent_label"
fi
printf '%s\n\n%s\n' "$mentions" "$(cat "$text")" > "$text.issue"
if [[ -z $open ]]; then
  url=$(gh issue create --repo "$GITHUB_REPOSITORY" --title "$title" --label "$labels" --assignee "$assignees" --body-file "$text.issue")
else
  url=$(gh issue comment "$open" --repo "$GITHUB_REPOSITORY" --body-file "$text.issue")
  [[ ${URGENT:-} != true ]] || gh issue edit "$open" --repo "$GITHUB_REPOSITORY" --title "$title" --add-label "$urgent_label" >/dev/null
fi
echo "Told the maintainers: $url"
mail "$subject" "$(cat "$text")"$'\n\n'"$url"
