#!/usr/bin/env bash
# Opens, or brings up to date, the pull request that compiles in the trust
# root Sigstore publishes today. Run by the workflow sigstore-watch when its
# comparison fails, with a token that can push a branch and open a pull
# request (GH_TOKEN).
#
# It writes to $GITHUB_OUTPUT: pull_request (its URL) and urgent (true when the
# new trust root changes who issues signing certificates).
#
# Needs gh, jq and go.
set -euo pipefail

cd "$(dirname "$0")/.."
branch=build/sigstore-trust-root
files=(pkg/provenance/sigstore_trusted_root.json pkg/provenance/sigstore_tuf_root.json pkg/provenance/sigstore_tuf_targets.json)

# The verifier of this checkout decides: a trust root it refuses is not
# compiled in by a script.
scripts/sigstore-trust-root.sh --update
if git diff --quiet -- "${files[@]}"; then
  echo "Nothing to compile in."
  exit 0
fi

authorities() { jq -S .certificateAuthorities; }
urgent=false
if ! diff <(git show "HEAD:${files[0]}" | authorities) <(authorities < "${files[0]}") >/dev/null; then
  urgent=true
fi
{
  echo "Sigstore published a newer trust root than the one compiled into the enclave and the verifier. \`scripts/sigstore-trust-root.sh --update\` fetched it from Sigstore's TUF repository, and the verifier of \`develop\` accepted it: every TUF root signed by the keyholders of the one before."
  echo
  if [[ $urgent == true ]]; then
    echo "**Urgent: it changes who issues signing certificates.** A running enclave does not take that from an update. Merge this, release, and deploy with the handoff while the old issuer still signs (docs/operations.md, \"Sigstore changed\")."
  else
    echo "It changes logs or timestamp authorities, not who issues signing certificates. A running enclave is given the new trust root before each handoff, so merging this is enough: the next release carries it."
  fi
  echo
  echo "## What changed in trusted_root.json"
  echo
  echo '```diff'
  diff <(git show "HEAD:${files[0]}" | jq -S .) <(jq -S . "${files[0]}") || true
  echo '```'
  echo
  echo "CI checks that these three files are exactly an update the base branch accepts (\`scripts/check-trust-root-change.sh\`)."
} > "${RUNNER_TEMP:-/tmp}/trust-root-pr.md"

git checkout -q -B "$branch"
git -c user.name="github-actions[bot]" -c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
  commit -q -m "build: update sigstore's trust root" -- "${files[@]}"
remote="https://x-access-token:${GH_TOKEN}@github.com/${GITHUB_REPOSITORY}.git"
# Push only when the branch does not hold these files already, so the daily
# run does not start CI again for nothing.
if ! git fetch -q "$remote" "$branch" 2>/dev/null || ! git diff --quiet FETCH_HEAD -- "${files[@]}"; then
  git push -q --force "$remote" "$branch"
fi

title="build: update sigstore's trust root"
pr=$(gh pr list --repo "$GITHUB_REPOSITORY" --head "$branch" --base develop --state open --json url --jq '.[0].url // empty')
if [[ -z $pr ]]; then
  pr=$(gh pr create --repo "$GITHUB_REPOSITORY" --base develop --head "$branch" --title "$title" --body-file "${RUNNER_TEMP:-/tmp}/trust-root-pr.md")
else
  gh pr edit "$pr" --repo "$GITHUB_REPOSITORY" --body-file "${RUNNER_TEMP:-/tmp}/trust-root-pr.md" >/dev/null
fi
echo "Pull request: $pr"
{
  echo "pull_request=$pr"
  echo "urgent=$urgent"
} >> "${GITHUB_OUTPUT:-/dev/null}"
