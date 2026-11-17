#!/usr/bin/env bash
# Builds the drift check's image (deploy/drift) and pushes it to the ECR
# repository permaily-drift, then prints the digest to put in
# deploy/tofu/production.auto.tfvars as drift_image_digest (docs/operations.md,
# "The drift check").
#
#   AWS_PROFILE=permaily scripts/drift-image.sh
#
# Needs docker (with buildx, for linux/amd64 on any machine), the aws CLI and
# tofu. The base images are pinned by digest in deploy/drift/Dockerfile, so the
# image holds those and the two scripts, nothing downloaded at build time. The
# tag is the commit the scripts come from; a tag cannot be pushed twice.
set -euo pipefail

: "${AWS_PROFILE:?set AWS_PROFILE=permaily (there is no default profile)}"
tofu=${TOFU:-tofu}
repo=$(cd "$(dirname "$0")/.." && pwd)
region=us-east-2

[[ -z $(git -C "$repo" status --porcelain -- deploy/drift) ]] || { echo "deploy/drift has uncommitted changes" >&2; exit 1; }
commit=$(git -C "$repo" rev-parse HEAD)
registry=$($tofu -chdir="$repo/deploy/tofu" output -raw drift_repository)
image="$registry:${commit:0:12}"

aws ecr get-login-password --region "$region" | docker login --username AWS --password-stdin "${registry%%/*}"
docker buildx build --platform linux/amd64 --provenance=false --sbom=false -t "$image" --push "$repo/deploy/drift"

digest=$(aws ecr describe-images --region "$region" --repository-name "${registry#*/}" \
  --image-ids "imageTag=${commit:0:12}" --query 'imageDetails[0].imageDigest' --output text)
echo
echo "Pushed $image"
echo "deploy/tofu/production.auto.tfvars:"
echo "  drift_image_digest = \"$digest\""
