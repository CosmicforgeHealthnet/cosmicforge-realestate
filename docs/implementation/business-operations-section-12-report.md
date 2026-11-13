# Business Operations Section 12 — Unified Communication and AI Operations Inbox

Branch `staging`. Migration `00031_communications_inbox.sql`. Implementation: `internal/http/operational_comms.go`, `operational_comms_ai.go`, `operational_comms_worker.go`, `internal/automation/grounding.go`, `internal/integrations/cfmeet/contract.go`, `internal/integrations/messaging/adapter.go`.

## Boundary

| Concern | Authority | Business Operations stores |
|---|---|---|
| Native human conversation | CF Meet | Context reference (`communication_contexts.external_conversation_ref`) assigned by CF Meet only; no message bodies (`inbox_message_receipts` CHECK forbids a CF Meet preview) |
| External channel threads | Provider | Provider message id, source time, content digest, optional expiring preview only when the connection and adapter both permit it |
| Workflow | Business Operations | Threads, assignment history, SLA, quick replies, scheduled reply commands, AI suggestions, action cards, automation rules, support cases |
| Alerts | Notification Service | Nothing from conversations is sent; a test asserts no conversation content reaches notification jobs |

## Implemented behaviour

| Requirement | Implementation | Evidence |
|---|---|---|
| Provider registry, capabilities, retention, credential reference names only | `messaging_connections` (state guard DISCONNECTED→AUTHORIZING→CONNECTED→DEGRADED/SUSPENDED→DISCONNECTED); CONNECTED requires a registered adapter covering every configured capability (CF Meet requires a non-`Unavailable` port) | `TestBusinessInboxCFMeetAuthorityPendingThenEstablishedWithoutMessageStorage` |
| CF Meet context authority | Contexts start `PENDING_AUTHORITY`; `ProcessCommunicationContexts` calls `EnsureContext` outside transactions (context id = idempotency key); unavailable contract records `CF_MEET_CONTRACT_UNAVAILABLE`, never a fabricated reference | same test |
| Durable inbox sync, dedupe, replay, health | Leased/fenced `inbox_sync_cursors`; receipts unique on (connection, provider message id); three poll failures → DEGRADED, recovery → CONNECTED | `TestBusinessInboxExternalSyncDeduplicatesAndScheduledReplySendsOnceWithReadback`, `TestBusinessInboxProviderOutageDegradesAndRecoversWithoutDuplicates` |
| Unified multi-property inbox, filters, search, assignment, SLA | `/inbox` lists only properties where the caller holds `inbox:read`; assignment needs a current member with inbox access; first-response/resolution due computed from `inbox_sla_policies` (defaults by priority) | sync test (SLA due = source time + 1h) |
| Read state | Reported only when the provider supports it; otherwise `READ_STATE_UNAVAILABLE` (409) | sync test |
| Quick replies, scheduled replies, drafts, cancel | `quick_reply_templates` (declared variables only); `scheduled_replies` state guard; cancel only before dispatch | sync test |
| Send at most once; unknown outcome | Leased claim committed before the provider call; backoff honoured under the row lock; ambiguous result → `UNKNOWN_PENDING_READBACK`; readback with the same client message id → `SENT` only on provider acknowledgement | sync test (three concurrent dispatchers, one provider send) |
| Revocation at dispatch | Author's recorded authority (`currentRecordedPMSAuthority`) rechecked; revoked → `BLOCKED` | sync test |
| AI factual answers | `automation.Ground`: approved, unexpired, versioned sources only; citations; refuses price/availability, multi-topic, unsupported facts; reservation facts only from the context's bound reservation; prompt-injection content escalates; model/engine recorded | `TestGround*` unit tests; `TestBusinessInboxAIGroundingInjectionActionCardsAndApprovedAutoReply` |
| AI attribution and acceptance | Accepted answers become scheduled replies prefixed with an automated-answer attribution; cited source versions rechecked at acceptance | AI test |
| Approved auto-reply | `AI_FACTUAL_AUTOREPLY` rule requires MFA and an exact independent `COMMERCIAL_AI_AUTOREPLY_ACTIVATION` approval; sponsor authority rechecked at creation and dispatch; disable is an MFA-free kill switch that cancels pending automated replies | AI test |
| Action cards | Proposal only: required capability/approval/owning route recorded; acceptance requires the operator to hold the owning capability and the target version to be current; EXECUTED only with an owning-workflow record as evidence | AI test, `TestBusinessSupportCaseLifecycleAndSLA` |
| Support cases | Governed lifecycle with SLA by severity, escalation marking by worker | support test |

## Workers

`ProcessCommunications` (per-second loop): context establishment, inbox sync, scheduled reply dispatch/readback, SLA breach marking and rule-based escalation, suggestion/card expiry.

## External dependencies (not internal gaps)

* CF Meet internal contract: not published in this repository. Default `cfmeet.Unavailable`; contexts remain pending. A signed contract double certifies the Business side only.
* Marketplace/OTA/direct messaging adapters: disabled by default (`messaging.Disabled`). No provider endpoint is invented.
* No configured model provider: the deterministic grounded engine is the certified behaviour; no inference result is fabricated.
