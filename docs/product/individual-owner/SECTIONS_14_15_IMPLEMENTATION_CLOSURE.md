# Individual Owner Sections 14–15 implementation closure

Authority: the user's full Sections 14–15 prompt, original Owner specification pages 24–26, and the retained Payment blueprint. Scope stops after Owner Section 15. Baseline: development `44cce026f9586672c4bae72a977c7f002adb532c`, matched by origin, clean working tree. Historical Sections 12–13 remain unchanged evidence, not automatically a fresh PASS.

This is the single combined coverage/gap register and final closure report. Final Git metadata is exported after commit to the ignored evidence directory because a tracked report cannot contain its own commit SHA.

Paths below: **U** = `services/individual-owner-service/internal/usecases/`; **T** = `services/individual-owner-service/tests/integration/`; paths such as internal/authz are under Owner unless explicitly qualified. Every cited test function was checked against the actual source. **E** = `tests/e2e/owner_functional_test.go:TestOwnerFunctionalJourneyWithRealServices`, extended by `owner_primary_journeys_test.go` within the same invocation. **F** = `services/payment-service/tests/integration` parent `TestActualOwnerFinancialCoreConsumer` and its existing tagged Owner child in T/financial_core_cross_test.go and T/core_workflow_cross_test.go. Provider money, approval, consent and source actors in F are explicit TEST fixtures.

## All 42 primary-journey steps

Local statuses concern the available Owner/backend and supported contract boundary. The external column is part of each verdict, never a claim that a missing producer/provider executed. E's offline attestations and test templates are not bank settlement, approved legal policy or registry approval. Financial subchains reuse F, not a new money engine.

