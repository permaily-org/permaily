#!/usr/bin/env bash
# Serves the app as the enclave does, for the real-browser tests' production project (playwright.config.ts): builds
# the web app, embeds it as the image's build does (docs/build.md, "Local development"), compiles the test server of
# web/tests/e2e/server with it, and runs that on localhost:<port>. internal/webui/dist is left as it was found when it
# held only its placeholder. Needs go and bun (`nix develop`).
#
# Usage: web/tests/e2e/serve.sh <port>
set -euo pipefail
port=${1:?usage: serve.sh <port>}
cd "$(dirname "$0")/../../.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

(cd web && bun run build >/dev/null)
dist=internal/webui/dist
placeholder_only=false
if [[ $(find "$dist" -mindepth 1 -not -name .gitkeep | head -1) == "" ]]; then placeholder_only=true; fi
cp -R web/dist/. "$dist/"
status=0
CGO_ENABLED=0 go test -c -o "$work/e2e.test" ./web/tests/e2e/server || status=$?
if [[ $placeholder_only == true ]]; then
  find "$dist" -mindepth 1 -maxdepth 1 -not -name .gitkeep -exec rm -rf {} +
fi
[[ $status -eq 0 ]] || exit $status

PERMAILY_E2E_ADDR="localhost:$port" "$work/e2e.test" -test.run '^TestServe$' -test.timeout 0 &
server=$!
trap 'kill "$server" 2>/dev/null; wait "$server" 2>/dev/null; rm -rf "$work"' EXIT INT TERM
wait "$server"
