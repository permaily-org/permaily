#!/usr/bin/env bash
# Deploys an enclave image and permaily-host to the instance created by deploy/tofu.
#
#   nix build .#eif --out-link result-eif                # or .#eif-staging, or scripts/release-image.sh for a release
#   nix build .#permaily-host --out-link result-host
#   AWS_PROFILE=permaily scripts/deploy.sh result-eif result-host/bin/permaily-host [--debug]
#
#   AWS_PROFILE=permaily scripts/deploy.sh --logs-only
#
# --debug runs the enclave in debug mode (console readable with
# `nitro-cli console`). Its PCRs are all zero, so permaily-verify rejects it.
# Never use it for a deployment you intend to verify.
#
# Every deploy also installs the CloudWatch agent that ships the journal of
# permaily-host and permaily-enclave to the log group `permaily-host`
# (deploy/host/install-logs.sh, ADR 0034). --logs-only does that alone, and
# restarts nothing else.
set -euo pipefail

usage() { sed -n '2,17p' "$0"; exit 2; }
logs_only=false
if [[ ${1:-} == "--logs-only" ]]; then
  [[ $# -eq 1 ]] || usage
  logs_only=true
else
  [[ $# -ge 2 ]] || usage
  eif_dir=$1
  host_bin=$2
fi
debug_flags=""
[[ ${3:-} == "--debug" ]] && debug_flags="--debug-mode"

: "${AWS_PROFILE:?set AWS_PROFILE=permaily (there is no default profile)}"
tofu=${TOFU:-tofu}
repo=$(cd "$(dirname "$0")/.." && pwd)
out() { $tofu -chdir="$repo/deploy/tofu" output -raw "$1"; }

bucket=$(out artifacts_bucket)
instance=$(out instance_id)
region=$(out region)

# The log shipping's installer and configuration, carried in the command itself.
b64() { base64 <"$1" | tr -d '\n'; }
logs_installer=$(b64 "$repo/deploy/host/install-logs.sh")
logs_config=$(b64 "$repo/deploy/host/cloudwatch-agent.json")
read -r -d '' logs_script <<EOF || true
logs=\$(mktemp -d)
echo $logs_installer | base64 -d > \$logs/install-logs.sh
echo $logs_config | base64 -d > \$logs/cloudwatch-agent.json
bash \$logs/install-logs.sh \$logs/cloudwatch-agent.json || echo "WARNING: the CloudWatch agent was not installed; the journal is not shipped (docs/operations.md)" >&2
rm -rf \$logs
EOF

run() {
  local comment=$1 commands=$2 cmd_id
  cmd_id=$(aws ssm send-command --region "$region" --instance-ids "$instance" \
    --document-name AWS-RunShellScript --comment "$comment" \
    --parameters "$(jq -n --arg s "$commands" '{commands: [$s]}')" \
    --query Command.CommandId --output text)
  aws ssm wait command-executed --region "$region" --command-id "$cmd_id" --instance-id "$instance" || true
  aws ssm get-command-invocation --region "$region" --command-id "$cmd_id" --instance-id "$instance" \
    --query '[Status, StandardOutputContent, StandardErrorContent]' --output text
}

if $logs_only; then
  echo "Installing the log shipping on $instance"
  run "permaily logs" "set -euo pipefail
$logs_script"
  exit 0
fi

eif="$eif_dir/permaily.eif"
[[ -f $eif && -f $eif_dir/pcrs.json && -f $host_bin ]] || { echo "missing $eif, $eif_dir/pcrs.json or $host_bin" >&2; exit 1; }
eif_sha=$(sha256sum "$eif" | cut -d' ' -f1)
host_sha=$(sha256sum "$host_bin" | cut -d' ' -f1)
prefix="artifacts/$eif_sha"

echo "Uploading image $eif_sha"
aws s3 cp --region "$region" --only-show-errors "$eif" "s3://$bucket/$prefix/permaily.eif"
aws s3 cp --region "$region" --only-show-errors "$host_bin" "s3://$bucket/$prefix/permaily-host"
echo "Expected PCRs:"
cat "$eif_dir/pcrs.json"

read -r -d '' script <<EOF || true
set -euo pipefail
cloud-init status --wait >/dev/null
tmp=\$(mktemp -d)
aws s3 cp --region $region --only-show-errors s3://$bucket/$prefix/permaily.eif \$tmp/permaily.eif
aws s3 cp --region $region --only-show-errors s3://$bucket/$prefix/permaily-host \$tmp/permaily-host
echo "$eif_sha  \$tmp/permaily.eif" | sha256sum -c -
echo "$host_sha  \$tmp/permaily-host" | sha256sum -c -
install -m 0644 \$tmp/permaily.eif /opt/permaily/permaily.eif
install -m 0755 \$tmp/permaily-host /opt/permaily/permaily-host
rm -rf \$tmp
$logs_script
sed -i 's/^ENCLAVE_EXTRA_FLAGS=.*/ENCLAVE_EXTRA_FLAGS=$debug_flags/' /etc/permaily/enclave.env
systemctl restart permaily-host.service
systemctl restart permaily-enclave.service
nitro-cli describe-enclaves | jq '.[] | {EnclaveID, State, Flags, Measurements}'
EOF

echo "Restarting the enclave on $instance"
run "permaily deploy $eif_sha" "$script"
