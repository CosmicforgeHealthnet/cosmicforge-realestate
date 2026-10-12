# External dependencies and Section 4 handoff

> Current Owner compatibility handoff: [Owner Sections 12–13 integration closure](../individual-owner/SECTIONS_12_13_INTEGRATION_CLOSURE.md). The existing signed wallet client now consumes accepted business/receipt/authority/remedy/statement/payout contracts. Core adds scoped read compatibility and current verified-reference eligibility only; this does not approve payout execution or replace any production gate. Earlier Section 1–3 descriptions below remain historical.

> Current provider evidence and remaining account/commercial/legal blockers: [Paystack TEST certification](PAYSTACK_SANDBOX_CERTIFICATION.md). The dependency statuses below are historical Sections 1–3 observations.

> Historical Sections 1–3 baseline. The current [Sections 4–6 closure](SECTIONS_4_6_CLOSURE_REPORT.md) and [operations guide](SECTIONS_4_6_OPERATIONS.md) supersede deferred lifecycle descriptions below. Original test evidence and scope are retained.

| Dependency | Status | Required owner/evidence |
|---|---|---|
| Eight original Product Feature Documents v5.0 | Unavailable | Product supplies controlled documents; no legal/fee/release rule invented |
| Paystack test credentials and correct merchant | Not supplied/configured for this run | Payments owner supplies secret-managed test keys and merchant; actual sandbox runs required |
| Paystack merchant category and approved collection channels/currencies | Uncertified externally | Written provider/commercial approval by entity and category |
| Manual Payouts enrollment and settlement-account arrangement | Not approved in this run | Paystack acceptance, treasury account references and settlement SLA |
| Paystack-Titan DVA eligibility/capacity | External blocked | Merchant activation, approved validation/consent policy and sandbox/live provisioning proof |
| Licensed custody/stored-value/escrow arrangement | NOT APPROVED | Legal, banking/compliance and provider authorization; O-006 |
| Verification service legal-party/bank/party-to-provider assertions | Controlled signed contract locally | Integration of current KYC/KYB/bank references, expiry/revocation and canonical cross-domain linkage before second provisioning |
| Marketplace/Business/Manager/Artisan/Professional/Admin domain workflows | Contract ready; future integration | Source teams supply authoritative registrations, mandates, entitlements, evidence and approvals; absence does not prevent Payment startup |
| Settlement-to-charge matching and cash attestation | Deferred lifecycle integration | Treasury-approved external evidence, unique matched receipts/batches, processing fee policy and immutable journal mappings |
| Provider migration/funding movement | Disabled | Approved money movement, reconciled destination funding, exact journals and authorization |
| Staging infrastructure | NOT RUN | payment_db least-privilege roles, secrets/TLS, scoped NATS credentials/ACLs, gateway/Owner deployment, alerts and digest-built images |
| Production certification | NOT APPROVED | Legal/provider gates, sandbox/staging E2E, risk policy, retention/recovery and explicit activation review |

No test-mode charge, merchant balance, DVA or internal label proves lawful safeguarding. The runtime cannot enable production provider operations by flipping a flag. External integration/custody restrictions are distinct from local contract correctness.

Completed foundations: every public role wallet, identity/binding, seven balance views, immutable double-entry engine and reversals, provider-scoped coverage, durable intents/routes, Paystack collection/customer/DVA/settlement adapters, inbox retries/replay, outbox/JetStream publication, verification/payout references, audited identity controls, gateway reads and Owner registration delivery. The static interfaces and live-disable gates reserve custody, payout, refund/chargeback and mandate responsibilities for authorized later workflows.

Stable entities and contracts: four Payment migrations, Owner registration migration 00019; core.Provision/Wallet/Posting/Entry/IdentityControl/PayoutReference; finance.IntentInput/Intent/DVAInput/DVA/ProviderConfig/Coverage; providers interfaces; contracts/internal/openapi/payment-service.yaml, contracts/public/openapi/payments.yaml, contracts/webhooks/paystack.yaml, contracts/events/payments/*.v1.schema.json and postman/payment-service.postman_collection.json.

Before beneficiary allocation: obtain approved legal owner/co-owner entitlement, manager mandate/fee-only scope, named individual/organization beneficiary verification, source quote/agreement versions, all-in cost/fee/tax policy and transaction/branch/property dimensions. No listing author automatically receives proceeds. Funding must remain in its original provider/entity/currency pool; use positive, balanced, immutable postings with policy/evidence and operation fingerprints.

Before lifecycle settlement/release: implement transaction-level settlement matching and independent custody/cash proof, liability/reserve reconciliation, milestone evidence and authoritative release approval, restrictions/deposit handling, idempotent state machines and the Owner payment.API compatibility translation. finance.payment.collected is collection evidence only; it must not be translated directly into payment.intent.updated HELD/RELEASED or booking confirmation. Existing Owner events and API contracts remain a separate later integration.

Deferred after Section 3: split/allocation engine; contractual holds and full milestone release; rent billing/recurring commission and legal fee decisions; complete withdrawals/OTP/recipient changes/cooldowns; full refunds, chargeback compensation and dispute remedies; generated business receipts/statements; treasury dashboards; bank-account reassignment; automatic bank/card debits and mandates; international/cross-border activation. Recorded bank references remain ineligible for execution. No Section 4 workflow was started.

Outstanding implementation qualifications: cross-domain linkage must precede second wallet provisioning; no automatic merge of existing identities. Ambiguous provider operations require original readback or operator resolution; they deliberately do not retry value movement. Full sandbox, cross-service real-process registration, production broker ACL, Docker-image, staging and live-custody certifications are not local PASS claims. See TEST_EVIDENCE.md and CLOSURE_REPORT.md for executed gates and exact repository handoff.
