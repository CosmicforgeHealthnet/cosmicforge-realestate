# Business Operations Section 22 — Resilience and Concurrency

| Requirement | Implementation | Evidence |
|---|---|---|
| Inventory safety | Unchanged canonical inventory fencing (Sections 7–10); new allocations use a one-live-per-unit unique index; upsell capacity checked under the offer row lock | allocation test (double hold → 409); upsell capacity test |
| Provider delivery idempotency | Client message id = scheduled reply/response id; leased claims committed before the call; provider calls outside transactions; ambiguous → readback | three concurrent dispatchers → one send; response readback test |
| Inbound dedupe | Unique provider message ids, review source identity/version, funnel source references, lead dedupe keys, campaign members | S12/S13/S14 tests |
| Outage isolation | Per-connection degradation; other channels and internal operations unaffected; CF Meet unavailable keeps contexts pending | provider outage test |
| Transactions | Network calls outside DB transactions; state + history + outbox atomic; compensations via explicit states (BLOCKED, UNKNOWN_PENDING_READBACK, exceptions) | all worker code |
| Offline operations | Existing PMS offline replay unchanged; `/offline-sync` and `/reconciliation-tasks` are aliases to the same handlers | existing offline tests |
| Recovery | Lease expiry lets another worker resume (scheduled replies, review responses, report jobs, cursors); duplicate deliveries no-op | worker tests |

Backups/off-server restore are not certified here (no environment access).