| Step | Exact requirement | Existing enforcement reused | Actual new work | Verification | Local | External status / exact outstanding requirement |
|---|---|---|---|---|---|---|
| 14.1.1 | Registration/authentication | U/accounts.go; login.go; lifecycle.go | Reuse; E2E Windows harness portability | T/auth_test.go:TestRegistrationVerificationActivationAndEvent; E/F as applicable | PASS | Production OAuth/email/KYC remains independent |
| 14.1.2 | Identity case | U/verification.go | Case/subject/type/aggregate binding strengthened | T/property_test.go:TestIdentityVerificationEventsAreIdempotentAndOrdered; E/F as applicable | PASS | Real Verification sandbox; production KYC absent |
| 14.1.3 | Property record/claim | U/property.go; ownership.go | IANA timezone validation | T/property_test.go:TestPropertyLifecycleOwnershipVerificationAndEvents; E/F as applicable | PASS | Registry/title evidence must be actual |
| 14.1.4 | Authority media | U/integration.go; property_parts.go | Reuse real Media process/upload | T/property_test.go:TestUnitsParcelsAssetsMediaAndImports; E/F as applicable | PASS | Storage fixture is S3-compatible simulation; production scanner/storage not certified |
| 14.1.5 | Property authority case | U/verification.go | Binding checks; existing case/evidence API reused | T/property_test.go:TestPropertyLifecycleOwnershipVerificationAndEvents; E/F as applicable | PASS | Registry provider/test adapter is not legal approval |
| 14.1.6 | Verification projections/readback | U/verification.go; jobs_property.go | Verified cases included in lost-expiry readback | T/property_test.go:TestVerificationSubjectBindingAndLostExpiryReadback; E/F as applicable | PASS | Source supports EXPIRED; restrictions handled separately; no invented REVOKED event |
| 14.1.7 | Transaction readiness | U/property.go; authority.go; authz | Reuse gated readiness/publication separation | T/property_test.go:TestPropertyLifecycleOwnershipVerificationAndEvents; E/F as applicable | PASS | Jurisdiction approval unchanged |
| 14.1.8 | Listing/pricing/availability | U/listing.go; pricing.go; availability.go | Reuse immutable versions | T/listing_test.go:TestListingPublicationPricingAndVersions; E/F as applicable | PASS | Marketplace consumer absent |
| 14.1.9 | Atomic publication/Marketplace event | U/listing.go; domainbase.go | Existing outbox/NATS; linked process chain extension | T/events_test.go:TestAccountEventsRelayToJetStreamWithRecoveryAndContracts; E/F as applicable | PASS | Real Marketplace deployment/recipient projection pending |
| 14.2.1 | Discovery | U/listing.go | Reuse scoped public view | T/listing_test.go:TestListingMediaMarketplaceAccess; E/F as applicable | PASS | Marketplace signed simulator only |
| 14.2.2 | Inquiry/viewing/application | U/leads.go; viewings.go; applications.go | Link existing application/offer endpoints in process harness | T/leads_test.go:TestApplicationsScreeningDecisionsAndOffers; E/F as applicable | PASS | Marketplace source/consent fixtures; actual customer backend absent |
| 14.2.3 | Communication | U/announcements.go; dispatch.go | Real Notification SMTP adapter to local TEST sink on Windows | T/announcements_test.go:TestGuestAnnouncementsThroughRecipientDomain; E/F as applicable | PASS | CF Meet and actual tenant/guest delivery absent |
| 14.2.4 | Human screening/evidence | U/applications.go; evidence.go | Reuse purpose-bound evidence/human decision | T/leads_test.go:TestApplicationsScreeningDecisionsAndOffers; E/F as applicable | PASS | No automatic screening authority introduced |
| 14.2.5 | Offer/pricing snapshot | U/applications.go; accepted_collection.go | Reuse accepted terms and financial once-fee contracts | T/tenancy_test.go:TestResidentialLeaseLifecycle; E/F as applicable | PASS | Rates/legal services not invented; source acceptance required |
| 14.2.6 | Agreement/signatures | U/agreements.go; templates.go | Durable original-envelope recovery; real-process offline signing | T/tenancy_test.go:TestElectronicSignatureReconciliation; E/F as applicable | PASS | Real e-sign/legal templates not approved; process templates are TEST |
| 14.2.7 | Rent/deposit collections | U/core_rent_context.go; core_financial_workflows.go; obligations.go | Reuse separate Core process stories; local rent clock corrected | T/financial_report_projection_test.go:TestFinancialReportsExcludeRefundedRent; E/F as applicable | PASS | Partial reversal certified; true partial capture/installments unsupported; terminal bank refunds/custody blocked |
| 14.2.8 | Tenancy conditions/activation | U/agreements.go; tenancy.go | Property-local initial-obligation date and accepted start-date move-in guard | T/tenancy_test.go:TestResidentialLeaseLifecycle; E/F as applicable | PASS | Core charge alone never means funding/release; law/provider independent |
| 14.2.9 | Move-in evidence/checklist | U/tenancy.go | Link inspection/acknowledgement in real-process chain | T/tenancy_test.go:TestResidentialLeaseLifecycle; E/F as applicable | PASS | Offline artifacts and customer attestations TEST only |
| 14.2.10 | Maintenance/notices/renewal | U/tenancy_cases.go; maintenance.go; obligations.go | Property-local due/arrears date; reuse renewal/version guards | T/tenancy_test.go:TestRentArrearsLateFeesAndWaivers; E/F as applicable | PASS | Legal policy approval and actual providers pending; saved debit deferred |
| 14.2.11 | Move-out/deposit/reopening | U/tenancy_cases.go; core_financial_actions.go | Link inspection/keys/reopening/restart in process harness | T/tenancy_test.go:TestResidentialLeaseLifecycle; E/F as applicable | PASS | Core partial refund fixture reused; external finality and lawful custody not certified |
| 14.3.1 | Verified land/commercial/listing | U/commercial.go; property.go; listing.go | Add actual land-sale process chain | T/sale_test.go:TestCommercialTermsLOIAndLandTerms; E/F as applicable | PASS | Corporate receiving flow blocked without real corporate identity/authority |
| 14.3.2 | Buyer/offer/proof references | U/sale.go | Link signed buyer offer and accepted transaction | T/sale_test.go:TestPropertySaleOffersClosingAndDataRoom; E/F as applicable | PASS | Actual buyer/legal signatory producer absent; proof references TEST |
| 14.3.3 | Professional/data-room access | U/professionals.go; sale.go; evidence.go | Scoped Owner-domain Media proxy, final live-grant check and URL/purpose expiry guard; reuse appointment/report controls | T/maintenance_test.go:TestProfessionalAppointmentDueDiligence; E/F as applicable | PASS | Professional backend absent; simulator/contract only |
| 14.3.4 | Due diligence/conditions | U/sale.go | Link blocking diligence and human resolution | T/sale_test.go:TestPropertySaleOffersClosingAndDataRoom; E/F as applicable | PASS | Actual registry/legal/title results cannot be invented |
| 14.3.5 | Financial milestones/readback | U/core_business_context.go; core_financial_actions.go | Reuse signed Core protected sale/land stories | T/sale_test.go:TestSalePlatformMilestonesAndCancellation; E/F as applicable | PASS | Unknown bank outcome retains uncertainty; synthetic approvals are TEST |
| 14.3.6 | Closing/possession/perfection | U/sale.go; consent_transfer.go | Link evidenced land milestones/closing/restart | T/sale_transfer_test.go:TestOwnershipTransferAfterSale; E/F as applicable | PASS | Title/perfection source external; no governmental completion certified |
| 14.3.7 | Withdrawal/completion projection | U/sale.go; domainbase.go | Read final listing/transaction after process restart | T/sale_test.go:TestPropertySaleOffersClosingAndDataRoom; E/F as applicable | PASS | Marketplace deployed consumer pending |
| 14.4.1 | Inventory/rates/rules/fees | U/shortlet.go; pricing.go | Property-local dates; retain immutable cost | T/shortlet_test.go:TestShortletBookingRaceApprovalStayAndDamageClaim; E/F as applicable | PASS | Fee/legal policy authority unchanged |
| 14.4.2 | Marketplace date projection | U/shortlet.go | Reuse scoped inventory/calendar contract | T/shortlet_test.go:TestShortletExternalCalendarSync; E/F as applicable | PASS | Actual marketplace/calendar provider absent |
| 14.4.3 | Hold/confirmation/payment | U/shortlet.go; shortlet_extensions.go | Link actual process hold/overlap rejection; existing DB calendar lock reused | T/shortlet_extension_test.go:TestShortletExtensionRacesNewBooking; E/F as applicable | PASS | Core paid/unknown fixtures separate from actual bank certification |
| 14.4.4 | Guest communication | U/announcements.go; dispatch.go | Real Notification process; signed recipient fixture | T/announcements_test.go:TestGuestAnnouncementsThroughRecipientDomain; E/F as applicable | PASS | CF Meet/actual recipient execution absent |
| 14.4.5 | Access period/revocation | U/access.go; access_provider.go | Local checkout cutoff; fenced original-operation lookup | T/partners_test.go:TestSmartAccessUnknownOutcomeRecoveryAndFencing; E/F as applicable | PASS | Smart device simulation only; no physical credential execution |
| 14.4.6 | Turnover/readiness | U/shortlet.go | Link evidence-based turnover updates | T/shortlet_test.go:TestShortletBookingRaceApprovalStayAndDamageClaim; E/F as applicable | PASS | Actual artisan assignment/execution outside available backend |
| 14.4.7 | Damage/deposit/cancellation | U/shortlet.go; core_financial_actions.go | Reuse hospitality claim/hold and partial refund stories | T/legal_policy_test.go:TestDamageClaimSilenceGoesToReview; E/F as applicable | PASS | Lawful custody/award/bank-finality external; no silent acceptance |
| 14.4.8 | Completion/access/report | U/shortlet.go; access.go; reports.go | Process completion/restart; protected deposits excluded | T/financial_report_projection_test.go:TestFinancialReportsExcludeProtectedStayDeposits; E/F as applicable | PASS | Actual device/terminal bank outcomes independent |
| 14.5.1 | Manager search/proposal | U/delegation.go | Reuse proposal and source boundary | T/listing_test.go:TestManagerDelegationLifecycleAndCapabilityEnforcement; E/F as applicable | PASS | Search returns unavailable without actual Manager service; verified profile producer absent |
| 14.5.2 | Property mandate | U/delegation.go | Link real Owner mandate creation | T/listing_test.go:TestManagerDelegationLifecycleAndCapabilityEnforcement; E/F as applicable | PASS | Signed manager actor TEST; no workspace deployment |
| 14.5.3 | Assignment event/workspace | U/delegation.go; domainbase.go | Reuse atomic event/version schema and NATS relay | T/listing_test.go:TestManagerDelegationLifecycleAndCapabilityEnforcement; E/F as applicable | PASS | Manager consumer/workspace absent |
| 14.5.4 | Permission/scope enforcement | U/authority.go; authz; delegation.go | Real-process scoped manager read; existing fee diversion guards retained | T/maintenance_test.go:TestMaintenancePlatformQuotesManagerLimitsCompletionAndWarranty; E/F as applicable | PASS | No unrestricted account/payout capability granted |
| 14.5.5 | Permission version/change | U/delegation.go | Link narrowed immutable permission version | T/listing_test.go:TestManagerDelegationLifecycleAndCapabilityEnforcement; E/F as applicable | PASS | Actual Manager projection propagation absent; authoritative checks immediate |
| 14.5.6 | Revocation/access termination | U/delegation.go; access.go | Link immediate denial and restart readback | T/partners_test.go:TestSmartAccessGrantsLifecycle; E/F as applicable | PASS | Physical device revoke/Manager consumer pending |
| 14.5.7 | Handover/history/authority | U/delegation.go; ownership.go | Read retained assignment after process restart | T/listing_test.go:TestManagerMandateExpiryWorker; E/F as applicable | PASS | Actual handover/report producers pending; ownership unchanged |

