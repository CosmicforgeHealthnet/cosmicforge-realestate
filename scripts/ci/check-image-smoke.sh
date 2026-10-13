#!/usr/bin/env bash
# Structural image gate. It does not replace configured process/deployment E2E.
set -euo pipefail
image="${1:?image reference required}"
component="${2:?component required}"
test "$(docker inspect --format '{{.Config.User}}' "$image")" = 'nonroot:nonroot'
work="$(mktemp -d)"
cid=""
trap 'test -z "$cid" || docker rm "$cid" >/dev/null; rm -rf "$work"' EXIT
cid="$(docker create "$image")"
binaries=(api)
if [ "$component" != api-gateway ]; then binaries+=(worker migrate); fi
for binary in "${binaries[@]}"; do
  docker cp "$cid:/app/$binary" "$work/$binary"
  test -s "$work/$binary"
done
for binary in api worker; do
  if [ "$component" = api-gateway ] && [ "$binary" = worker ]; then continue; fi
  # A server is deliberately absent. Success is a broken probe; Docker errors
  # 125/126/127 must also fail, rather than masquerading as the expected exit 1.
  rc=0
  probe=healthcheck
  if [ "$component" = individual-owner-service ]; then probe=readinesscheck; fi
  docker run --rm "$image" "/app/$binary" "$probe" >"$work/probe" 2>&1 || rc=$?
  test "$rc" = 1
  grep -Eq 'http://.*(health|ready)' "$work/probe"
done
if [ "$component" != api-gateway ]; then
  rc=0
  docker run --rm "$image" /app/migrate version >"$work/migrate" 2>&1 || rc=$?
  test "$rc" = 1
  grep -qi 'DATABASE_URL' "$work/migrate"
fi
