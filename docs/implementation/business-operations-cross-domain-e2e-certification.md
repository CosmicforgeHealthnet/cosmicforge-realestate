# Business Operations — cross-domain journey certification

"Real DB" = disposable PostgreSQL 16 database created per test and migrated with
the production migrations. "Process" = real service binaries started as OS
processes. A journey whose other services are represented by a signed contract
double is reported as **contract certification**, not live service integration.
Broker: the Business integration harness exercises the transactional outbox; NATS
JetStream delivery is exercised by the existing platform E2E and outbox relay tests,
not by each journey below.

| # | Journey | Named test(s) | Processes / doubles | Level |
|---|---|---|---|---|
| 1 | Registration → verified capacity → staff → property → approved listing | `TestJourneyJ31CompleteBusinessOperatingLifecycle`, capacity/staff/inventory suites | Business handler + real DB; Verification contract double | Contract certification |
| 2 | Property publication → first-party Marketplace projection → readback | Business: `TestBusinessCommercialSignedPublicationAmbiguousCommitReadbackAndPause`; Marketplace: `TestMarketplaceBusinessSupplyContractRealProcess` | Marketplace real migrate+api processes + own DB; Business side represented by its signed identity | Marketplace real-process; Business↔Marketplace contract-matched, not one combined process run |
| 3 | Marketplace reservation → hold → payment intent → confirmed | `TestBusinessCommercialSignedReceiptDedupeForgedCallerAndMissingReservationDetails`, `reservation_payments_test.go` | Signed Marketplace receipt double; Payment double | Contract certification (Marketplace customer reservation producer not implemented) |
| 4 | Arrival → check-in → folio → move/extension → checkout → night audit | `TestBusinessJourneyHotelOperationsExtensionChargesCheckoutNightAudit` | Business + real DB | Internal integration |
| 5 | Cancellation → stock release → waitlist → rebooking | `TestBusinessJourneyWaitlistReleaseToConfirmedReservation` | Business + real DB | Internal integration |
| 6 | RMS forecast → approved rate → channel propagation | `TestBusinessJourneyRMSBookingDataToAutopilotChannelAndMonitoring` | Channel adapter double | Contract certification |
| 7 | Residential application → offer → agreement → tenancy → rent/deposit → renewal/move-out | `TestAnnexDResidentialRentalLifecycleFromOfferToDepositRelease` | Business + real DB; Payment intent double for readback | Internal integration + Payment contract |
| 8 | Commercial applicant → LOI → approved lease → service charge → termination | `TestAnnexDCommercialLOITermsLeaseServiceChargeLifecycle` | Business + real DB | Internal integration |
| 9 | Land → title evidence → allocation → closing milestones | `TestAnnexDLandTitleAllocationDueDiligenceAndClosingMilestones` | Business + real DB; Payment double | Internal integration + Payment contract |
| 10 | Buyer offer → unit hold → conditions → payment milestones → closing | `TestAnnexDSaleOfferUnitHoldConditionsPaymentMilestonesClosing` | as above | Internal integration + Payment contract |
| 11 | Off-plan → milestone certification → variation → snagging → handover | `TestAnnexDOffPlanProgressCertificationVariationAndConservation`, `TestBusinessDevelopmentAllocationMilestonesSnagsAndHandover` | Business + real DB (professional references recorded, Professional Services not running) | Internal integration |
| 12 | Housekeeping → maintenance provider → evidence → inspection → readiness | `TestAnnexDMaintenanceQuotesProviderWarrantyAndAssetHistory`, `TestBusinessHousekeepingRoomReadyGateInspectionSeparationAndMaintenanceBlock`, `TestJourneyJ24CheckoutTurnoverInspectionReadyRoom` | Artisan represented by references | Internal integration |
| 13 | Manager mandate → scoped authorization → expiry/revocation | `TestBusinessManagerDelegationScopedExpiringRevocableAndEnforced` | Signed property-manager caller to the Business internal endpoint | Contract certification |
| 14 | Professional appointment → finding → data room → revocation | `TestBusinessProfessionalDataRoomGrantPurposeExpiryApprovalAndRevocation`, `TestAnnexDManagerPerformanceAndProfessionalFindingConditionLink` | Signed Media caller | Contract certification |
| 15 | Inbox → communication context → assignment → SLA → scheduled reply | s12 inbox tests | CF Meet/channel doubles | Contract certification |
| 16 | Review → response → repeated issue → quality task → recovery | s13 review tests | Business + real DB | Internal integration |
| 17 | Campaign → consent → lead → booking attribution → unsubscribe | `TestBusinessCampaignConsentAwareDispatchWithdrawalAndApprovalLimits`, `TestBusinessLeadsDedupAssignmentConversionAttributionAndFunnel` | Notification double | Contract certification |
| 18 | Brand/storefront → Marketplace readback | Marketplace listing readback in `TestMarketplaceBusinessSupplyContractRealProcess`; Business storefront tests (s14) | as journey 2 | Partial |
| 19 | Upsell → quote → folio obligation → payment reference | `TestBusinessUpsellUsesCanonicalAddOnPriceAndFolioAuthority` | Payment double | Contract certification |
| 20 | Budget → independent approval → obligation → reconciliation | `TestBusinessFinanceBudgetControlIndependentApprovalAndObligations`, `TestBusinessFinanceObligationReadbackUnknownSettledAndReversal` | Payment double | Contract certification |
| 21 | Financing consent → partner reference → status | `TestAnnexDFinancingConsentInsuranceComplianceAndAccessOutcomes` | Partner references only | Internal integration |
| 22 | Insurance coverage → compliance → claim evidence | same | Insurer references only | Internal integration |
| 23 | Smart device grant → expiry/revocation → unknown outcome | same + `TestBusinessPMSSmartAccessLostResponseRecoversOriginalGrantAndRevokesAfterDeparture` | Provider references / doubles | Internal + contract |
| 24 | Partner credential → signed webhook → rotation/revocation | `TestAnnexDPartnerCredentialsWebhooksRotationAndRevocation`, `TestBusinessCommercialProviderCallbackKeyProtectionSignatureAndReplay` | — | Internal integration |
| 25 | Tax/currency → price → fiscal consistency | `TestAnnexDFXFreshnessOrganisationChangeAndClosureContinuity`, `pms_fiscal_test.go` | — | Internal integration |
| 26 | Content suggestion → operator approval → publication | `TestAnnexDRiskSignalsProtectionIncidentsAndContentSuggestions` | — | Internal integration |
| 27 | Offline command → replay → conflict → reconciliation | `TestBusinessPMSOfflineStaleAssignmentDurableTaskAndRealReplay` | — | Internal integration |
| 28 | Reservation risk → manual review → restriction/appeal | `TestBusinessRiskAppealSeparationSalesOffersAndTenancyConversion`, risk signals test | — | Internal integration |
| 29 | Business migration → partial failure → resume | `TestBusinessImportPreviewResumeDedupeAndSafeRecovery` | — | Internal integration |
| 30 | Ownership change/closure → independent authorization → retention | `TestAnnexDFXFreshnessOrganisationChangeAndClosureContinuity` | Verification reference recorded | Internal integration |
| 31 | Security, replay, provider outage recovery | `TestJourney09PaymentAndBrokerOutageRecoverWithoutDuplicateEffects`, `TestJourney10…`, `security_concurrency_test.go` | doubles | Contract certification |
| 32 | Complete business lifecycle | `TestJourneyJ31CompleteBusinessOperatingLifecycle` + Annex D journeys | doubles | Contract certification |

Supporting gates: `TestAnnexDDatabaseGuardsMatchEveryGeneratedDomain` (all 71 guards),
`TestAnnexDUpgradeFromPopulatedSchema37`, `TestMarketplaceSupplyExactlyOnceAppendOnlyAndExpiry`
(concurrent duplicate deliveries → one acceptance).

**Not achieved in this pass:** a single run that starts business-operations-service,
marketplace-service, payment-service and verification-service together as processes
for journeys 1–3/18. Each service's side is tested against the shared contract.