## All 16 engineering controls

| Control | Requirement | Enforcement | Test evidence | Actual delta | Negative/failure coverage / qualification | Local |
|---|---|---|---|---|---|---|
| 15-A | Concurrency/fencing | lifecycle; delegation.go; shortlet.go; core_financial_workflows.go; external_operation.go | T/listing_test.go:TestManagerDelegationLifecycleAndCapabilityEnforcement; affected shared/Gateway suites | New provider leases/fencing; existing CAS/calendar/financial leases | Competing offer/move-in/calendar tests and original-provider recovery; production chaos not certified | PASS |
| 15-B | Command idempotency | internal/http/api.go; shared/go/idempotency; accepted_collection.go | T/property_test.go:TestIdentityVerificationEventsAreIdempotentAndOrdered; affected shared/Gateway suites | Reuse actor/scope/fingerprint/durable responses; original external keys now persisted | Shared concurrent same-key/conflict/commit-response-crash tests; provider lookup required before ambiguous retry | PASS |
| 15-C | Transactional outbox | domainbase.go; shared/go/outbox | T/events_test.go:TestAccountEventsRelayToJetStreamWithRecoveryAndContracts; affected shared/Gateway suites | Reuse; no new outbox | Atomic rollback, concurrent workers, broker outage and duplicate tolerance in shared suite | PASS |
| 15-D | Processed event deduplication | verification.go; consumers.go; core_financial_events.go; shared/go/events | T/event_compat_test.go:TestUpstreamEventsCannotCorruptState; affected shared/Gateway suites | Verification subject/type/aggregate/older-outcome rejection | Real producer events, known version, mismatched source/aggregate, replay/stale outcomes | PASS |
| 15-E | Unknown outcomes | external_operation.go; esign_recovery.go; access_provider.go; core_wallet_operations.go | T/partners_test.go:TestSmartAccessUnknownOutcomeRecoveryAndFencing; affected shared/Gateway suites | Persist uncertainty, original request hash/ref, fenced readback/manual review | Ambiguous envelope/provider acceptance; unknown Core refund/payout retained. External calendar adapter is fetch-only, no write repeated | PASS |
| 15-F | Capability/service authorization | internal/authz; authority.go; internal/http/router.go | T/property2_test.go:TestInternalPublicSummaryAdminRestrictionAndCapabilities; affected shared/Gateway suites | Reuse layered source/actor/property/current verification/step-up checks | Valid service JWT without correct actor/capability denied; restricted identity/financial guards retained | PASS |
| 15-G | Versioned policies | service.go; legal_policy.go; templates.go; pricing.go; delegation.go | T/legal_policy_test.go:TestJurisdictionPolicyAdministration; affected shared/Gateway suites | Reuse configurable/versioned policy and dual approval | No approved legal policy invented; unsafe deposit-silence/late-fee automation disabled | PASS |
| 15-H | Immutable audit | domainbase.go; migrations/00001_platform.sql; migrations/00022_external_operation_recovery.sql | T/partners_test.go:TestSmartAccessUnknownOutcomeRecoveryAndFencing; affected shared/Gateway suites | Audit/lifecycle/property history TRUNCATE forbidden; provider operations retained | Update/delete/truncate and altered original request/result rejected | PASS |
| 15-I | Retention/legal hold | lifecycle.go; disputes_privacy.go; tenancy.go; sale.go | T/oversight_test.go:TestDisputesConsentsAndPrivacyRequests; affected shared/Gateway suites | Reuse closure/dispute/deposit guards; new provider-operation rollback refuses populated history | Other record retention needs legal policy; required financial references retained, no ledger deletion | PASS |
| 15-J | Privacy/purpose binding | evidence.go; applications.go; sale.go; authority.go | T/sale_test.go:TestPropertySaleOffersClosingAndDataRoom; affected shared/Gateway suites | Scoped domain-service proxy; recheck grant after Media; reject URLs outliving purpose; reuse least-privilege grants/audit | Cross actor/property/category/download tests; actual provider/customer consent not invented | PASS |
| 15-K | Rate/abuse controls | gateway/api-gateway/internal/ratelimit; challenges.go; login.go; announcements.go | T/mfa_test.go:TestCodeAttemptsAreBounded; affected shared/Gateway suites | Reuse Gateway Redis budgets and service login/OTP/attempt/announcement limits | Gateway replicated-budget/Redis-outage and sensitive actor buckets tested; durable critical events never discarded by rate limits | PASS |
| 15-L | Flags/safe degradation | internal/config/config.go; integration adapters; Payment production controls | T/partners_test.go:TestFinancingConsentOffersAndInsurance; affected shared/Gateway suites | Reuse nil-adapter/config refusal/provider gates | Unsupported providers return 503; no live provider/custody bypass | PASS |
| 15-M | Schema/backfill rollout | migrations/00022_external_operation_recovery.sql; wallet_backfill.go; sql | T/lifecycle_test.go:TestMigrationsRoundTrip; affected shared/Gateway suites | Add reference-only table/retention triggers; keep prior migrations/history; no guessed backfill | Clean roundtrip and populated rollback; SQLC/generator parity required | PASS |
| 15-N | Business clock/timezone | business_clock.go; shortlet.go; access.go; obligations.go; tenancy.go | T/shortlet_test.go:TestShortletPropertyClockAndAccessWindow; affected shared/Gateway suites | Validate IANA, local dates and checkout instants; local rent opening/arrears | Cross-midnight negative and DST boundaries; UTC durable instants, no server-zone assumption | PASS |
| 15-O | Data-quality reconciliation | jobs_property.go; catalog.go; core_financial_workflows.go; reconciliation.go; dispatch.go | T/oversight_test.go:TestReconciliationIsClassifiedAndAdminScoped; affected shared/Gateway suites | Verified-case reconciliation; bounded admin classification without destructive repair | Failed delivery, unknown references, expired mandates/grants and stale financial projections; external orphan truth requires signed source readback | PASS |
| 15-P | Manual/operator review | disputes_privacy.go; legal_policy.go; core_financial_actions.go; reconciliation.go; external_operation.go | T/legal_policy_test.go:TestDepositSilenceNeedsApprovedPolicy; affected shared/Gateway suites | Reuse restricted attributed cases/dual approval; unresolved provider operations become manual review | Fraud/ownership/screening/deposit/privacy controls; actual Admin console and legal approvals absent | PASS |

