# Business Operations — BO-1 to BO-18 workstream status

Historical section reports are cited as evidence; verdicts reflect code and tests
present at the end of this pass.

| Workstream | Verdict | Basis |
|---|---|---|
| BO-1 Bootstrap/auth/KYB | COMPLETE (internal); KYB partner EXTERNAL_ACTIVATION_DEFERRED | Section 3 report; auth/MFA/capacity tests |
| BO-2 Organisation/RBAC/approvals | COMPLETE | approvals, delegation, SoD tests; new organisation change/closure cases (D.25) |
| BO-3 Portfolio/inventory | COMPLETE | Section 4 reports; inventory graph/import tests |
| BO-4 Authority/verification/content | COMPLETE (Verification owns authority — equivalence) + content suggestions (D.40) | inventory verification tests; `TestAnnexDRiskSignalsProtectionIncidentsAndContentSuggestions` |
| BO-5 Inventory/rates | COMPLETE | Section 7 certification |
| BO-6 Channel Manager | COMPLETE internally; first-party Marketplace receiver now exists; OTA partners EXTERNAL_ACTIVATION_DEFERRED | Section 6 tests; marketplace-service tests |
| BO-7 RMS | COMPLETE; licensed market data EXTERNAL_ACTIVATION_DEFERRED (benchmarks are source/licence-referenced) | RMS suites; `market_benchmarks` |
| BO-8 CRM/marketing/support/reputation | COMPLETE; CF Meet contract external | Sections 11–14 tests |
| BO-9 Residential/commercial/land | COMPLETE | 00038 + Annex D journeys 7–9 |
| BO-10 Sales/development/off-plan | COMPLETE | Annex D journeys 10–11; strengthened milestone certification |
| BO-11 Short-let | COMPLETE | short-let settings, incidents, turnover equivalence |
| BO-12 Hotel PMS | COMPLETE | PMS suites |
| BO-13 Housekeeping/night audit/cashier | COMPLETE | checklists, lost & found, linen; night audit tests |
| BO-14 Maintenance/vendors/assets | COMPLETE (Business side); Artisan owning service NEEDS_INTEGRATION | Annex D maintenance journey |
| BO-15 Managers/professionals/data rooms | COMPLETE (Business side); Property-manager and Professional-services owning services NEEDS_INTEGRATION | manager/professional tests |
| BO-16 Analytics/AI/automation | COMPLETE | analytics reports, experiments, AI suggestions |
| BO-17 Risk/privacy/financing/insurance/access | COMPLETE (Business side); partner/provider activation deferred | Annex D financing/insurance/access/risk tests |
| BO-18 Full certification | PARTIAL — see final closure: four Category B owning services and process-level E2E for most cross-service journeys remain | `business-operations-final-backend-system-closure.md` |
