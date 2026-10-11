# Paystack TEST certification operations

Capability results and limitations are in [PAYSTACK_SANDBOX_CERTIFICATION.md](PAYSTACK_SANDBOX_CERTIFICATION.md). These commands are opt-in TEST work, not a deployment/activation workflow. Default tests retain deterministic provider simulations.

## Prerequisites

Use an ignored root `.env` containing the locally supplied TEST credentials and exact `PAYSTACK_BASE_URL=https://api.paystack.co`. Never print the file, put credentials on the command line or commit it. Backend authentication uses `PAYSTACK_SECRET_KEY`; the public key is unused. Leave all live custody/collection/refund/transfer, recurring execution and cash-out flags false. The runner rejects production, live credentials or another API origin.

Set `CF_TEST_POSTGRES_URL` to isolated real PostgreSQL and `CF_TEST_REDIS_ADDR` to real loopback Redis. The certification suite uses the existing fixture migrations/bootstrap and synthetic test authorities; fixture databases are removed after the run. Embedded JetStream is launched by its existing tests. Set `CF_REQUIRE_INTEGRATION=1`; unavailable required infrastructure is a failure.

## Independent real certification

Run `python scripts/payments/paystack_sandbox.py` only when a new set of TEST transactions is intended. It creates four independent NGN 1,000 hosted checkouts, exercises refunds, recipient creation and a test transfer request. It uses the existing Go adapters and Financial Core. It never enables payout entitlement/custody. Merchant bootstrap comes from authenticated Paystack TEST customer data, then uses the existing generated-ID test configuration with named secret references.

While it runs, inspect only `tmp/payments/paystack-sandbox-checkouts.json` to open hosted URLs. Confirm the checkout UI is TEST mode. Complete the four scenarios with [official Paystack test payment fixtures](https://paystack.com/docs/payments/test-payments/), including the documented failed-refund and attention fixtures for their named scenarios. No card data belongs in runtime source or evidence. The bounded eight-minute window reads original provider references; UI success alone never confirms the ledger.

Checkout/access-code-bearing files are ignored local control files: do not publish or commit them. Saved evidence omits credential headers, signatures, card/BVN/bank/customer details and actual access-code values. The runner scans for supplied keys before accepting its output.

`python scripts/payments/paystack_sandbox.py --receipt-checkout` creates one additional, independent NGN 1,000 TEST checkout for atomic receipt/reconciliation regression. Its hosted URL is in ignored `tmp/payments/paystack-receipt-checkout.json`. It does not refund that separate charge.

## Pending outcomes

Use `python scripts/payments/paystack_sandbox.py --followup` with the original ignored `paystack-real-evidence.json` and merchant bootstrap available. It reads existing charge/refund references and provider refund IDs; it submits no new charge, refund, recipient or transfer. Do not rerun the main certification to resolve an existing ambiguous operation. Preserve original evidence; a later observation does not rewrite the earlier result.

HTTP acceptance and `pending` do not establish terminal refund success/failure/attention. Missing merchant eligibility/compliance must be resolved by the account owner through Paystack. Do not bypass account controls, create direct Titan integration or simulate a real funded DVA. Merchant TEST balance is not custody or withdrawable wallet money.

## Local gates and closure

Run the existing `scripts/payments/certify.ps1` with real test infrastructure, GCC/CGO race support, supported staticcheck/SQLC and Bash paths. It covers Payment/Owner/Gateway race regression, vet/staticcheck, SQLC and architecture gates. Also run source builds, sandbox-tag vet/staticcheck/build, contract generation reproduction and formatting/whitespace checks. Integration tests themselves validate implemented OpenAPI/Postman routes, DTOs and emitted event schemas. Only the explicit real runner uses the `paystack_sandbox` build tag; running all tagged tests without a chosen scenario would create multiple independent checkouts.

For saved final evidence, retain final Payment race results separately from Owner/Gateway regression and the three selected real-test runs. Exclude superseded/repeated Payment rows before running `python scripts/payments/summarize_tests.py --paystack-sandbox`. It rejects duplicate test identities and any failed/skipped tests. Never merge exploratory failures into a claimed failure-free final run or label simulator results CERTIFIED.

Commit authorized source/tests/scripts/docs/redacted evidence only, verify the index contains neither supplied key and root `.env` remains ignored, then run `python scripts/payments/finalize_report.py --paystack-sandbox` from a clean tree. It exports the exact committed SHA/status report under ignored `tmp/payments`. Do not push. Keep Section 11, production/custody/payout activation, recurring mandate execution and cash-out outside this task.