## Reuse, demonstrated gaps and actual delta

Reused: account/auth/MFA/lifecycle, Verification/Media/Notification signed clients, source ownership/consent, listing/pricing, applications/offers, agreements/inspection, calendar exclusion locks, mandates/limits, sale checklists/data rooms, domain workers, outbox/inbox, jurisdiction policies, immutable financial bindings and all ten existing Owner/Core process stories. No Payment production implementation is changed and no second ledger is introduced.

Demonstrated gaps and source defects: a mismatched property's verification result was accepted; check-in used the UTC date when the property remained on the preceding local date. New readback includes verified cases so a lost expiry event invalidates publication. Access formerly allowed a whole extra checkout day; it now uses configured local check-in/out instants. Rent opening/arrears and their read projections, tenancy access dates, move-in and notice-effective dates use the property timezone. Move-in cannot precede the accepted start date. Eligible guest anonymization now has a valid immutable batch audit identity and preserves open dispute holds and the original booking/customer reference. Smart access and missing-envelope creation previously retried ambiguous calls directly; the existing workers now retain an original request fingerprint, operation key, uncertainty, fenced lease and durable result, requiring original-operation lookup or manual review before retry. Audit/lifecycle/property-history TRUNCATE is prohibited. A bounded, Admin-actor-scoped reconciliation view classifies recoverable versus material issues without rewriting money. Original references remain immutable. A valid buyer data-room download failed against real Media with MEDIA_NOT_FOUND; Owner now uses an authorized domain-service proxy after checking its actual recipient/purpose grant, retains the buyer identity in Owner audit, and rechecks the live grant/document after Media returns. A URL outliving the grant is refused. Media authorization is unchanged.

