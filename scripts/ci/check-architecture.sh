#!/usr/bin/env bash
# Architecture boundary gate (BACKEND_FOUNDATION_BLUEPRINT.md §3, §4, §16; ADR-0009).
#
# Fails when:
#   - a Go module imports (or requires) another service/gateway module;
#   - shared/go imports any service or gateway module;
#   - Paystack is referenced outside payment-service;
#   - Firestore / Realtime Database clients are imported anywhere;
#   - a central identity service is reintroduced.
#
# Usage: scripts/ci/check-architecture.sh [repo-root]
#        scripts/ci/check-architecture.sh --self-test
set -euo pipefail

PREFIX="github.com/CosmicforgeHealthnet/cosmicforge-realestate"
SHARED="$PREFIX/shared/go"

check() {
  local root="$1" fail=0
  local -a mods
  mapfile -t mods < <(find "$root" -name go.mod -not -path '*/testdata/*' -not -path '*/node_modules/*' -not -path '*/.git/*' | sort)
  if [[ ${#mods[@]} -eq 0 ]]; then
    echo "no Go modules found under $root" >&2
    return 1
  fi
  for gm in "${mods[@]}"; do
    local dir modpath deps
    dir="$(dirname "$gm")"
    modpath="$(awk '/^module /{print $2; exit}' "$gm")"
    # go.mod must not require another in-repo module except shared/go.
    while read -r req; do
      [[ -z "$req" || "$req" == "$SHARED" || "$req" == "$modpath" ]] && continue
      echo "VIOLATION: $modpath requires in-repo module $req" >&2
      fail=1
    done < <(grep -oE "$PREFIX/[A-Za-z0-9._/-]+" "$gm" | grep -v "^$modpath\$" | sort -u || true)
    if ! deps="$(cd "$dir" && GOWORK=off GOFLAGS=-mod=mod go list -deps -test -f '{{.ImportPath}}' ./... 2>&1)"; then
      echo "go list failed in $dir:" >&2
      echo "$deps" >&2
      fail=1
      continue
    fi
    while read -r dep; do
      [[ "$dep" != "$PREFIX"/* ]] && continue
      [[ "$dep" == "$modpath" || "$dep" == "$modpath"/* ]] && continue
      if [[ "$modpath" != "$SHARED" && ( "$dep" == "$SHARED" || "$dep" == "$SHARED"/* ) ]]; then
        continue
      fi
      echo "VIOLATION: $modpath imports $dep" >&2
      fail=1
    done <<< "$deps"
  done

  # Paystack only in payment-service.
  local hits
  hits="$(grep -rIlE 'api\.paystack\.co|PAYSTACK_SECRET|paystack-go' --include='*.go' "$root" 2>/dev/null \
    | grep -v '/services/payment-service/' | grep -v '/testdata/' || true)"
  if [[ -n "$hits" ]]; then
    echo "VIOLATION: Paystack referenced outside payment-service:" >&2
    echo "$hits" >&2
    fail=1
  fi
  # Firebase is not a business database.
  hits="$(grep -rIlE '"cloud\.google\.com/go/firestore"|"firebase\.google\.com/go(/v4)?/db"' --include='*.go' "$root" 2>/dev/null | grep -v '/testdata/' || true)"
  if [[ -n "$hits" ]]; then
    echo "VIOLATION: Firestore/Realtime Database client imported:" >&2
    echo "$hits" >&2
    fail=1
  fi
  if [[ -e "$root/services/identity-service" ]]; then
    echo "VIOLATION: services/identity-service must not exist (no central identity service)" >&2
    fail=1
  fi
  return $fail
}

if [[ "${1:-}" == "--self-test" ]]; then
  here="$(cd "$(dirname "$0")" && pwd)"
  fixture="$here/testdata/archcheck"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  cp -r "$fixture/." "$tmp/"
  if check "$tmp" 2>"$tmp/out.txt"; then
    echo "self-test FAILED: cross-service import was not detected" >&2
    exit 1
  fi
  grep -q "svc-a imports $PREFIX/services/svc-b" "$tmp/out.txt" || { cat "$tmp/out.txt" >&2; echo "self-test FAILED: wrong diagnostics" >&2; exit 1; }
  grep -q "svc-a requires in-repo module" "$tmp/out.txt" || { cat "$tmp/out.txt" >&2; exit 1; }
  echo "architecture check self-test passed"
  exit 0
fi

root="${1:-$(git rev-parse --show-toplevel)}"
if check "$root"; then
  echo "architecture boundaries OK"
else
  echo "architecture boundary check FAILED" >&2
  exit 1
fi
