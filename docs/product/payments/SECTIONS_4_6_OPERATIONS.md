# Payment Sections 4–6 architecture and operations

The lifecycle package extends the existing Financial Core. Payment alone owns
payment_db, ledger journals, allocation amounts, reservations and financial
events. Business services attest their own facts through signed service JWTs;
references do not grant Payment ownership of agreements, property titles,
tenancy status, inspections or bank verification. No other service database is
queried. Provider adapters remain confined to Payment.

## Authorized workflow

1. Admin records an explicitly approved, immutable policy pack scoped by category,
   jurisdiction, currency, contract, product and effective interval. All eighteen
   catalogue templates start disabled. Approval, cancellation, refund, dispute and
   adjustment references are required for active packs. No legal deadline, tax
   rate, commission or tariff is inferred from the blueprint's examples.
2. The business source records the accepted quote, payer consent and versioned
   agreement. Exact positive integer components must reconcile to the payable.
   The canonical intent binds that quote, amount, currency, source, jurisdiction,
   category, policy and custody requirement. One intent is allowed per quote.
3. Payment verifies collection from the original provider/reference. Collection
   debits provider receivable and credits PAYMENT_PENDING; it proves neither
   actual settled cash nor lawful custody.
4. The original source submits the exact allocation. Verified owner percentages,
   fee-only manager mandates, named beneficiary wallets and component authority
   must match. Accepted terms are immutable. An unreleased, unheld correction
   compensates the original journal and appends a new allocation version.
5. Allocation transfers PAYMENT_PENDING to transaction-specific PROTECTED,
   deposit, reserve, fee or internal settlement-control accounts. Owner gross
   proceeds never become a manager entitlement. Platform allocation is unearned
   until its release conditions are fulfilled.
6. Admin supplies two distinct finance approvals and reconciled cash evidence.
   Payment authenticates the settlement, checks its own transaction membership,
   exact gross/fee totals, original charge context and cumulative merchant
   liquidity, and then journals matched cash. Open reconciliation exceptions
   block confirmation. A merchant balance alone never proves custody.
7. Policy-authorized services attest versioned, expiring release evidence for
   each line and condition. Original source or Admin commands a capped release.
   Funding, current verification/authority, restrictions and any required lawful
   custody approval must pass. PROTECTED becomes AVAILABLE; internal platform
   settlement-control becomes earned REVENUE. Partial releases remain possible
   while other amounts on the same line are reserved or already paid.
8. Verification attests a beneficiary-owned bank reference and an immutable
   provider recipient binding. Admin authorizes the exact wallet, allocation
   line, amount and destination with step-up/risk references and required dual
   review. Approvals expire and are single-use.
9. The delegated beneficiary domain commands withdrawal. Payment checks current
   verification, destination, authority, limits, pool coverage, approved test
   payout capability and independently funded provider fee capacity. It journals
   AVAILABLE to WITHDRAWAL_PENDING before submitting one external attempt.
10. Only authenticated original-reference readback can complete the payout.
    CONFIRMED_PAID consumes the reservation and settled cash. A confirmed
    no-effect failure returns funds to AVAILABLE or FROZEN when held, restricted
    or verification has been revoked. Unknown, OTP, processing, mismatch or
    reversal-review outcomes retain the reservation. An unknown or over-cap
    provider fee also prevents terminal accounting. Actual accepted fees debit
    processing expense and settled cash, without consuming beneficiary liabilities.

Every journal and associated fact/audit/outbox record commits atomically.
The pool lock serializes coverage and fee reservations; line locks serialize
release, hold and withdrawal caps. Authority and verification locks prevent
revocation from being silently bypassed during authorization. Payout terminal
locks make callbacks, recovery and manual readback converge on one terminal
journal. Integer arithmetic rejects overflow and fractional-minor-unit BPS
allocations instead of silently leaving a residual.

## Command and read authority

| Operation | Required authenticated authority |
|---|---|
| Policy version, activation scope, evidenced deactivation | Admin |
| Owner/co-owner/corporate-owner entitlement | Owner or Business Operations, verified ownership percentage |
| Manager/management company entitlement | Owner or Business Operations, explicit fee mandate and allowed fee components |
| Professional entitlement | Accepted work mandate and verified beneficiary |
| Platform accounting entitlement | Admin; internal account, no public wallet |
| Quote, allocation, rent agreement and obligation | Original authoritative business source |
| Independent milestone certificate | Approved policy evidence service; developer self-certification rejected unless explicitly approved |
| Funding | Admin, two distinct finance approvers, authenticated provider and reconciled cash evidence |
| Release or share hold | Original source or Admin; clearing a hold requires dual approval |
| Bank-to-recipient binding | Verification service |
| Withdrawal approval | Admin with exact step-up/risk decision references |
| Withdrawal execution | Delegated beneficiary's own domain and wallet |
| Readback | Beneficiary owner, Admin or Payment recovery worker |
| Public reads | Signed gateway delegation plus resource ownership; no public financial mutation |

Payer receipts disclose accepted charge components and collection/settlement
facts. They omit other parties' allocations. Beneficiary allocation reads filter
unrelated shares. The source can inspect its full business transaction. Payout
reads remain wallet-owner/Admin scoped. Evidence, mandate and risk decisions
come from authenticated authorities; opaque strings from a client are insufficient.

## Rent and deposits