Migration 22 expands with `owner_external_operations` and retention triggers. Existing known provider references are reused without backfill; absent historical facts are not guessed. Populated rollback must refuse loss of provider operation history. SQLC and API contracts are regenerated only for the affected Owner module; one additive internal Admin GET is introduced. Real-process E2E is extended, with Windows executable/cleanup support and a local SMTP recipient fixture for the actual Notification SMTP adapter (Unix retains its original dev capture).

## Evidence and quality gates

One consolidated applicable race regression executed with real PostgreSQL18, Redis8.8.0, real NATS JetStream, Go1.26.9/CGO/GCC, distinct service databases and actual service JWTs. It passed Owner135 tests, Gateway22, Shared135 and E2E5 top-level/5 subtests, and failed two Owner fixtures: `TestDepositSilenceNeedsApprovedPolicy` and `TestCommercialTermsLOIAndLandTerms`. Both fixtures moved in immediately despite an accepted future start date. Their accepted fixture start dates were corrected to the current business date; the final targeted race rerun passed both. **Effective final distinct Owner137/137, Gateway22/22, Shared135/135, E2E5/5 top-level + 5/5 subtests PASS.** No actual test was skipped. Package entries without test files are not skipped tests. The original failed full run remains failed evidence.

The affected financial regression passed **4/4** actual Payment tests: `TestActualOwnerFinancialCoreConsumer`, `TestCapabilityEvidenceCannotActivateProduction`, `TestLockedCollectionPolicyRevocationDoesNotResubmit`, `TestVerifiedPayoutReferenceReadEligibility`. The parent reran its actual tagged Owner child and **10/10** additional financial stories. The unchanged Payment foundation's baseline91 top-level/85 subtests are historical evidence, not newly rerun or silently counted in these four tests.

Executed commands:

```powershell
# Real test infrastructure configured; CF_REQUIRE_INTEGRATION=1, CGO_ENABLED=1
go test -race -count=1 -p 2 -json ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/... ./tests/e2e/...
go test -race -count=1 -json ./services/payment-service/tests/integration -run '^TestActualOwnerFinancialCoreConsumer$|^TestCapabilityEvidenceCannotActivateProduction$|^TestLockedCollectionPolicyRevocationDoesNotResubmit$|^TestVerifiedPayoutReferenceReadEligibility$'
go test -race -count=1 -json ./services/individual-owner-service/tests/integration -run '^TestCommercialTermsLOIAndLandTerms$|^TestDepositSilenceNeedsApprovedPolicy$'
go vet ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/... ./tests/e2e/...
staticcheck ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/... ./tests/e2e/...
go build ./services/individual-owner-service/... ./gateway/api-gateway/... ./shared/go/... ./tests/e2e/...
go vet -tags financial_cross_service ./services/individual-owner-service/tests/integration
staticcheck -tags financial_cross_service ./services/individual-owner-service/tests/integration
sqlc generate -f services/individual-owner-service/sql/sqlc.yaml
go run ./services/individual-owner-service/cmd/apidocgen -root . -check
python scripts/payments/generate_contracts.py
bash scripts/ci/check-architecture.sh --self-test
bash scripts/ci/check-architecture.sh
python scripts/payments/check_secrets.py
git diff --check
```

Vet/build/architecture/generators/contract parity/event-schema tests/migrations/secret and whitespace checks PASS. Staticcheck initially found four unused test result assignments (SA4006); the HTTP calls and assertions were retained while discarding the unused values. Owner/E2E staticcheck and vet then passed; current build and tagged child checks passed. No production code changed after the consolidated compile snapshot; the final deltas are the two fixture start dates and the unused-result normalization. A source inventory records the pre-correction file hashes. Clean migration roundtrip and populated migration22 retention checks passed. SQLC regenerated22 files with identical pre/post bytes; Payment generator changed no contracts. Owner generated routes match the OpenAPI/Postman handlers; existing consumed/emitted event schemas remain current.

Evidence (ignored local logs retained):

| Evidence | SHA-256 |
|---|---|
| `tmp/payments/owner-14-15-consolidated.jsonl` | `bb2d8a7c8467b302c42690122e1c659493fb9a652e3ca4a0655f610849694aef` |
| `tmp/payments/owner-14-15-financial-regression.jsonl` | `852c2b58fb7eeebf0ace2ebb20ec4d9d672594549c2fa4d9abeecd1a1bca61f1` |
| `tmp/payments/owner-14-15-final-corrective-complete.jsonl` | `fca17aa0c57c9df03ba7b85d6f38354f94e401fddc6645add50d3e76a5ec5070` |

Intermediate failures are preserved, including Windows executable/capture-file fixture limitations, the attempted DB capture after Notification had correctly purged security-message content, the incorrect short-let test route, initial clock/retention fixtures without eligible records, and the data-room Media rejection. These are not reclassified as successful runs. Windows uses the actual Notification SMTP adapter with a local SMTP recipient simulator; Unix retains the original capture provider. S3-compatible storage, KYC registry responses, future source actors, legal templates, money/provider responses and operator approvals are explicitly TEST fixtures. Windows processes are killed for cleanup/restart; this is crash/durability evidence, not a Windows graceful-shutdown certification.

