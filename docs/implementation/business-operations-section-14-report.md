# Business Operations Section 14 — Marketing, Direct Commerce and Conversion

Migration `00034_marketing_commerce.sql`. Implementation: `internal/http/operational_marketing.go`.

| Requirement | Implementation | Evidence |
|---|---|---|
| Lead sources, leads, dedupe, assignment, activities, SLA, follow-up, merge, conversion | Approved `lead_sources`; dedupe key = property + channel + normalised external reference (duplicate returns the live lead); assignee must hold `leads:write`; real contact activities satisfy first-response SLA; follow-up worker records `FOLLOW_UP_DUE` once; conversion links an actual live reservation (never creates/verifies a customer) and writes attribution | `TestBusinessLeadsDedupAssignmentConversionAttributionAndFunnel` |
| Segments | Marketing segments reference Section 11 guest segments (current evaluation of the active version); no duplicate CRM authority | campaign test |
| Campaigns | Versioned, approved CAMPAIGN_MESSAGE marketing template required; launch requires MFA; exact `COMMERCIAL_CAMPAIGN_LAUNCH` approval when audience ≥ 50 or budgeted (amount bound); schedule/pause/resume/cancel; sponsor authority rechecked by the worker (revoked → paused, pending sends cancelled) | `TestBusinessCampaignConsentAwareDispatchWithdrawalAndApprovalLimits` |
| Consent-aware dispatch | Members evaluated with `crm.ConsentAllowed(MARKETING)`; ineligible members recorded as suppressed; eligible members get existing durable guest notification jobs (`CAMPAIGN_MESSAGE`, purpose MARKETING); the Notification worker rechecks consent, suppression, template and campaign state at dispatch | campaign test (withdrawal before dispatch → SUPPRESSED, zero sends) |
| Delivery truth | Reports show Notification Service acceptance only; `delivery_confirmed` null; spend is the configured budget reference, never Payment ledger | campaign test |
| Brand and storefront | Organisation brand profile (Media references); storefront collections only of published listings and active packages/promotions; publish requires verified commercial gate + exact `COMMERCIAL_STOREFRONT_PUBLICATION` approval + MFA; immutable publication versions; projection to Marketplace via `storefront.updated.v1` | lead test (unpublished supply rejected) |
| Packages/add-ons/upsells | Upsell offers reference canonical Section 7 add-ons; acceptance freezes the canonical add-on price, enforces eligibility (state, nights, source, date window) and daily capacity; settlement authority stays with folio/Payment (link to an exact folio posting) | `TestBusinessUpsellUsesCanonicalAddOnPriceAndFolioAuthority` |
| Conversion funnel | Canonical stages projected idempotently from canonical records (inquiry, hold, reservation, check-in, completed stay, verified review, repeat); impressions/views only via signed Marketplace intake; rates null when denominators are unsourced | lead test |

External: Marketplace storefront readback endpoint is not available; `marketplace_state` records `PROJECTION_PUBLISHED` (event published) and never claims acknowledgement.
