#!/usr/bin/env bash
# Deploys an enclave image and permaily-host to the instance created by deploy/tofu.
#
#   nix build .#eif --out-link result-eif                # or .#eif-staging, or a downloaded release
#   nix build .#permaily-host --out-link result-host
#   AWS_PROFILE=permaily scripts/deploy.sh result-eif result-host/bin/permaily-host [--debug]
#
# --debug runs the enclave in debug mode (console readable with
# `nitro-cli console`). Its PCRs are all zero, so permaily-verify rejects it.
# Never use it for a deployment you intend to verify.
set -euo pipefail

usage() { sed -n '2,11p' "$0"; exit 2; }
[[ $# -ge 2 ]] || usage
eif_dir=$1
host_bin=$2
debug_flags=""
[[ ${3:-} == "--debug" ]] && debug_flags="--debug-mode"

: "${AWS_PROFILE:?set AWS_PROFILE=permaily (there is no default profile)}"
tofu=${TOFU:-tofu}
repo=$(cd "$(dirname "$0")/.." && pwd)
out() { $tofu -chdir="$repo/deploy/tofu" output -raw "$1"; }

bucket=$(out artifacts_bucket)
instance=$(out instance_id)
region=$(out region)

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
sed -i 's/^ENCLAVE_EXTRA_FLAGS=.*/ENCLAVE_EXTRA_FLAGS=$debug_flags/' /etc/permaily/enclave.env
systemctl restart permaily-host.service
systemctl restart permaily-enclave.service
nitro-cli describe-enclaves | jq '.[] | {EnclaveID, State, Flags, Measurements}'
EOF

echo "Restarting the enclave on $instance"
cmd_id=$(aws ssm send-command --region "$region" --instance-ids "$instance" \
  --document-name AWS-RunShellScript --comment "permaily deploy $eif_sha" \
  --parameters "$(jq -n --arg s "$script" '{commands: [$s]}')" \
  --query Command.CommandId --output text)
aws ssm wait command-executed --region "$region" --command-id "$cmd_id" --instance-id "$instance" || true
aws ssm get-command-invocation --region "$region" --command-id "$cmd_id" --instance-id "$instance" \
  --query '[Status, StandardOutputContent, StandardErrorContent]' --output text