Agreements are immutable signed source facts with fixed or continuing terms.
Rent obligations are separate immutable economic records keyed by agreement,
period and RENT/DEPOSIT stream. Creation is an explicit command and does not
initialize collection. Initial charges are allowed once across agreement versions.
Recurring obligations include only RECURRING components; onboarding/platform
ONE_TIME charges and ACTUAL_SERVICE_ONLY legal work do not recur. New legal work
requires separately approved disclosed work and evidence.

Owner-side commission uses the accepted policy's BPS and contract reference.
The tenant still pays the accepted rent; commission and explicitly contracted
manager fees are deducted before exact co-owner distribution. Manager authority
must name the agreement's precise fee mandate. No production commission rate
is supplied. The NGN 1,000,000 rent, NGN 200,000 initial fee and 200 BPS examples
appear only in approved test fixtures.

Fixed terms cannot automatically renew beyond their end. Continuing agreements
can create later manually commanded periods while authoritative terms remain
active. Revisions require the current prior version, later effective time and
material-change evidence. Historical obligations retain their original versions.
Termination is another accepted agreement version, not destructive rewriting.
Annual, semiannual, quarterly, monthly and custom schedules are policy-configured;
sources supply exact period and due dates. Overlapping periods are rejected.

Residential deposits have a separate policy, quote and obligation stream and
ledger DEPOSIT liability owned by the payer. They cannot be released or withdrawn
as ordinary rent/commission. Deposit remedies, statutory deadlines, refunds and
deductions require approved later workflows and jurisdiction policy; none is guessed.

## Recovery and incident procedure

- For collection uncertainty, retain the canonical intent and original provider
  route; use existing original-reference verification. Do not initialize again.
- For payout uncertainty, retain WITHDRAWAL_PENDING and the durable attempt. The
  worker polls readback for nonterminal attempts; it never calls transfer
  initialization. A crash after RESERVED/SUBMITTING is resolved by readback or
  operator review, even when that means an attempt remains reserved indefinitely.
- Do not manually mark a timeout successful/failed, replace its recipient or
  create a failover transfer. A new command is possible only after a definitive
  no-effect failure, safe compensation and a new independent withdrawal approval.
- OTP_REQUIRED remains reserved. The provider interface supports a future OTP
  adapter contract, but no public OTP or automatic finalization flow is exposed.
- A provider reversal is REVIEW_REQUIRED, retaining reservation. Reversals of
  already terminal payments require the later chargeback/reversal workflow;
  terminal history cannot be rewritten.
- Apply a line hold for a dispute or evidence issue. Only its remaining protected
  and unreserved available funds move to FROZEN. Other beneficiaries are independent;
  an in-flight payout must still be resolved from provider truth.
- Resolve coverage discrepancies through existing reconciliation exception
  controls and authoritative evidence. Never create fictitious cash or move
  funds to another merchant pool to clear an exception.
- Observe unknown payouts, expiring evidence/mandates, overdue reconciliation,
  rejected cash coverage, fee mismatches and outbox retries. Audit trails retain
  source, policy, evidence, operation fingerprints and original references.

## Provider and activation boundaries

Paystack test adapter contracts use authenticated recipient and transfer readback,
original reference, exact amount/currency/recipient, merchant and test-mode
validation. The [transfer API](https://paystack.com/docs/api/transfer/) and
[recipient API](https://paystack.com/docs/api/transfer-recipient/) are the primary
wire references. Submission acknowledgement alone never completes accounting.
Missing authoritative fee information causes a reserved review state. Settlement
membership currently reads one page of up to 100 transactions: larger batches
fail exact-total matching until complete pagination is implemented and certified.

Registering TRANSFER in test mode requires payout approval, a payout approval
reference, fee approval/cap and a payout adapter. CustodyApproved, CUSTODY,
MANDATE, live mode and production environment remain rejected. The three live
runtime capability flags remain disabled. Recorded bank references themselves
never activate execution. No due-date scheduler, provider authorization reuse,
automatic card/bank debit or saved-account charge exists.

Regulated-finance policy representation requires a licensed-partner approval
reference. It cannot turn an ordinary owner wallet into an insurance or mortgage
partner wallet. Execution remains externally blocked pending a separately approved
partner integration. Paystack transfers are not an escrow/custody authorization.

## Reproduction and controlled handoff

Set CF_TEST_POSTGRES_URL and CF_TEST_REDIS_ADDR to dedicated real local test
services, CF_REQUIRE_INTEGRATION=1, CGO_ENABLED=1 and a working C compiler for the
race detector. Run scripts/payments/certify.ps1 with the actual staticcheck, SQLC
and Git Bash paths. Independently build all three affected modules with GOWORK=off.
Generate contracts with scripts/payments/generate_contracts.py; contracts are
JSON-form YAML and actual lifecycle DTO schemas are reflected by cmd/contracts.
All event definitions embed only the schemas their payload needs.

Saved final JSON test logs and sections-4-6-test-summary.json are the executed
evidence. Summarize with scripts/payments/summarize_tests.py --sections-4-6.
After committing, export the exact-SHA report using
scripts/payments/finalize_report.py --sections-4-6. The original Sections 1–3
closure and evidence remain historical and are not overwritten.

Migration 00005 supports an empty up/down round trip. Once accepted lifecycle
history exists, destructive rollback is rejected. Use forward corrective migrations,
append policy/authority decisions and compensated journals; preserve financial history.
Deployment least privilege, NATS ACL/TLS, secret-managed sandbox testing, Docker
images, staging and production certification remain external gates.
