# Sections 7–10 API and frontend handoff

Generated contracts are authoritative: `contracts/internal/openapi/payment-service.yaml`, `contracts/public/openapi/payments.yaml`, `contracts/webhooks/paystack.yaml`, `postman/payment-service.postman_collection.json`, and `contracts/events/payments/*.schema.json`. Regenerate with `scripts/payments/generate_contracts.py`. DTO reflection prevents a second hand-written shape for lifecycle requests and responses. Route parity and actual outbox envelopes are validated by integration tests.

There are 100 implemented HTTP operations across internal, public and webhook contracts, and 31 payment event schemas. Public endpoints require domain access authentication at the gateway and audience-bound signed gateway delegation downstream. Internal endpoints require service JWTs; service role and party checks are enforced again inside command methods. HTTP success is an authoritative command/read result, not provider or custody certification.

## Internal additions

| Operation | Authority and result |
| --- | --- |
| Remedy policy/evidence registration and evidence revocation | Admin-approved immutable supplement; evidence supplied by approved original issuer |
| Deposit registration/read | Canonical segregated liability and permitted party projection |
| Case open/read/history, facts, decision, privileged override | Contested amount only; independent Admin review and separated decision execution |
| Refund reserve/read/submit/resolve/cancel | Admin policy/decision; one submission and original-attempt readback; unsubmitted cancellation only |
| Collection reversal | Admin/Payment and authoritative economic provider evidence |
| Recoverable actions | Dual Admin authority, canonical liable party, capped approved recovery |
| Destination version/revocation | Verification/current KYC and bank ownership; cooling, predecessor and risk controls |
| Customer top-up/read; funding attestation | Owned CUSTOMER request; Admin dual cash settlement attestation |
| Wallet spend and pool availability | Owned CUSTOMER accepted-obligation spend; owned wallet eligibility projection |
| Unmatched DVA resolution | Dual Admin review through the normal authoritative DVA matching workflow |
| Reconciliation run/resolution and metrics | Admin; preserved findings, later clear observation, no silent ledger correction |
| Original readback dead-letter replay | Admin evidence; retains provider/reference and never resubmits |
| Intent cancellation | Canonical source/Admin, policy and current evidence; unsubmitted intents only |
| Recurring design | Non-executable DEFERRED descriptor |

Bodies carry canonical UUIDs and evidence references, not browser-selected legal authority. Idempotency-Key is mandatory for case/fact/decision, refund reservation, top-up, wallet spend, recoverable action and intent cancellation. Stable record IDs additionally protect immutable registrations, versions, overrides and reconciliation runs. Reuse the same key for an identical retry; change the economic body only through a new approved decision. Mismatched fingerprints return conflict.

## Public additions

- POST/GET customer top-ups; POST wallet spends.
- GET wallet availability by owned wallet and exact pool; GET recurring design.
- POST/GET financial cases and GET immutable case facts; POST challenges/facts with appropriate party authority.
- GET permitted refund and deposit projections.
- POST beneficiary withdrawal using an existing exact Admin approval and the current verified destination.

Admin mutation, funding attestation, reconciliation, refund execution, destination attestation and readback replay are not public routes. Customer cash-out is not activated. The gateway already authenticates and proxies the payment prefix, so no new bypass route is introduced.

## Display rules

Treat amounts as exact integer minor units; aggregate balance strings avoid JSON precision loss. Format by the returned currency. Keep gross, fee, protected, pending, disputed, refund-reserved, available and withdrawable amounts separate. Use `/wallets/{id}/availability/{pool}` to enable a withdrawal action; a raw available balance cannot enable it. Confirmed cash funding and certified safeguarding are separate labels.

Show top-up as pending until authoritative funding. A checkout redirect, webhook receipt, mandate, saved bank or saved card is not usable money. Send wallet spend only after the user accepts the exact obligation and quote version. After timeout, retry the same operation key or read the retained record; do not create a replacement external payment.

Show refund/payout processing and unknown/review states as retained reservations. Keep original attempt history visible when projection becomes REVERSED. Deposit partial awards remain separate from the refundable remainder. Show claims and counter-evidence without replacing original facts. Cooling and restricted/rail-unavailable states need meaningful reasons, without exposing bank credentials, internal secrets or primary evidence documents.

Section 9.2 must show **DESIGN RESERVED / EXECUTION DEFERRED**. There is no activation, charge or recurring execution endpoint to connect to a UI switch.

## New event payloads

`finance.deposit_registered.v1`, `finance.case_opened.v1`, `finance.case_decided.v1`, `finance.refund_reserved.v1`, `finance.refund_state_changed.v1`, `finance.financial_reversed.v1`, `finance.recoverable_action.v1`, `finance.destination_changed.v1`, `finance.customer_funded.v1`, `finance.wallet_spent.v1`, `finance.reconciliation_completed.v1`, `finance.provider_state_changed.v1`.

Existing economic, lifecycle, identity and payout events remain. Consumers must deduplicate envelope event ID and treat financial facts as authoritative only when committed by Payment Service. State-change events are observations of durable lifecycle transitions; submission is not completion. No provider secret, primary evidence document or automatic recurring instruction is in these payloads.
