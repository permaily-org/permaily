#!/usr/bin/env bash
# Runs the browser suite of ADR 0026 ("Tests", 26 to 31) with the browsers cut
# off from everything but the suite's server: both run in containers on an
# internal Docker network (no route out), the browsers' only proxy is the
# server's, and their only DNS server is the server's resolver, which records
# every name asked and answers that it does not exist. No host a hostile
# message names can resolve to, or reach, anything outside.
#
# Usage: tests/message-frame/run.sh [browsers] [playwright test arguments...]
#   browsers: a comma-separated list of chromium, firefox, webkit (all three by default).
# Needs: go (or FRAMETEST_BINARY), docker, the web app built in web/dist, and `bun install` done in tests/message-frame.
set -euo pipefail
cd "$(dirname "$0")/../.."
root=$(pwd)
browsers=${1:-chromium,firefox,webkit}
[[ $# -gt 0 ]] && shift
image=mcr.microsoft.com/playwright:v1.65.0-noble@sha256:9beb546099e11f52d62e505537914870e01a67e0e6b00ae41da4775324e73dbe
test -f web/dist/index.html || { echo "web/dist is not built: cd web && bun run build" >&2; exit 1; }
test -d tests/message-frame/node_modules/@playwright/test || { echo "run bun install in tests/message-frame" >&2; exit 1; }

work=$(mktemp -d)
name=permaily-frametest-$$
net=$name
cleanup() {
  docker rm -f "$name-server" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

# The server: the test binary of internal/mailhtml/frametest, static, or one built already (FRAMETEST_BINARY).
if [[ -n ${FRAMETEST_BINARY:-} ]]; then
  cp "$FRAMETEST_BINARY" "$work/frametest.test"
else
  CGO_ENABLED=0 go test -c -o "$work/frametest.test" ./internal/mailhtml/frametest
fi
# A subnet of its own, so the server has an address the browsers' resolv.conf can name.
subnet=10.$((RANDOM % 200 + 20)).$((RANDOM % 250)).0
docker network create --internal --subnet "$subnet/24" "$net" >/dev/null
server=${subnet%.0}.2
printf 'nameserver %s\noptions timeout:1 attempts:1\n' "$server" >"$work/resolv.conf"
chmod -R a+rX "$work"

docker run -d --name "$name-server" --network "$net" --ip "$server" -u "$(id -u):$(id -g)" \
  -v "$root:/src:ro" -v "$work:/work:ro" -w /src/internal/mailhtml/frametest \
  -e PERMAILY_FRAMETEST_ADDR=0.0.0.0:18990 -e PERMAILY_FRAMETEST_DNS=0.0.0.0:53 -e PERMAILY_FRAMETEST_WEB=/src/web/dist \
  --cap-add NET_BIND_SERVICE --sysctl net.ipv4.ip_unprivileged_port_start=53 \
  "$image" /work/frametest.test -test.run '^TestServe$' -test.timeout 0 -test.v >/dev/null

set +e
docker run --rm --network "$net" --ipc host -u "$(id -u):$(id -g)" -e HOME=/tmp -e CI -e NPM_CONFIG_UPDATE_NOTIFIER=false \
  -e FRAMETEST_BROWSERS="$browsers" -e FRAMETEST_PROXY="http://$server:18990" \
  -v "$root:/src" -v "$work/resolv.conf:/etc/resolv.conf:ro" -w /src/tests/message-frame \
  "$image" sh -c 'for i in $(seq 1 60); do node -e "fetch(process.env.FRAMETEST_PROXY + \"/health\").then((r) => process.exit(r.ok ? 0 : 1), () => process.exit(1))" && break; sleep 1; done; exec npx playwright test "$@"' sh "$@"
status=$?
set -e
if [[ $status -ne 0 ]]; then
  echo "--- the suite's server" >&2
  docker logs "$name-server" 2>&1 | tail -40 >&2
fi
exit $status