Historical provenance: Section 12–13 report at baseline records Owner128, Gateway22, Payment91 top-level+85 subtests after its documented assertion correction and ten additional financial process stories. See unchanged `SECTIONS_12_13_INTEGRATION_CLOSURE.md` for hashes and source qualifications. Earlier stored Owner coverage and Payment master/11–14 reports are reused within their own recorded revisions, not counted as newly run tests.

## External dependency register

| Dependency / responsible authority | Locally supported evidence | What remains / evidence needed to close |
|---|---|---|
| Marketplace and tenant/buyer/guest producer | Signed scoped source actors, public listing/media/calendar contracts, outbox/NATS, recipient contract | Deployed actual producer/consumer, current identity and real acceptance/consent, durable projections and recipient delivery |
| Manager producer/workspace | Property mandates/current authority, permission versions, immediate denial/revocation and retained history; signed test actor | Actual verified profile/search producer and versioned workspace consumer/handover reports |
| Artisan/Professional producer | Accepted source amount/recipient/completion/report preflight and Core process stories | Actual signed provider/backend/deliverable production; no simulated work called completed externally |
| Admin console | Restricted attributable backend review/cases, dual approval where policy requires, original-operation/manual review observations | Real operator console, independently authorized decision makers and actual approvals |
| Verification / KYC / bank attestation | Real service/sandbox contracts and local negative/readback checks | Production KYC/bank/legal-party attestations and expiry/revocation; no new verified bank invented |
| Media / Notification | Real processes, signed upload/status/grants/reports and dispatch; S3-compatible/local SMTP TEST provider | Certified storage/scanner and real notification provider/user delivery; provider acceptance alone is not recipient delivery |
| CF Meet / maps | Context refs / validated manual coordinates and unsupported guard | Actual approved service/provider contracts, authentication and execution/readback |
| E-sign / calendars / smart locks | Offline evidence; provider-neutral original-ref recovery, fetch-only calendar and scoped grant logic, simulated adapter tests | Certified signature provider/legal templates; supported calendar provider/write contract; physical devices and revocation finality |
| Financing / insurance | Consent-scoped offer/policy/claim references and unavailable behavior | Licensed actual partners, approved products/financial contracts |
| Commercial legal party / land registry | Distinct Core corporate receiving guard and evidenced Owner land/sale checklists; TEST source evidence | Real corporate owner/signatory authority; actual land title, consent/perfection and registry integration evidence |
| Paystack / licensed custody / treasury | Prior TEST certification retained in documented scope; new runs do not repeat provider certification | Transfer merchant phone/DVA eligibility account blockers; terminal refund, public webhook, DVA assignment/funding NOT CERTIFIED/NOT RUN; legal custody/safeguarding not approved |
| Jurisdiction legal/product approvals | Versioned guarded policies/templates; illustrative fees remain accepted source facts | Approved local templates, notice/deposit/tax/retention/fee policy; O-001–O-012 all OPEN |
| GitHub CI / staging / production | Local gates independent; previous Actions jobs executed zero steps due billing | Working Actions account/budget; independently certified staging; explicit production and provider/legal approvals |

## Final verdicts and delivery

| Verdict | Status and precise scope |
|---|---|
| 14.1 Onboard/verify/publish | LOCAL PROCESS PASS: actual Gateway/Owner/Verification/Media/Notification processes, DB/events; actual Marketplace projection remains pending |
| 14.2 Direct rental | LOCAL PROCESS PASS for supported backend/contract boundary: linked application/offer/offline-sign/occupancy/inspection/termination/reopening and restart; separate actual Core process money stories; no CF Meet, licensed custody or real bank certification |
| 14.3 Land/commercial/sale | LOCAL PROCESS PASS for Owner land sale/diligence/data-room/closing and separate Core land occupation/milestone guards; real corporate receiving party and registry completion BLOCKED |
| 14.4 Short-let | LOCAL PROCESS PASS for hold/overlap/stay/access/turnover/restart; completion/deposit/cancellation/release via existing accelerated-clock Core stories; physical device, real recipient and bank finality not certified |
| 14.5 Manager | LOCAL PROCESS PASS for the mandate/permission/version/revocation/history boundary; profile/search producer, workspace activation and actual Manager service BLOCKED |
| Section14 | LOCALLY CLOSED WITH EXTERNAL QUALIFICATIONS; all42 rows individually mapped |
| Section15 | LOCAL PASS for the available controls and documented contract boundary; all16 rows mapped |
| Individual Owner backend | LOCAL IMPLEMENTATION CLOSED for Sections14�15 within the stated qualifications; not a claim that future services or production are live |
| Financial Core regression | PASS for4 newly executed affected tests and10 reused/renewed stories; broader unchanged foundation evidence reused at baseline |
| External certification | Independent statuses in dependency register; none promoted from simulation to production |
| Staging | NOT CERTIFIED |
| Production | BLOCKED; O-001�O-012 OPEN |

