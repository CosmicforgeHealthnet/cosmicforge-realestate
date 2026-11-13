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
