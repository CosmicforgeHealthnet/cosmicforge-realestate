# Payment foundation implementation plan

Authority: `CosmicForge_Real_Estate_Payment_Wallet_Protected_Transactions_Master_Blueprint_v1.0.pdf`, v1.0, 10 October 2026, SHA-256 `fd1cfd14fb2f78ed7601fbafc49113f7d03049d3ef8041f6f08f0342d964e7f1`. The supplied PDF is the accepted readable source; its adjacent text is a searchable extraction. Starting branch `development`, commit `18cf3fab112827926c150813a70c983449b13a5f`, clean tree, remote fetch successful. No AGENTS.md found.

## Source-to-contract plan (written before implementation)

| Source | Assignable implementation | Evidence required |
|---|---|---|
| 1.1, 1.2, 1.3; D-002, D-011 | sole payment_db authority; signed service JWT; domain ownership; deny missing evidence; deferred workflows unavailable | authentication, isolation and startup tests |
| 2, 2.1; D-001, A04 | authenticated registration adapter; financial identity and domain bindings; role wallet; seven disjoint balance categories | PostgreSQL duplicate/concurrent provisioning and ownership tests |
| 2.2; A16 | immutable journals; minor units; currency/entity/pool segregation; transaction locks; compensations and coverage | database constraints, rollback, race, overspend and reconciliation tests |
| 3.1, 3.3; A10, A15, A21 | capability registry, durable route snapshots, provider interfaces, unknown result read-back, deny migrations without funding evidence | two controlled adapter contracts and route safety tests |
| 3.2; A11, A17 | Paystack checkout/verify, HMAC inbox, durable retries/outbox; settlement observations distinct from charge receipts | official API contract simulators, real database recovery tests; actual sandbox separately |
| 3.4; A19 | consent, authoritative verification reference, provider identification, titan-paystack assignment, unique merchant/customer mapping, requery | negative consent/identity tests and provider contract tests |

Repository reconciliation: Go/Chi/pgx/Goose/OpenAPI 3.1, UUIDv7, shared serviceauth, events, outbox, migrations and testkit will be reused. Payment service is a scaffold. Individual Owner retains tenancy, pricing, delegation and banking requests. Its deferred release/refund/payout contracts will not be represented as implemented. There is no central identity service: per-domain account authorities supply registration evidence. Cross-domain party linking requires explicit verified linkage rather than email/name matching. Organization and personal liabilities remain separate.

Only the Individual Owner implementation specification is present; the eight original v5 feature documents are unavailable and recorded as source dependencies. No rates, legal deadlines or release defaults are inferred. Pages 7–21 of the payment blueprint are compatibility constraints, not authorization to implement Sections 4 onward.

Sequence: Section 1 boundaries/config and tests; Section 2 wallets/ledger/migrations/tests; Section 3 providers/collections/inbox/DVA/settlement/tests; contract and integration gates; evidence/closure report; commit with no production activation. External Paystack sandbox credentials, Manual Payouts enrollment, DVA eligibility, licensed custody, commercial/legal approvals and staging certification require independent evidence.
