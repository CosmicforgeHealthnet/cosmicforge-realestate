# Business Operations — Annex D.1–D.42 closure

Baseline `52592e8` (staging). This pass added migration `00038_annex_d_lifecycles.sql`
(45 governed tables), the Annex D rule set in
`services/business-operations-service/internal/http/operational_domains_annex_d.go`,
and the named tests in `tests/integration/annex_d_lifecycles_test.go` and
`tests/integration/annex_d_upgrade_test.go`. All tests named here ran against a real
disposable PostgreSQL 16 database (see the certification report for the run).

Every new lifecycle is generated from one declarative source
(`scripts/contracts/business_ops_domains.py`) into: the migration (tables, tenant
foreign keys, CHECK constraints, unique "one live" indexes and a database state-guard
trigger whose edges equal the API transitions), the Go domain table, the route registry,
OpenAPI and Postman. Every command goes through the shared engine, which enforces
tenant/scope RBAC, recent MFA, exact independent approvals (bound to record version and
authoritative amount), separation of duties, evidence references, Idempotency-Key,
optimistic version checks, immutable `operations_history`, `audit_events` and a
transactional outbox event.

Status values: `PASS_VERIFIED` = implemented and exercised by a named executable test;
`EQUIVALENT_IMPLEMENTATION_VERIFIED` = served by an existing model, proven by a named
test; `EXTERNAL_ACTIVATION_DEFERRED` = internal contract complete, real provider/partner
activation outstanding; `NEEDS_INTEGRATION` = depends on an unfinished owning service.

