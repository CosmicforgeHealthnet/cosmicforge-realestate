#!/usr/bin/env bash
# Tests gate control flow with a fake Docker CLI; not image certification.
set -euo pipefail
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cat >"$work/docker" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
 inspect) echo "${MOCK_USER:-nonroot:nonroot}";;
 create) echo fake-container;;
 cp) if [[ "$2" = *"${MOCK_MISSING:-never}" ]]; then exit 1; fi
     echo binary >"$3"; chmod +x "$3";;
 rm) exit 0;;
 run) if [[ "$*" = *migrate* ]]; then echo 'DATABASE_URL is required'; exit 1; fi
      echo 'Get http://127.0.0.1:8080/health: connection refused'; exit "${MOCK_PROBE:-1}";;
 *) exit 99;;
esac
MOCK
chmod +x "$work/docker"
export PATH="$work:$PATH"
bash scripts/ci/check-image-smoke.sh fixture individual-owner-service
for rc in 0 125 126 127; do
  if MOCK_PROBE="$rc" bash scripts/ci/check-image-smoke.sh fixture individual-owner-service; then echo 'broken probe accepted'; exit 1; fi
done
if MOCK_USER=root bash scripts/ci/check-image-smoke.sh fixture individual-owner-service; then echo 'root accepted'; exit 1; fi
if MOCK_MISSING=worker bash scripts/ci/check-image-smoke.sh fixture individual-owner-service; then echo 'missing worker accepted'; exit 1; fi
echo 'PASS: expected failure, false success, Docker failures, root and missing binary cases'
