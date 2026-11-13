# Business Operations Section 21 — Security, Privacy and Enterprise Controls

| Control | Implementation | Evidence |
|---|---|---|
| Tenant/scope authorization | Every new handler authorizes on the record's own scope (company, branch, property, development, parcel) through the live membership; records are always loaded by `(organisation_id, id)` | cross-tenant assertions in S12–S16 tests |
| Revoked staff | Authority evaluated per request and rechecked by workers before acting for a person | revoked author reply BLOCKED (S12); revoked sponsor pauses campaigns (S14) |
| Separation of duties | Approval engine forbids self-approval; generated `Distinct` transitions forbid the last actor/assignee (inspections, verifications, certifications, risk restriction approval, appeals) | housekeeping, work order, milestone, risk tests |
| Step-up MFA | Payout destination/changes, automation enablement, storefront publication, campaign launch, grants, approvals-bound transitions, dispute decisions, write-offs | Section 15/16 tests |
| Exact approvals | `requireCommercialApproval` (server-derived amount/currency, frozen policy with MFA + SoD) for new COMMERCIAL_* actions; Annex F privileged actions (`BUSINESS_PAYOUT_DESTINATION_CHANGE`, `EXTERNAL_MANAGER_ACCESS`, `PROFESSIONAL_DATA_ROOM_GRANT`, `BULK_DISPOSAL_OR_SALE`) via `requirePrivilegeChange` | approval-required 403 assertions in tests |
| Secrets | Provider/integration credentials are secret-manager reference names only (pattern-validated); never returned, logged or placed in Postman | connection/integration validation |
| Webhook/provider authenticity | New inbound internal routes require signed service identity, caller allowlist and organisation delegation; account tokens rejected | review intake test (account token → 401) |
| Privacy | No chat history stored or sent to Notification; previews expire with provider retention; Media references only for evidence; marketing consent rechecked at dispatch; legal hold on disputed reviews; data-room grants purpose/time-limited and revocable | S12/S13/S14/S16 tests |
| AI security | Retrieved/guest text never instructs; injection detection escalates; no action executes from AI; source allowlist and version recheck; provenance recorded | AI tests |
| Fraud/risk | Protected characteristics rejected; restrictions need a distinct reviewer; appeal path | risk test |
| Audit | Append-only `operations_history`, `audit_events`, assignment/attempt/version tables with immutability triggers | migrations 00031–00037 |

Scans (gitleaks, govulncheck, Trivy) run in the Security and Images workflows on the final SHA (see Section 24 report).