| Annex | Feature | Status | Implementation / evidence |
|---|---|---|---|
| D.1–D.3 | Capacities, registration/security/KYB, organisation/workforce | PASS_VERIFIED (prior sections, regression-run) | Sections 3 reports; `capacity_verification_test.go`, `auth_*`, `staff_*`, `approvals_http_test.go` |
| D.4 | Banking/finance approvals | PASS_VERIFIED (prior) | `s15_finance_test.go` (payout cooling-off, split conservation, readback) |
| D.5 | Property identity / hierarchy | PASS_VERIFIED (prior) | `inventory_graph_test.go`, `hierarchy_test.go` |
| D.6 | Ownership / property authority | EQUIVALENT_IMPLEMENTATION_VERIFIED | Verification owns authority; Business projects `inventory_verification_links` + history. `TestBusinessInventoryAuthoritativeVerificationActivationAndExpiry` |
| D.7 | Bulk listing / import | PASS_VERIFIED (prior) | `inventory_import_test.go` |
| D.8 | Leads / engagement | PASS_VERIFIED (prior) | `s14_marketing_test.go` |
| D.9 | Residential rental lifecycle | PASS_VERIFIED | New: `rental_offers`, `agreements` (versioned, one SIGNED version per contract, DB-enforced), `rent_obligations` (generated schedule from activated tenancy; worker due/overdue; settlement only from Payment-readback `financial_obligations`), `deposit_obligations` (held only after verified settlement; release request bound by approval to the refund; deductions must equal the settled move-out case), `notices` (termination notice moves tenancy to NOTICE_GIVEN), `renewal_cases` (successor contract validation), `move_out_cases` (closure ends tenancy and cancels future rent), `utility_accounts` (counterparty required; transfer needs provider reference then evidence), `/residential/arrears` projection. Tenancy activation now requires a signed agreement. `TestAnnexDResidentialRentalLifecycleFromOfferToDepositRelease` |
| D.10 | Commercial operations | PASS_VERIFIED | `commercial_lois`, `commercial_terms` (approved terms supersede prior), `service_charge_budgets` (approval, reconciliation variance by a distinct actor), service-charge obligations on the deal; lease commencement requires a signed COMMERCIAL_LEASE agreement. `TestAnnexDCommercialLOITermsLeaseServiceChargeLifecycle` |
| D.11 | Land | PASS_VERIFIED | `land_terms` (parcel-scoped child), `land_allocations` (one live allocation per parcel; confirmation requires Verification title reference + approval; perfection by distinct actor), parcel-scoped `due_diligence_items` / `closing_milestones`; land approval/completion gated on conditions and milestones. `TestAnnexDLandTitleAllocationDueDiligenceAndClosingMilestones` |
| D.12 | Property sales | PASS_VERIFIED | `sale_allocations` (unit holds, expiry, release on withdraw/fall-through, conservation against off-plan allocations), DD gate on CONTRACT, payment milestones proven only by settled Payment-readback obligations, closing gate on COMPLETE. `TestAnnexDSaleOfferUnitHoldConditionsPaymentMilestonesClosing` |
| D.13 | Developer / off-plan | PASS_VERIFIED | `progress_certifications` (professional reference + evidence + MFA + distinct actor); milestone CERTIFY now requires a certified progress certificate; `project_variations` (costed, approval bound to cost); buyer allocation conservation. `TestAnnexDOffPlanProgressCertificationVariationAndConservation`, updated `TestBusinessDevelopmentAllocationMilestonesSnagsAndHandover` |
| D.14 | Short-let | PASS_VERIFIED | `shortlet_settings` (one active house-rule version, supersession), `incident_cases`; turnover = `housekeeping_tasks` TURNOVER (equivalence). `TestAnnexDHospitalityChecklistsLostFoundLinenSettingsServicesExperiments`, `TestAnnexDRiskSignalsProtectionIncidentsAndContentSuggestions` |
| D.15 | Hotel structure | PASS_VERIFIED | `property_service_catalogue` (priced services with pricing unit). Same hospitality test |
| D.16, D.18 | Reservations/front desk, channel/pricing/RMS | PASS_VERIFIED (prior, regression-run) | Sections 6–10 suites |
| D.17 | Housekeeping / guest service | PASS_VERIFIED | `housekeeping_checklists` — tasks bound to an active checklist cannot be submitted until every item is recorded; `lost_found_items` (90-day retention, documents/cash never disposed, distinct return); `linen_stock_refs` (par-level counts). Hospitality test above |
| D.19 | Property managers | PASS_VERIFIED + EQUIVALENT | `manager_performance_refs` new; permissions = assignment capability list (equivalence). `TestAnnexDManagerPerformanceAndProfessionalFindingConditionLink`, `TestBusinessManagerDelegationScopedExpiringRevocableAndEnforced` |
| D.20 | Maintenance / vendors / assets | PASS_VERIFIED | `vendor_quote_refs` (independent acceptance, one accepted quote; work-order approval bound to the accepted quote amount), `provider_assignment_refs` (completion before work-order completion), `warranties` (expiry worker), `service_contract_refs` (EXPIRING/EXPIRED worker), `/assets/{id}/service-history`. `TestAnnexDMaintenanceQuotesProviderWarrantyAndAssetHistory` |
| D.21 | Professional services / data rooms | PASS_VERIFIED | `professional_findings` (from active appointment, evidence, distinct acceptance, linked to open due-diligence condition). Manager/professional test above |
| D.22 | Support / communications | EQUIVALENT_IMPLEMENTATION_VERIFIED | `communication_contexts`. `TestBusinessInboxCFMeetAuthorityPendingThenEstablishedWithoutMessageStorage` |
| D.23 | Analytics | PASS_VERIFIED | `market_benchmarks` (licensed, source-backed, staleness worker), `experiments` (MFA + distinct start). Hospitality test above; reports from prior sections |
| D.24 | Privacy / fraud / disputes | PASS_VERIFIED + EQUIVALENT | `risk_signals` (source-backed, protected attributes refused, never restrict on their own); dispute evidence equivalence. `TestAnnexDRiskSignalsProtectionIncidentsAndContentSuggestions`, `TestAnnexDEquivalenceDisputeEvidenceIntegrationHealthAccounting` |
| D.25 | Migration / business changes / closure | PASS_VERIFIED + EQUIVALENT (scope noted) | `organisation_change_cases` (Verification reference + approval before APPLY; name change applied with previous name in history), `closure_cases` (blocked by live tenancies, deposits, reservations, open financial obligations or disputes; retention ≥ 1 year; organisation moves to CLOSURE_PENDING). Resumable assisted migration = inventory import jobs (checkpointed preview/commit/pause/resume/rollback) — this covers portfolio/inventory data only; reservation, guest and ledger history import from a legacy PMS is not implemented and would be a new import family. Closure CLOSE (after retention, organisation `CLOSURE_PENDING`) requires `organisation:verify` + MFA + a distinct actor and sets the organisation `CLOSED`; it was previously unreachable because `CLOSURE_PENDING` restricts `organisation:write`. `TestAnnexDFXFreshnessOrganisationChangeAndClosureContinuity`, `inventory_import_test.go` |
| D.26 | Financing | PASS_VERIFIED | `financing_consents` (explicit data-scope allow-list, evidence, expiry ≤ 1 year); SUBMIT to a partner requires a current DATA_SHARING consent. Partner lending: EXTERNAL_ACTIVATION_DEFERRED. `TestAnnexDFinancingConsentInsuranceComplianceAndAccessOutcomes` |
| D.27 | Insurance | PASS_VERIFIED | `insurance_compliance_checks` evaluated from policy references (kind, cover amount, coverage through date) to COMPLIANT / NON_COMPLIANT, remediation/waiver. Underwriting/settlement: EXTERNAL_ACTIVATION_DEFERRED. Same test |
| D.28 | Smart access | PASS_VERIFIED | `access_events` (issue/revoke commands; UNKNOWN results never activate a grant; only provider confirmation or independent manual resolution changes the grant), `device_health_snapshots` (OFFLINE/TAMPER marks device offline). Physical lock certification: EXTERNAL_ACTIVATION_DEFERRED. Same test |
| D.29–D.34 | Channel manager, RMS, command center, CRM, reputation, AI | PASS_VERIFIED (prior, regression-run) + EQUIVALENT (`ai_recommendations`) | Sections 6–14 suites |
| D.35 | Direct commerce | EQUIVALENT_IMPLEMENTATION_VERIFIED | `promotion_codes` = `promotions`. `TestBusinessCommercialApprovedPromotionPackageAddOnPolicyPrecedence` |
| D.36 | Integration / partner platform | PASS_VERIFIED | Configuration: `integration_clients` (scopes, whole-number rate limit 1–6000/min, environment SANDBOX default or PRODUCTION), `api_credentials` (secret-manager reference + SHA-256 fingerprint only; raw secrets refused; fingerprints globally unique among live credentials; rotation; expiry), `webhook_subscriptions` (public HTTPS only; HMAC signing-secret reference; verification), suspension/revocation cascades. Runtime (migration 00039): partner API `GET /partner/v1/me`, `GET /partner/v1/reservations` authenticated by `X-CF-Partner-Key` (fingerprint lookup through an ACTIVE client and unexpired credential; both keys during rotation), per-route client scope, per-credential one-minute window in `partner_api_rate_windows` shared by all replicas (429 + Retry-After), SANDBOX credentials receive synthetic fixtures only, PRODUCTION reads only the owning organisation through a guest-PII-free projection; partners never get database access. `TestAnnexDPartnerCredentialsWebhooksRotationAndRevocation`, `TestAnnexDPartnerAPIScopesRateLimitSandboxRotationAndRevocation`. Outbound webhook *delivery* to partner endpoints is not implemented (subscriptions are configuration + verification only). |
| D.37 | Tax / currency / fiscal | PASS_VERIFIED + EQUIVALENT | `fx_rate_refs` (source-backed decimal string rate, ≤ 7-day freshness, distinct activation supersedes prior pair, STALE worker); tax rules = `tax_profiles`. Display/settlement consistency is enforced by single-currency pricing: quotes, folios and settlement use the rate-plan currency (folio currency changes refused, night audit blocks `RATE_CURRENCY_MISMATCH`, no implicit FX). FX references are recorded reference data and are **not** applied to prices; multi-currency display is not offered. `TestAnnexDFXFreshnessOrganisationChangeAndClosureContinuity`, `pms_days_test.go` |
| D.38 | Night audit / close | EQUIVALENT_IMPLEMENTATION_VERIFIED | `close_exceptions` = `night_audit_exceptions` + `financial_exception_cases` |
| D.39 | Group / corporate / events | PASS_VERIFIED (prior) | `pms_groups_test.go` |
| D.40 | Content intelligence / digital twin | PASS_VERIFIED | `content_suggestions` (source-backed with evidence; operator acceptance by a distinct actor; edited acceptance; publication references a content version). 3D processing provider: EXTERNAL_ACTIVATION_DEFERRED |
| D.41 | Offline continuity / DR | PASS_VERIFIED + EQUIVALENT | `branch_incidents`; reconciliation tasks = `offline_reconciliation_tasks`. Off-server backup/restore: LIVE_DEPLOYMENT_DEFERRED |
| D.42 | Trust / reservation risk / protection | PASS_VERIFIED | `protection_case_refs` (from live risk case, external reference lifecycle), risk signals advisory only. Risk test above |

## Strengthened existing gates (behaviour change, tests updated, not weakened)

- Tenancy `ACTIVATE` requires a SIGNED `RESIDENTIAL_TENANCY` agreement version.
- Commercial deal `ACTIVATE` (LEASE/LICENCE) requires a SIGNED `COMMERCIAL_LEASE` agreement.
- Construction milestone `CERTIFY` requires a CERTIFIED progress certification.
- Work-order `APPROVE`, when quotes exist, requires an accepted quote and binds the approval to its amount.
- Work-order `COMPLETE` requires the assigned provider's recorded completion.
- Financing `SUBMIT` requires a current data-sharing consent.
- Land `APPROVE`/`COMPLETE` and sale `CONTRACT`/`COMPLETE` require satisfied/waived conditions and confirmed/waived closing milestones.
- Engine: transition amounts are now computed after the domain rule, so approvals bind to authoritative amounts; create records carry their currency to rules.
