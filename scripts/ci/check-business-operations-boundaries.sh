#!/usr/bin/env bash
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
target="$root/services/business-operations-service"
fail=0

# This bounded context may retain references and identifiers, but may not become
# an authority for another service's data, provider operation, or raw media.
if grep -RniE 'api\.paystack\.co|PAYSTACK_SECRET|paystack-go|stripe-go|aws-sdk-go|cloud\.google\.com/go/firestore|firebase\.google\.com/go(/v4)?/db' \
  --include='*.go' --include='*.sql' "$target"; then
  echo "Business Operations must not execute payment-provider calls or own external media/business databases." >&2
  fail=1
fi
if grep -RniE 'CREATE TABLE (messages|conversations|media_objects|documents|payments|ledger_entries|verification_cases)' \
  --include='*.sql' "$target/migrations"; then
  echo "Business Operations migration attempts to create data owned by another service." >&2
  fail=1
fi
if grep -RniE 'services/(payment|media-document|verification|notification)-service/(internal|repositories)' \
  --include='*.go' "$target"; then
  echo "Business Operations imports another service implementation." >&2
  fail=1
fi
if [ "$fail" -ne 0 ]; then exit 1; fi
echo "business-operations architecture boundaries OK"