Privacy qualification: Owner denies new document access immediately after grant expiry/revocation and rejects a returned URL that would outlive the purpose grant. Previously issued URLs retain Media's existing bounded storage lifetime (default5 minutes); they cannot be retroactively revoked by changing an Owner grant. A deployment requiring immediate invalidation of already issued storage URLs needs revocable content delivery/storage authorization and a separate reviewed contract. No claim of immediate physical/storage revocation is made.

No live provider calls, deployments, regulatory approvals or changes to the Payment ledger/withdrawal/refund/custody algorithms occurred. Core source/migrations/contracts are unchanged except the shared Postman collection's additive Owner route. Generic PAYMENT_SERVICE_URL remains refused; the established typed Financial Core client is reused. No deployment/live enablement or Section 16 implementation is authorized by this task. Normal non-force push to the existing development line is authorized after verification; divergence requires reporting before any merge/rebase.

## Exact changed files

- `contracts/internal/openapi/individual-owner-service-domain.yaml`
- `docs/product/individual-owner/IMPLEMENTATION_COVERAGE.md`
- `docs/product/individual-owner/SECTIONS_14_15_IMPLEMENTATION_CLOSURE.md`
- `postman/CosmicForge_RealEstate.postman_collection.json`
- `services/individual-owner-service/internal/http/routes_closure.go`
- `services/individual-owner-service/internal/integrations/esign/esign.go`
- `services/individual-owner-service/internal/integrations/smartaccess/smartaccess.go`
- `services/individual-owner-service/internal/repositories/db/models.go`
- `services/individual-owner-service/internal/repositories/db/partners.sql.go`
- `services/individual-owner-service/internal/repositories/db/property.sql.go`
- `services/individual-owner-service/internal/repositories/db/tenancy.sql.go`
- `services/individual-owner-service/internal/usecases/access.go`
- `services/individual-owner-service/internal/usecases/access_provider.go`
- `services/individual-owner-service/internal/usecases/agreements.go`
- `services/individual-owner-service/internal/usecases/business_clock.go`
- `services/individual-owner-service/internal/usecases/business_clock_test.go`
- `services/individual-owner-service/internal/usecases/disputes_privacy.go`
- `services/individual-owner-service/internal/usecases/esign_recovery.go`
- `services/individual-owner-service/internal/usecases/external_operation.go`
- `services/individual-owner-service/internal/usecases/obligations.go`
- `services/individual-owner-service/internal/usecases/property.go`
- `services/individual-owner-service/internal/usecases/reconciliation.go`
- `services/individual-owner-service/internal/usecases/sale.go`
- `services/individual-owner-service/internal/usecases/shortlet.go`
- `services/individual-owner-service/internal/usecases/tenancy.go`
- `services/individual-owner-service/internal/usecases/tenancy_cases.go`
- `services/individual-owner-service/internal/usecases/verification.go`
- `services/individual-owner-service/migrations/00022_external_operation_recovery.sql`
- `services/individual-owner-service/sql/queries/partners.sql`
- `services/individual-owner-service/sql/queries/property.sql`
- `services/individual-owner-service/sql/queries/tenancy.sql`
- `services/individual-owner-service/tests/integration/core_workflow_retention_test.go`
- `services/individual-owner-service/tests/integration/fakes_test.go`
- `services/individual-owner-service/tests/integration/legal_policy_test.go`
- `services/individual-owner-service/tests/integration/listing_test.go`
- `services/individual-owner-service/tests/integration/oversight_test.go`
- `services/individual-owner-service/tests/integration/partners_test.go`
- `services/individual-owner-service/tests/integration/property_test.go`
- `services/individual-owner-service/tests/integration/sale_test.go`
- `services/individual-owner-service/tests/integration/shortlet_test.go`
- `services/individual-owner-service/tests/integration/tenancy_test.go`
- `tests/e2e/owner_domain_test.go`
- `tests/e2e/owner_functional_test.go`
- `tests/e2e/owner_primary_journeys_test.go`
- `tests/e2e/owner_test.go`
- `tests/e2e/platform_test.go`
- `tests/e2e/smtp_capture_test.go`

## Current CI and Git delivery

Baseline backend run38315529690 executed zero steps. Its Payment check115031120654 states account payments failed or the spending limit must increase. Baseline CI is BLOCKED, not an application-test failure or PASS. Final commit/remote verification and its Actions result are recorded in the post-commit metadata/final handoff. No merge/rebase/force push is permitted.

## All 30 required failure/security scenarios

Fresh means executed in the current consolidated/affected run or its explicitly recorded correction. Historical Core evidence applies to byte-unchanged Payment code and is not called freshly executed.

