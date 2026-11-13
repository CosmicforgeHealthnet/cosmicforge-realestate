# Business Operations Section 19 — Background Workers

All workers run inside the existing worker process (`cmd/worker` → `internal/workers/runtime.go`); no second scheduler was introduced. Workers act under the explicit service identity (`business.actor_service`), never a human session or MFA, and recheck recorded human authority (sponsor/author membership and grants) where they act on a person's behalf.

| Required worker | Implementation | Leasing / fencing | Notes |
|---|---|---|---|
| Transactional outbox relay | `outbox.NewWorker` (existing) | owner lease | — |
| Channel outbound / inbound / reconciliation | `ProcessChannelSync`, signed receipts (existing) | job leases | — |
| Reservation hold expiry | `ProcessReservationMaintenance` (existing) | — | — |
| Availability/freshness | `ProcessCommercialMaintenance` (existing) | — | — |
| Revenue forecast / pricing publisher | `ProcessRMSAutomation`, `ProcessRMSMaintenance` (existing) | lease generations | — |
| Housekeeping scheduler | **new** `scheduleTurnovers` in `ProcessOperations` | `ON CONFLICT` on one-live-task index | from canonical `room_status_current = VACANT_DIRTY` |
| Night audit / cashier reconciliation | `ProcessPMSDayAutomation`, `ProcessHospitalityMaintenance` (existing) | — | — |
| Preventive maintenance | **new** `schedulePreventive` | `FOR UPDATE SKIP LOCKED`, next-due advanced atomically | one work order per due period |
| Verification/licence expiry | `Verification.Tick`, `Lifecycle.Tick` (existing) | — | — |
| Manager/staff access expiry | **new** manager EXPIRING/EXPIRED sweeps; existing temporary access expiry | state guard | revocation immediate on API |
| Privacy retention / legal hold | `ProcessDataRetention` (existing) | — | — |
| Analytics snapshots | **new** `ProcessReports` | lease generation | requester authority rechecked; immutable snapshot |
| External import | `ProcessInventoryImports` (existing) | checkpoints | — |
| Smart-access expiry/health | `ProcessPMSAccess` (existing) + **new** business access grant expiry | — | — |
| Inbox sync | **new** `ProcessInboxSync` | cursor lease owner + generation | degrade after 3 failures |
| Scheduled reply dispatch | **new** `ProcessScheduledReplies` | claim lease + generation; backoff under row lock | readback before retry |
| Inbox SLA escalation | **new** `ProcessInboxSLA` | `SKIP LOCKED` | rule-gated priority escalation; support SLA marking |
| AI suggestion generation | **new** auto-reply inside inbox sync (rule-gated, `automation_runs` uniqueness) | per-receipt uniqueness | — |
| Review import | **new** `ProcessReviewImport` | cursor lease | — |
| Review response dispatch | **new** `ProcessReviewResponses` | lease + generation | readback; retract |
| Sentiment/topic extraction | inline at intake (idempotent unique key) | — | — |
| Quality escalation / monitoring | inline issue/task + **new** `ProcessQualityMonitoring` | — | — |
| Lead follow-up | **new** `ProcessLeadFollowUps` | `SKIP LOCKED` | one FOLLOW_UP_DUE per schedule |
| Campaign scheduling/dispatch | **new** `ProcessCampaigns` | unique (campaign, guest) | sponsor revocation pauses; consent per member |
| Storefront publication | outbox `storefront.updated.v1` | — | readback unavailable (external) |
| Conversion projection | `projectConversion` (idempotent, unique source references) | — | — |
| Finance reconciliation / exception monitoring | **new** `ProcessFinance` (payout cooling-off, remittance reconciliation, expense expiry) | `SKIP LOCKED` | Payment re-verification |
| Support SLA escalation | inside `ProcessInboxSLA` | — | — |

Scheduling: `ProcessCommunications` runs every second; `ProcessReputation`, `ProcessMarketing`, `ProcessFinance`, `ProcessOperations`, `ProcessReports` run in the existing one-minute maintenance loop. Failures are logged per domain and retried on the next tick; quarantined/blocked states require an authorised operator action (cancel, retry, resolve).

## Addendum — migration 00038 date-driven sweeps

All run inside `ProcessOperations` (Section 16 worker) under the explicit service identity, per-record transaction, `FOR UPDATE` lock, database-guarded edge and idempotent re-run (an already-moved record yields INVALID_TRANSITION and is skipped). Generated into `opsGeneratedSweeps`.

| Domain | From → to | Trigger column | Action |
|---|---|---|---|
| rental_offer | ISSUED → EXPIRED | `ends_at` <= now | OFFER_EXPIRED |
| rent_obligation | SCHEDULED → DUE | `starts_at` <= now | OBLIGATION_DUE |
| rent_obligation | DUE/LINKED → OVERDUE | `ends_at` <= now | OBLIGATION_OVERDUE |
| notice | SERVED/ACKNOWLEDGED → EFFECTIVE | `starts_at` <= now | NOTICE_EFFECTIVE |
| renewal_case | OPEN/OFFERED → LAPSED | `ends_at` <= now | RENEWAL_LAPSED |
| commercial_loi | ISSUED → EXPIRED | `ends_at` <= now | LOI_EXPIRED |
| land_allocation | PROVISIONAL → RELEASED | `ends_at` <= now | ALLOCATION_HOLD_EXPIRED |
| sale_allocation | HELD → EXPIRED | `ends_at` <= now | UNIT_HOLD_EXPIRED |
| warranty | ACTIVE → EXPIRED | `ends_at` <= now | WARRANTY_EXPIRED |
| service_contract | ACTIVE/EXPIRING → EXPIRED | `ends_at` <= now | CONTRACT_EXPIRED |
| service_contract | ACTIVE → EXPIRING | `ends_at` within 30 days | CONTRACT_EXPIRING |
| risk_signal | OBSERVED → EXPIRED | `ends_at` <= now | SIGNAL_EXPIRED |
| financing_consent | GRANTED → EXPIRED | `ends_at` <= now | CONSENT_EXPIRED |
| api_credential | ACTIVE/ROTATING → EXPIRED | `ends_at` <= now | CREDENTIAL_EXPIRED |
| fx_rate_ref | ACTIVE → STALE | `ends_at` <= now | FX_RATE_STALE |
| content_suggestion | SUGGESTED → EXPIRED | `ends_at` <= now | SUGGESTION_EXPIRED |
| market_benchmark | ACTIVE → STALE | `ends_at` <= now | BENCHMARK_STALE |
| tenancy rent schedule | periodic tenancies kept 3 scheduled periods ahead | `scheduleRentObligations` | SCHEDULED |

Tested: residential journey (DUE→OVERDUE), sale hold expiry, warranty expiry, service contract EXPIRING, FX STALE.
