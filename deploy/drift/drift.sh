#!/bin/bash
# The hourly drift check (ADR 0035): `tofu plan -detailed-exitcode` of the
# develop branch's deploy/tofu against production, with read-only permissions
# and without the state lock. It never applies, and its role could not.
#
#   exit code 0: nothing to say, or the alarm was mailed
#   otherwise:   the alarm could not be mailed (bootstrap reports an error,
#                and the CloudWatch alarm on the function's errors mails)
#
# Environment (deploy/tofu/drift.tf): DRIFT_REPOSITORY and DRIFT_BRANCH (where
# deploy/ is fetched from, at the branch's newest commit), DRIFT_TOPIC_ARN (the SNS topic of the alarms) and
# DRIFT_CLOUDFLARE_SECRET (the secret holding a read-only Cloudflare token).
# Lambda gives AWS_REGION and the role's credentials. Nothing here prints the
# token.
set -uo pipefail

export HOME=/tmp TF_IN_AUTOMATION=1 TF_INPUT=0 CHECKPOINT_DISABLE=1
export TF_PLUGIN_CACHE_DIR=/tmp/plugins TF_VAR_aws_profile=""
mkdir -p "$TF_PLUGIN_CACHE_DIR"
work=$(mktemp -d /tmp/drift.XXXXXX)
trap 'rm -rf "$work"' EXIT
out=$work/output.txt

# aws SERVICE HOST [curl arguments]: a request signed with the role's credentials.
aws() {
  local service=$1 host=$2; shift 2
  curl -sS --fail-with-body --proto '=https' --max-time 30 \
    --aws-sigv4 "aws:amz:$AWS_REGION:$service" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
    -H "x-amz-security-token: $AWS_SESSION_TOKEN" "$@" "https://$host/"
}

# alarm SUBJECT MESSAGE: mails the subscribers of the alarms' topic.
alarm() {
  echo "ALARM: $1"
  aws sns "sns.$AWS_REGION.amazonaws.com" -o /dev/null \
    --data-urlencode Action=Publish --data-urlencode Version=2010-03-31 \
    --data-urlencode "TopicArn=$DRIFT_TOPIC_ARN" --data-urlencode "Subject=$1" \
    --data-urlencode "Message=$2"
}

commit=unknown
footer() { echo "Commit $commit of $DRIFT_REPOSITORY. Nothing was applied: the check can only read. The whole output is in CloudWatch Logs, group /aws/lambda/permaily-drift, at $(date -u +%Y-%m-%dT%H:%MZ). What to do: docs/operations.md, \"The drift check\"."; }

failed() {
  alarm "Permaily: the drift check failed" "The hourly drift check could not tell whether production matches deploy/tofu on $DRIFT_BRANCH ($1).

$(tail -n 40 "$out" 2>/dev/null)

$(footer)"
  exit $?
}

# The Cloudflare token, read-only, from Secrets Manager (populated by the operator).
secret=$(aws secretsmanager "secretsmanager.$AWS_REGION.amazonaws.com" \
  -H 'X-Amz-Target: secretsmanager.GetSecretValue' -H 'Content-Type: application/x-amz-json-1.1' \
  -d "{\"SecretId\":\"$DRIFT_CLOUDFLARE_SECRET\"}" 2>"$out") || failed "the Cloudflare token could not be read"
CLOUDFLARE_API_TOKEN=$(sed -n 's/.*"SecretString":"\([^"\\]*\)".*/\1/p' <<<"$secret")
unset secret
[[ -n $CLOUDFLARE_API_TOKEN ]] || failed "the Cloudflare token's secret has no value"
export CLOUDFLARE_API_TOKEN

# deploy/ at the branch's newest commit. OpenTofu fetches the archive itself
# (no tar here) and leaves out dot files, so the lock file comes apart, from
# the same commit; the providers are downloaded and checked against it.
commit=$(curl -sS --fail --proto '=https' --max-time 30 -H 'Accept: application/vnd.github.sha' \
  "https://api.github.com/repos/$DRIFT_REPOSITORY/commits/$DRIFT_BRANCH" 2>"$out") || failed "the branch could not be read from GitHub"
[[ $commit =~ ^[0-9a-f]{40}$ ]] || failed "GitHub did not answer a commit"
mkdir "$work/deploy" && cd "$work/deploy" || failed "no working directory"
{
  tofu init -input=false -no-color \
    -from-module="https://codeload.github.com/$DRIFT_REPOSITORY/tar.gz/$commit//${DRIFT_REPOSITORY#*/}-$commit/deploy?archive=tar.gz" &&
    cd tofu &&
    curl -sS --fail --proto '=https' --max-time 30 -o .terraform.lock.hcl \
      "https://raw.githubusercontent.com/$DRIFT_REPOSITORY/$commit/deploy/tofu/.terraform.lock.hcl" &&
    tofu init -input=false -no-color -lockfile=readonly
} >"$out" 2>&1 || { cat "$out"; failed "tofu init failed"; }

tofu plan -input=false -no-color -lock=false -detailed-exitcode >"$out" 2>&1
code=$?
cat "$out"
case $code in
  0) echo "No changes: production matches deploy/tofu at $commit." ;;
  2) alarm "Permaily: production drifted from deploy/tofu" "The hourly drift check found that production does not match deploy/tofu on $DRIFT_BRANCH: tofu plan wants to change what follows.

$(grep -E '^  # |^Plan:|^Changes to Outputs|^  [~+-] [a-z_]+ +=' "$out" | head -n 200)

$(footer)" ;;
  *) failed "tofu plan exited with $code" ;;
esac