| Scenario | Actual evidence | Verdict / qualification |
|---|---|---|
| 01 Unverified publication | T/listing_test.go:TestListingPublicationPricingAndVersions | Fresh PASS |
| 02 Unauthorized co-owner/representative | T/property_test.go:TestCoOwnersConsentAndGuardedTransfer | Fresh PASS |
| 03 Expired/restricted authority used again | T/property_test.go:TestVerificationSubjectBindingAndLostExpiryReadback; T/event_compat_test.go:TestUpstreamEventsCannotCorruptState | Fresh PASS; source-defined EXPIRED/restriction, not invented REVOKED event |
| 04 Duplicate publication | T/listing_test.go:TestListingPublicationPricingAndVersions (8 concurrent commands) | Fresh PASS, one durable event |
| 05 Forged customer/source identity | E2E foundation/gateway rejection subtest; Gateway TestCrossDomainTokenMisuse; T/leads_test.go:TestApplicationsScreeningDecisionsAndOffers | Fresh PASS; authoritative source actor simulators only |
| 06 Unrelated applicant evidence | T/leads_test.go:TestApplicationsScreeningDecisionsAndOffers | Fresh PASS |
| 07 Unaccepted/modified quote | F original accepted-context consumer; Payment TestAcceptedTermsInvoiceImmutableAndNoPaymentClaim | F fresh PASS; unchanged broader Payment test reused at baseline |
| 08 Duplicate collection | F ten business stories and original child | Fresh PASS, original intent retained |
| 09 Pending collection inferred paid | F original rent/short-let unpaid story; T/tenancy_test.go:TestResidentialLeaseLifecycle | Fresh PASS |
| 10 Activation without required conditions | T/tenancy_test.go:TestMoveInCannotPrecedeAcceptedBusinessPeriod; TestResidentialLeaseLifecycle | Fresh PASS |
| 11 Protected deposit withdrawal | F protected wallet/availability; Payment TestLifecycleConcurrentWithdrawalsAndNoEffectFailure | F fresh PASS; broader unchanged money test historical |
| 12 Double deposit refund | F residential/hospitality duplicate readback; Payment TestRefundUnknownPartialCapsAndSingleSubmission | F fresh PASS; unchanged Core single-submission/cap evidence historical |
| 13 Manager payout diversion | F manager accepted-fee/wrong fee story; T/maintenance_test.go:TestMaintenancePlatformQuotesManagerLimitsCompletionAndWarranty | Fresh PASS |
| 14 Commercial receiving-party mismatch | F accepted family/context guards; Payment TestCategoryPoliciesAllEighteenFamiliesAndApprovalGates | Core guard retained at unchanged baseline; actual corporate receiving flow externally BLOCKED |
| 15 Sale complete without evidence | T/sale_test.go:TestPropertySaleOffersClosingAndDataRoom; E land-sale subtest; F sale story | Fresh PASS |
| 16 Duplicate milestone release | F release-key replay in original/ten stories; Payment TestLifecycleReleaseFundingEvidenceAndConcurrency | F fresh PASS; broad money test unchanged/historical |
| 17 Overlapping bookings | E short-let subtest; T/shortlet_test.go:TestShortletBookingRaceApprovalStayAndDamageClaim | Fresh PASS |
| 18 Cancel/extension collision | T/shortlet_extension_test.go:TestShortletExtensionRacesNewBooking; F short-let cancellation/extension story | Fresh PASS |
| 19 Access expired/revoked | T/partners_test.go:TestSmartAccessGrantsLifecycle; TestSmartAccessUnknownOutcomeRecoveryAndFencing | Fresh PASS backend/contract; physical device external |
| 20 Artisan payment before acceptance | T/maintenance_test.go:TestMaintenancePlatformQuotesManagerLimitsCompletionAndWarranty; F artisan story | Fresh PASS |
| 21 Professional payment before deliverable | T/maintenance_test.go:TestProfessionalAppointmentDueDiligence; F professional story | Fresh PASS |
| 22 Manager after expiry/revocation | T/listing_test.go:TestManagerMandateExpiryWorker; TestManagerDelegationLifecycleAndCapabilityEnforcement; E manager subtest | Fresh PASS, authoritative current permissions |
| 23 Stale event overrides newer outcome | T/event_compat_test.go:TestUpstreamEventsCannotCorruptState; F duplicate/stale story | Fresh PASS |
| 24 NATS replay duplicates side effects | T/consumers_test.go:TestEventConsumersApplyOnceThroughUseCases; shared/go/events/jetstream tests; F | Fresh PASS |
| 25 Worker takeover duplicates mutation | T/partners_test.go:TestSmartAccessUnknownOutcomeRecoveryAndFencing; shared/go/idempotency/outbox crash/lease tests; F lease story | Fresh PASS, persisted crash-state and concurrent recovery; not production chaos certification |
| 26 Deletion during legal/financial hold | T/oversight_test.go:TestGuestRetentionPreservesHoldsAndAuditsEligibleAnonymization; TestDisputesConsentsAndPrivacyRequests; T/tenancy_test.go:TestDirectAgreementOffPlatformAndAuthorization | Fresh PASS local retained references/holds |
| 27 Document purpose expires/revoked in flight | T/sale_test.go:TestPropertySaleOffersClosingAndDataRoom; E actual Media buyer grant/deny/revoke | Fresh PASS API/issuance; existing issued-URL TTL qualification applies |
| 28 Hidden/unapproved repeating legal fee | F initial/subsequent rent; T/legal_policy_test.go:TestAutomaticLateFeesNeedApprovedPolicy; unchanged Core rent-category evidence | Fresh applicable PASS; no novel rate/law approved |
| 29 Unknown provider outcome equals success | F original unknown payout/refund stories; T/partners_test.go:TestSmartAccessUnknownOutcomeRecoveryAndFencing; T/tenancy_test.go:TestElectronicSignatureReconciliation | Fresh PASS, original reference recovery only |
| 30 Restricted identity bypasses controls | T/lifecycle_test.go:TestAdminRestrictSuspendReinstateWithIdempotencyAndCallerPolicy; T/property2_test.go:TestInternalPublicSummaryAdminRestrictionAndCapabilities; Payment TestCapabilityEvidenceCannotActivateProduction | Fresh PASS |
