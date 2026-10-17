# COSMICFORGE REAL ESTATE
## Individual Owner — Frontend Master Implementation Blueprint and API Handoff

**Version:** 1.1 · Postman/OpenAPI authoritative API handoff added  
**Prepared:** 17 October 2026  
**Checked repository:** `development` @ `ee3b8bdfd15f9805a615f10948eac89481bee0b3` (source check; re-pin at execution)  
**Document class:** Controlled engineering execution specification and complete API handoff locator  
**Product:** CosmicForge Real Estate — Individual Property Owner  
**Repository:** `CosmicforgeHealthnet/cosmicforge-realestate`  
**Repository base branch:** `development`  
**Implementation strategy:** Reference preservation → complete interactive mocks → mock approval → API integration → staging verification → release approval

> **Operating decision.** Build and approve the complete responsive web and native mobile **mock applications before connecting backend APIs**. The existing backend remains intact. This document is the single source of execution instructions for frontend work; original Stitch files and checked-out backend OpenAPI contracts remain the factual sources for visuals and server operations respectively.

---

# 1. Executive directive and definition of success

The Individual Owner product is a full-featured property-owner workspace, not a 30-page brochure. A completed frontend must let a user navigate through, understand and operate all 30 product areas using coherent web and mobile experiences. This implementation should preserve the look of the Stitch exports while creating real UI structure, reusable components, local mock state and eventually contract-correct service integration.

**Required end state:**

1. A browsable, preserved visual reference library inside the existing repository, containing the original Stitch HTML, PNG and design documents without destructive edits.
2. A runnable **Next.js/React/TypeScript** web application with all 30 areas represented by complete, navigable workflows, including dark and light appearance.
3. A runnable **Flutter/Dart** mobile application with equivalent owner capabilities, native navigation and usable phone-sized screens. Do not use WebViews of the exported HTML as the implementation.
4. A deterministic **mock mode** in both apps in which forms, decisions, dates, filtering, search, detail views, transitions and error states work locally, but no backend, payment, verification or third-party action is actually executed.
5. A reviewed and approved complete mock, with evidence, coverage register, known limitations and representative screenshots. **Do not begin real API integration without explicit approval.**
6. After approval, a separate integration stage driven by the repository's version-pinned OpenAPI contracts and Gateway paths; then staging QA, provider certification where applicable, and separate release approval.

**What is not success:** publishing static `code.html` files, showing one dashboard per area, using fake action-success timers, declaring a mock “production ready,” or calling an unconnected third-party service “verified.”

# 2. Authority, confirmed baseline and source precedence

## 2.1 Verified starting evidence

| Area | Confirmed baseline | Engineering implication |
|---|---|---|
| Original Stitch ZIP | 30 HTML `code.html` templates + 30 PNG `screen.png` previews + 2 Markdown design documents | Usable visual references; not application source |
| Desktop and mobile pairing | 15 desktop references and 15 mobile references | Visual starting point, not a completed 30-module journey set |
| Stitch templates | Local DOM interactions, placeholder links, hardcoded demo data and unsupported claims | Must rebuild functional behaviors behind reusable UI |
| Dark mode | Dark tokens described, no delivered working dark screen variants verified | Implement and test dark theme in both applications |
| Web scaffold | `apps/individual-owner/web/` contains scaffold placeholder at checked baseline | Initialize a proper Next.js app |
| Mobile scaffold | `apps/individual-owner/mobile/` contains scaffold placeholder at checked baseline | Initialize a proper Flutter app |
| Backend | Existing Go owner service and API Gateway, separate shared services | Do not rebuild or directly access backend databases |
| Contracts | `contracts/public/openapi/individual-owner.yaml` and `individual-owner-domain.yaml` | Later integration must use actual methods, payloads and guards |

This snapshot records the state inspected in October 2026. Before executing, the agent must inspect the checked-out commit rather than assume the repository has not changed.

## 2.2 Source precedence when materials disagree

1. **Checked-out backend implementation and published OpenAPI contracts:** server methods, request schemas, states, authorization and errors. No Stitch-generated route list overrides them.
2. **Existing repository architecture and product specifications:** service boundaries, technology stack, deployment pattern and domain safety rules.
3. **Original Stitch HTML + PNG previews:** visual layout and interaction intent, not verified business logic.
4. **Original design Markdown (`individual_owner_design.md`, `DESIGN.md`):** visual tokens, IA and product intent, subject to correction where inaccurate.
5. **This master plan:** work sequencing, local mock behavior, acceptance criteria and handoff. It may not invent unsupported backend capabilities.

Design and server integration are different authorities. If visual copy contradicts a backend status, the backend wins. If a business workflow does not have an implemented endpoint, mock it as a local scenario and flag the integration gap explicitly.

# 3. Locked technology and cross-service boundaries

| Component | Technology | Do not replace with |
|---|---|---|
| Web | Next.js, React, TypeScript, Tailwind CSS | A raw stitched collection of HTML pages |
| Web async/data | TanStack Query, React Hook Form, Zod | Ad hoc view-specific fetch and validation |
| Mobile | Flutter, Dart | React Native or HTML WebViews |
| Mobile state/routing | Riverpod, GoRouter | Unstructured global state and manual route strings |
| Mobile HTTP (integration phase) | Dio | Direct database calls or undocumented endpoints |
| Backend | Go, Chi, pgx, sqlc, Goose | Rewriting existing backend functionality |
| Data/event infrastructure | PostgreSQL, Redis, NATS JetStream | Client-side access to backend infrastructure |
| Contract | OpenAPI 3.1 via public API Gateway | Guessing routes from Stitch copy |
| Payments | Payment service / authorized provider | Client-side payment settlement or synthetic escrow release |
| Verification | Verification service | UI claiming government verification itself |
| Documents | Media and document service | Publicly accessible ownership/tenant evidence URLs |
| Human messaging / calls | CF Meet | Invented independent chat/call backend |
| Deploy | Existing Docker/CI and environment conventions | New deployment architecture without approval |

**Architecture rule:** Both clients are presentation and owner-action clients, not sources of financial, ownership, tax or verification truth. In mock mode they simulate these state changes visibly as examples. In integrated mode they must display authoritative server state and guarded allowed actions.

# 4. Project layout and preservation strategy

## 4.1 Existing local source archive

```text
C:\Projects\cosmicforge-realestate\apps\individual-owner\stitch_individual_owner_design_system.zip
```

Do not delete, rename or modify the original ZIP. Record its SHA-256 hash before extraction. The actual archive has one top-level directory named `stitch_individual_owner_design_system/`; each screen is stored as a folder containing `code.html` and `screen.png`.

## 4.2 Target folder structure

```text
C:\Projects\cosmicforge-realestate\
  apps\individual-owner\
    README.md
    stitch_individual_owner_design_system.zip      # Original untouched archive
    design\
      stitch\
        index.html                                   # Local clickable visual gallery
        inventory.json                               # 30 screen IDs + source paths + pair mapping
        SHA256SUMS.txt                               # Integrity manifest
        original\stitch_individual_owner_design_system\
          <each-screen-slug>\code.html
          <each-screen-slug>\screen.png
          individual_owner_design.md
          cosmicforge_individual_owner\DESIGN.md
      decisions\                                    # Approved corrections and exceptions
      tokens\tokens.json                             # Platform-neutral authoritative token set
    docs\
      FRONTEND_PROGRESS.md
      SCREEN_COVERAGE.csv
      MOCK_ACCEPTANCE.md
      API_MAPPING.md                                 # Later integration; verified only
      KNOWN_LIMITATIONS.md
    web\                                             # Next.js app
    mobile\                                          # Flutter app
```

The gallery must display every PNG as a clickable card, identify desktop/mobile pairs and link to the adjacent original HTML. The gallery is a **reference viewer only**, not the deployed app. Avoid copying large reference screenshots into a final application bundle. Discuss Git LFS/artifact storage or repository file-size rules before committing large binaries; never silently commit an entire archive if repository policy forbids it.

## 4.3 Initial safe PowerShell preparation

```powershell
Set-Location 'C:\Projects\cosmicforge-realestate'
git status --short
git fetch origin
git switch development
git pull --ff-only origin development
# Only create a feature branch if it does not already exist:
git switch -c feature/individual-owner-frontend-mock

$zip = 'apps\individual-owner\stitch_individual_owner_design_system.zip'
$destination = 'apps\individual-owner\design\stitch\original'
if (-not (Test-Path -LiteralPath $zip)) { throw "Stitch ZIP not found: $zip" }
Get-FileHash -Algorithm SHA256 -LiteralPath $zip
New-Item -ItemType Directory -Path $destination -Force | Out-Null
Expand-Archive -LiteralPath $zip -DestinationPath $destination -Force
```

**Execution safeguards:** If the Git worktree contains changes or the branch already exists, inspect and preserve work instead of running destructive reset/clean commands. If `development` advanced or dependencies changed, reconcile with the current repository before initializing new apps. The coding agent should automate creation of `index.html`, `inventory.json` and integrity manifests rather than manually rename 30 folders.

# 5. Screen reference catalogue (15 desktop / 15 mobile pairs)

These entries are the **existing export references**, not a ceiling on the number of app routes required. Each pair is expected to grow into supporting list, detail, create/edit, approval and exception states.

| Pair | Existing desktop reference folder | Existing mobile reference folder |
|---|---|---|
| 01 | `owner_dashboard` | `owner_dashboard_mobile` |
| 02 | `the_sapphire_horizon_property_detail` | `the_sapphire_horizon_mobile_property_detail` |
| 03 | `create_listing_wizard` | `create_listing_wizard_mobile` |
| 04 | `treasury_finance_wallet` | `finance_wallet_mobile` |
| 05 | `tenancies_leases_authoritative_ledger` | `tenancies_rent_mobile` |
| 06 | `lease_agreement_review_countersigning` | `lease_countersign_handover_mobile` |
| 07 | `short_let_management_booking_calendar` | `short_let_management_mobile` |
| 08 | `maintenance_preventive_care_professional_services` | `maintenance_repairs_mobile` |
| 09 | `commercial_leases_registered_land_parcels` | `commercial_land_portfolio_mobile` |
| 10 | `property_sales_closing_escrow_room` | `property_sales_closing_room_mobile` |
| 11 | `ownership_authority_co_owner_governance_delegated_management` | `ownership_delegation_mobile` |
| 12 | `reports_portfolio_intelligence` | `reports_analytics_mobile` |
| 13 | `settings_security_hub` | `account_settings_security_hub_mobile` |
| 14 | `owner_identity_verification_onboarding` | `owner_identity_verification_onboarding_mobile` |
| 15 | `rental_applications_tenant_screening_dossier` | `rental_applications_tenant_screening_dossier_mobile` |

Each reference is viewed via `<folder>/screen.png` or `<folder>/code.html` under the extracted `stitch_individual_owner_design_system` directory. Preserve original screenshots as comparison baselines for visual QA.

# 6. Information architecture and reusable UI system

## 6.1 Global navigation

**Web:** owner workspace shell with left sidebar; contextual property/portfolio switcher; top search, notifications, account menu; accessible breadcrumbs and cross-module deep links; responsive tablet rail/collapsible drawer. Avoid forcing users into an artificial 30-item flat sidebar: use grouped navigation, global search and contextual links.

**Mobile:** top app bar, reliable back behavior, task-based stacks and fixed root tabs `Home / Properties / Listings / Wallet / More` (subject to usability validation). Group secondary modules under More/context. Respect safe areas, keyboard insets, Android back, iOS back gestures and pending draft alerts.

**Navigation acceptance:** Every visible action has an implemented destination, modal, safe disabled explanation or feature-gated state. No `href="#"` without a legitimate explicit placeholder state. Every detail route supports a return path and direct URL/deep link where appropriate.

## 6.2 Design tokens and visual quality

Preserve the Stitch brand direction: primary sapphire `#2459E0`, dark/light surfaces (`#F6F8FC`, `#FFFFFF`, `#0C1525`, `#17253A`), text (`#17263C`, `#ECF3FF`), statuses (success teal, warning gold, danger red), Inter if licensed/available, consistent shadows, spacing and rounded corners. Consolidate conflicting radius values between source documents into an explicit approved `tokens.json` decision. Create matching Tailwind variables/theme and Flutter `ThemeData` values from that source. No duplicated hardcoded theme colors spread through business widgets.

**Required components:** app shell, navigation, breadcrumbs, properties grid, data tables, filters, calendar, KPI cards, transaction status/balance buckets, forms, date picker, file/evidence picker, document preview, pricing breakdown, permissions panel, multi-step progress, dialogs, bottom sheets, empty/loading/error/restricted/offline states, toast/status updates and confirmation flows.

## 6.3 Web/mobile acceptance dimensions

- Web layouts at 1440/1280 px, 768–1024 px tablet and small-browser widths; native mobile checks at common phone sizes, with larger accessibility text.
- Light and dark themes both implement all shared components and meaningful screen families; no merely theoretical dark tokens.
- Contrast, focus, screen reader semantics, keyboard behavior, tap targets ≥44 px when practical, reduced motion, readable financial numbers and explicit status labels.
- Avoid external Tailwind CDN and unstable hotlinked property images in application source. Use build-time Tailwind and licensed/local/mock assets, or validated media access in the integration stage.

# 7. Application architecture: mock-first, integration-ready

## 7.1 Clean interface boundary

Domain features must depend on **repository interfaces**, not on actual HTTP calls in components. Provide interchangeable adapters:

```text
UI / screens / widgets
       ↓
Feature controller, query/cache and form validation
       ↓
Domain repository interface / typed models
       ├── Mock repository  (active during entire mock phase)
       └── Gateway repository (created only after mock approval)
```

**Web suggested layout:** `src/app` routes; `src/features/<domain>` feature views, models and controllers; `src/components` for UI; `src/design` tokens; `src/lib/mock` deterministic local fixtures; `src/lib/contracts` integration-facing types. Follow an existing monorepo convention if one appears before initialization.

**Flutter suggested layout:** `lib/app`, `lib/core/theme`, `lib/core/router`, `lib/core/mock`, `lib/features/<domain>/presentation`, `domain` and `data` with Riverpod providers. Keep model semantics coordinated with web via a shared contract mapping or generated definitions; do not force Dart to import TypeScript types.

## 7.2 Mock-mode behavior rules

1. Mock mode is the only active data source before approval; no Owner API requests, direct provider requests, live payment initiation or real verification commands.
2. Seed all example records as clearly fictional; use consistent mock IDs, owner profiles, properties, units, listings, renters, booking reservations, funds and governance cases.
3. Actions update local repository state and immediately re-render affected screens. Cross-module consistency is mandatory: approving an application should affect the relevant application/offer/tenancy journey according to the defined simulated rules.
4. Mock states include `empty`, `ready`, `pending`, `restricted`, `error`, `offline`, `expired_session`, `access_denied`, `conflict`, `provider_unavailable` and `unknown_outcome` where relevant. Use a developer scenario switcher to reproduce cases.
5. Use stable fake references, dates and currency; mark high-stakes surfaces `DEMO DATA — NO REAL TRANSACTION` clearly. Simulated actions should say “Recorded in demo” rather than “Funds transferred” or “Government verified.”
6. Persist mock drafts/state in local development storage only when necessary, with a **Reset Demo Data** action. Never mix it with real credentials or customer data.
7. The same fixture scenarios and state transitions must be expressible in web and mobile; tests assert parity of user-visible behavior.
8. Defer production telemetry, push tokens, providers, contracts and authentication to the later integration phase. The architecture must remain capable of wiring them without rewriting UI flows.

## 7.3 Mock data families

`OwnerAccount`, `AuthSession`, `VerificationCase`, `Property`, `PropertyUnit`, `LandParcel`, `OwnershipClaim`, `OwnershipParty`, `ConsentRequest`, `ManagerProposal`, `ManagerAssignment`, `Listing`, `PriceModel`, `Availability`, `Inquiry`, `Viewing`, `RentalApplication`, `RentalOffer`, `Agreement`, `Tenancy`, `RentObligation`, `DepositCase`, `UtilityAccount`, `MeterReading`, `ServiceChargeBudget`, `CommercialLOI`, `SaleTransaction`, `ShortletBooking`, `TurnoverJob`, `DamageClaim`, `MaintenanceRequest`, `ProfessionalAppointment`, `Notification`, `WalletSnapshot`, `Statement`, `PortfolioReport`, `DisputeCase`, `PrivacyRequest`, `InsuranceRecord`, `FinancingAvailability`, `AccessDevice`.

These are conceptual model groups for mock design, **not claims that exact OpenAPI DTO names or fields exist**. At integration, align types and field semantics to the checked-out contracts.

# 8. The complete 30-module mock acceptance register

**Acceptance rule:** “Module complete” means **functional mock flow complete on web and Flutter** with list/overview, relevant detail, create/edit/decision actions, state transitions, validation and error/empty/blocked states. A reference screenshot alone never counts. Modules with provider gate C must implement truthful gated/read-only experiences, not simulate certified capabilities.

## 8.1 Foundation and owner authority (01–06)

| # / area | Minimum interactive mock behavior | Special constraints |
|---|---|---|
| **01 Dashboard / Portfolio** | Portfolio switch, KPI drilldown, tasks, alerts, calendar, property cards, activity; empty-account onboarding | KPIs from coherent mock records; do not invent audit/ledger provenance |
| **02 Registration / Security / Identity** | Register/login/recover, OTP and MFA UX simulation, profile, sessions, security step-up, identity evidence submit/review/resubmit | Fake auth only; label demo; do not claim live BVN/NIN or hardware enrollment |
| **03 Payout & Tax Profile** | Masked payout account list/add/edit/default request, tax-profile input, statements, security confirmations | No real bank account validation, tax certification or payout success |
| **04 Ownership & Authority** | Ownership claims, parties/shares, co-owner invite/response, consent requests/decisions, representatives, transfer status | Honor consent gates; no mock manager title transfer or automatic verified deed |
| **05 Property / Portfolio Management** | Property list/create/edit/detail; units, parcels, assets, media and history; filtering/archive confirmation | Enforce mock owner scope and version/conflict UI |
| **06 Property Verification** | Claim evidence upload, submit, pending/review/correction/rejection/approval demo states; history | Verification truth never inferred from document upload |

## 8.2 Listings and tenant acquisition (07–12)

| # / area | Minimum interactive mock behavior | Special constraints |
|---|---|---|
| **07 Property Listings** | Draft/create/edit/preview/publish/pause/withdraw/copy/archive; property-type branching | Publication pre-flight separate from ownership verification |
| **08 Pricing & Fees** | Price model edit/history, cost preview and itemized fee breakdown | Use configurable *sample* policies, no universal invented statutory caps |
| **09 Availability** | Calendar blocks/unblocks, availability confirmation, stale status, conflict/retry states | Booking/availability transitions consistent with mock short-lets |
| **10 Inquiries / Leads** | Dedicated list/detail, notes, priority, acknowledge/qualify/close, reminders, campaigns where supported | Privacy-aware criteria; human chat is gated through CF Meet |
| **11 Viewings & Access** | Slots, request approval, reschedule, cancel, consent, representative, check-in, outcome/no-show | Mock-only access code; no real door PIN or unsafe sharing links |
| **12 Applications & Screening** | Application list/detail, evidence request/grants, human review, screening assessment, approve/reject/appeal, rental offer | No fabricated bank feed, court records, guaranteed background checks or automated discrimination |

## 8.3 Tenancies, bills and finance (13–16)

| # / area | Minimum interactive mock behavior | Special constraints |
|---|---|---|
| **13 Agreements & Move-in** | Draft/version/review/invite signers/signature-state demo/activate/move-in checklist/handover | Distinguish offline signature recording from verified e-sign provider |
| **14 Rent / Deposits / Wallet** | Rent obligations, payment status timeline, held/pending/available/restricted buckets, deposit proposal/dispute, mock receipts | No payout, escrow release or settlement claim without authoritative future state |
| **15 Renewals / Notices / Move-out** | Renewal propose/respond, notice draft/issue/delivery, inspections, keys, deposit case and closure | Legal rules and delivery status are mocked; no statutory compliance claim |
| **16 Utilities & Service Charges** | Utility accounts, meter entries, bills, allocation rules, budgets, service-charge items and disputes | Must have standalone flow, not only a table row in tenancy |

## 8.4 Transaction and commercial operations (17–23)

| # / area | Minimum interactive mock behavior | Special constraints |
|---|---|---|
| **17 Commercial Rental / Sale** | Commercial terms, LOIs, negotiation, lease calendar, offer actions | Tax/VAT/WHT examples clearly non-authoritative |
| **18 Land Rent / Lease / Sale** | Parcels/land terms, site inspections, documents, offers, leasing/sale progress | No fake title guarantees, land registry sync or GPS beacon certification |
| **19 Property Sales** | Sale transaction, offers/counteroffers, diligence checklist, document room and grants, milestone progress, transfer request | Closing, consent and financial settlement must remain separate statuses |
| **20 Short-Let Operations** | Settings/rates, multi-unit calendar, booking approve/decline, check-in/out, extensions, turnover, claims and reports | No hardware actuation, marketplace/channel sync or confirmed payment mock claim |
| **21 Property Manager Delegation** | Proposals, assign property, permission matrix, consent, activity, modify/suspend/resume/revoke | Never grant owner payout-redirection/title authority by delegation |
| **22 Maintenance & Preventive Care** | Request/triage, quotes, compare/approve, schedule, job events, completion, warranty, preventives | Provider quotes and reserve usage remain simulated; no actual disbursement |
| **23 Professional Services** | Categories, appointment request, provider info/unavailable, deliverables, findings, accept/dispute | Provider discovery/credentials not live unless separately certified later |

## 8.5 Communication, insights, safety and provider-dependent areas (24–30)

| # / area | Minimum interactive mock behavior | Special constraints |
|---|---|---|
| **24 Notifications / Communication** | Full inbox, filters, read/unread, preferences, contextual deep links, delivery vs read state | CF Meet messaging/calls gated until actual integration |
| **25 Reports / Insights** | Filters, range selection, portfolio statement/report request demo, downloadable demo export, property drilldowns | Mock calculations reproducible from fixture data; no fake FIRS certification |
| **26 Fraud / Privacy / Disputes** | Dispute list/open/evidence/timeline, activity and privacy consent/requests; restricted states | Avoid allegations of verified fraud; protect evidence fields |
| **27 Accessibility / Assisted Use / Closure** | Settings, language/appearance, delegated assistance, data rights and closure workflow simulation | Account deletion blocked/pending reason where relevant |
| **28 Financing / Mortgages** | Information/read-only reference, application-interest UI only if permitted, provider unavailable/coming-later state | Gate C: no loan rates/eligibility/approvals or false launch date |
| **29 Insurance / Protection** | Existing insurance *demo record* list/detail/history, expiry reminders, partner unavailable state | Gate C: no real policy issuer, coverage guarantee or underwriting |
| **30 Smart Access / Remote Control** | Device register/read-only, temporary grant mock, access review/expiry/revoke demo, device-offline errors | Gate C: never display real PIN, send in URL, or actuate a device |

**No double-counting:** Features shared across modules (financial status, media uploads, verification) are implemented as shared components/adapters once. Each module is accepted only for its own meaningful owner workflow. A single settings card or alert icon does not complete another module.

# 9. Required end-to-end mock journeys

The following journeys must run as consistent scenarios on both platforms. Each is exercised against fixtures and controls, without live servers:

1. **Owner onboarding:** create demo owner → login → simulated MFA → submit identity evidence → see review/pending and corrective states → open empty portfolio.
2. **Property-to-listing:** create property → add unit/land parcel → claim ownership → attach document → verification review → draft listing → configure pricing and availability → preview → simulate approved publication gate.
3. **Prospect-to-tenancy:** receive inquiry → acknowledge/qualify → open/approve viewing → applicant submits demo evidence → human screening decision → offer → agreement draft/review/signature state → move-in.
4. **Rent and deposit:** generate rent obligation → simulate funding observed → show separate held/available/restricted state → raise/dispute deposit deduction → simulate authorized resolution without claiming a real payout.
5. **Lifecycle:** lease renewal → notice → key return and inspection → deposit case → tenancy closure with accurate status propagation.
6. **Short-let:** configure unit/rates → reserve available dates → mock guest approval → prep/turnover → check-in/out → record damage claim → calendar frees dates consistently.
7. **Maintenance:** tenant/owner raises problem → request and compare quotes → approve simulated vendor → job progress → completion acceptance or dispute → reports update.
8. **Ownership & delegation:** add co-owner → request consent → manager proposal/assignment → restrict capabilities → review activity → revoke; revoked user sees denied state.
9. **Sale / land / commercial:** list parcel or commercial unit → offer/LOI → review/counter → diligence → document grants → transfer/closing milestones, kept separate from cash settlement.
10. **Insights and privacy:** notification → contextual record → dashboard/report filter/export → privacy request/dispute and account-security review.

All important actions must have a destination and return path. A successful mocked operation must update at least one appropriate list/detail/activity state and survive within the active demo session.

# 10. Correct the known risks in exported designs

**P0 before exposing any mock to reviewers:**

- Remove simulated completed payouts/disbursements triggered by timers, toasts or local numeric checks. Use “demo action recorded; no funds moved.”
- Remove unverified GTBank custody, ring-fenced escrow guarantees, FIRS tax stamps/rates, 100% unencumbered title, fictitious cadastral verification, biometric BVN/NIN lookup and named insurer certifications. Example data must not look like a real bank/gov endorsement.
- Remove the exported short-let WhatsApp URL that embeds a door-access PIN. Never seed a usable PIN in URLs, logs or source templates.
- Replace “Sovereign Principal / Sovereign Asset / Guaranteed Title” language with Property Owner, Ownership Record, Verification Pending/Verified by authority where applicable; use controlled, precise finance states.
- Use only approved branding and properly licensed local images; exported Google-hosted URLs are reference-only, not production content. Fix Tailwind browser CDN usage.
- Make badges and action controls reflect state/permissions. Never show a disabled-but-executable sensitive action.

**P1 during full mock:** implement dedicated lead center, viewings, utilities, full notification inbox, disputes and insurance/provider gating. These were historically represented by contextual dashboard cards rather than complete journeys.

# 11. Engineering phases and evidence gates

| Phase | Deliverables | Exit / acceptance evidence |
|---|---|---|
| **0. Reference vault** | ZIP preserved, extracted folder, integrity inventory, local gallery; source metadata; 15 paired references | Gallery opens locally; count 30 HTML + 30 PNG + 2 MD; every card links to a real file |
| **1. Application foundation** | Next.js and Flutter scaffolds, tokens, light/dark, app shells, routing, mock repository boundary, fixtures, error components | Both run locally; theme/nav tests pass; mock-only request guard working |
| **2. Core owner mock** | Modules 01–12 complete in web/mobile; forms, verification and property/listing/lead/viewing/application journeys | Journey tests 1–3 pass, screen coverage file current |
| **3. Lifecycle and operations mock** | Modules 13–23: tenancy, bills, wallet, commercial/land/sale, short-let, delegation, maintenance/professionals | Journeys 4–9 pass, no fake completed financial/legal actions |
| **4. Cross-cutting mock** | Modules 24–30; notifications, reports, dispute, privacy, accessibility and gated providers | Journey 10 pass, dark mode and device accessibility checks |
| **5. Full mock closure** | Independent functional audit, visual regression, web/mobile parity, mock mode UX review, issues register | **User approval required before API integration**; demonstrate every module with passing evidence |
| **6. Contract integration** | Gateway adapter, actual auth, owner operations, readbacks, typed errors, session security; remove demo-only assertions | Contract tests + available live dev/staging e2e; no silent stub success |
| **7. Certification / deployment** | Security and legal reviews, provider-specific checks, staging testing, monitoring and release plan | Independent deployment authorization; distinguish code closure from provider/production sign-off |

Phases 0–5 should proceed continuously inside the agreed scope without stopping after each screen, but do not skip required test checkpoints. Phase 6 **cannot** begin without explicit approval of the mock. Phase 7 cannot be claimed simply because local tests pass.

# 12. Quality assurance and test contract

## 12.1 Automated tests

**Web:** TypeScript type checking, linting, production build, unit/component tests, mock repository transition tests, route access tests and Playwright (or established project E2E runner) end-to-end journeys. Use existing repo tooling before introducing a second framework.

**Flutter:** `flutter analyze`, widget tests for screens, provider tests for mock state, navigation tests, golden/image tests where practical, and integration tests on emulator/device for keyboard/safe-area/navigation.

**Source/audit checks:** no hardcoded live API URLs in mock code, no `fetch()`/Dio live requests in mock mode, no real credentials/PII, no dangerous PIN links, no unconditional success after a delayed timer, no `href="#"` for essential actions, no fabricated title or tax claims.

## 12.2 Test scenario matrix

Every meaningful feature must cover at least: happy mock path, empty, loading, invalid form, cancel/back, inaccessible/permission denied, conflict/duplicate, retry, provider unavailable where relevant, and mobile navigation continuity. Financial and ownership actions additionally cover restricted, pending, step-up-required and ambiguous/unknown-outcome presentations.

**Reports must say:** tests actually executed; test counts and pass/fail; environment; exact commit; known failing/skipped tests and reasons. Never claim a successful backend integration test while the mock adapter is the active source.

## 12.3 Human UX review

Perform desktop visual checks at 1440 and 1280; responsive web at 768 and 390; Flutter at Android and iOS phone sizes where devices are available. Compare with the 30 reference PNGs, check long content, dark-theme contrasts, text scaling and sensitive-copy corrections. Record *differences with rationale* instead of chasing pixel-identical layouts at the expense of functionality.

# 13. Future backend integration: after mock approval only

## 13.1 Integration gate

Before enabling any real adapter, approve: an accepted full mock, approved design copy, a pinned backend SHA/OpenAPI version, non-production Gateway base URL, session storage policy, credential handling, any required backend changes and external provider readiness. Missing external partners are documented and gated, not treated as frontend defects.

## 13.2 Implement contract-correct Gateway adapters

Generate or derive types from the checked-out OpenAPI; verify method, path, inputs, response schemas, role checks, error responses and concurrency policies. The following are **verified illustrative paths** relative to `/api/v1/owner`; they are not a substitute for full contract discovery:

| Feature | Example actual owner operation |
|---|---|
| Dashboard | `GET /dashboard` |
| Portfolio | `GET /properties`; `GET /properties/{property_id}` |
| Identity verification | `POST /verification` |
| Listing draft creation | `POST /listings` |
| Listing publication | `POST /listings/{listing_id}/publish` |
| Inquiry lead | `GET /inquiries` |
| Application review | `GET /applications/{application_id}` |
| Agreement review / signature state | `POST /agreements/{agreement_id}/send-for-signature`; `POST /agreements/{agreement_id}/signatures` records offline signatures |
| Wallet status | `GET /financial-wallet` |
| Short-let bookings | `GET /shortlet/bookings` |
| Maintenance | `GET /maintenance`; `POST /maintenance/{request_id}/approve` |
| Sale detail | `GET /transactions/sales/{sale_id}` |
| Manager assignments | `GET /manager-assignments`; `PATCH /manager-assignments/{manager_assignment_id}` |
| Portfolio reports | `GET /reports/portfolio` |

**Integration behaviors:** verified sessions/auth; least-privilege scopes; authenticated public Gateway only; step-up for sensitive operations; optimistic concurrency/`If-Match` when required; idempotency keys where required; masked fields; purposeful document grants; readbacks after ambiguous outcomes; retries without duplicate commands; and API-shaped error surfaces (403, 404, 409/412, 422, 429, 503).

## 13.3 External dependencies

External verification, digital signature provider execution, professional/manager discovery, financing, insurance, physical smart locks, marketplace connectivity and CF Meet can remain unavailable. UI must reflect actual backend/readiness response, including 503/unavailable. **Provider certification is distinct from frontend code completion**. A functional local page never proves provider readiness.

## 13.4 Release decision

Release requires separately documented staging user journeys, authorized data access, financial reconciliation behavior, security/privacy/accessibility checks, approved legal content, external provider approval and operational monitoring. Do not auto-merge to `staging` or `main` or deploy on the strength of a UI build.

# 14. Repository discipline, progress bookkeeping and handoff

- Work on `feature/individual-owner-frontend-mock` or an appropriately named existing feature branch, based on `development`; never override other developers' changes. Branch workflow is `feature/*` → `development` → `staging` → `main` upon authorization.
- Phase 0 may update only the Individual Owner design directory and supporting manifest files; subsequent mock implementation is confined to `apps/individual-owner/web`, `mobile`, shared owned design/doc files, and established package configuration where needed.
- Do not modify backend services or contracts during mock development. Report a contract inconsistency rather than silently changing server behavior.
- Commit in bounded functional slices; don't make one huge unreviewable commit. Preserve each successful slice; avoid reworking passed modules without a discovered failure.
- Update `apps/individual-owner/docs/FRONTEND_PROGRESS.md` and `SCREEN_COVERAGE.csv` after each phase, using `NOT_STARTED`, `IN_PROGRESS`, `MOCK_PASS`, `INTEGRATED_PASS`, `EXTERNAL_PENDING`, `BLOCKED` rather than a binary “complete.”
- Reports must contain exact SHA/branch, touched files, web and mobile preview instructions, mock scenarios, tests and evidence, blockers, user decision needed (if any), and **the exact next unfinished unit of work**.
- Never claim a GitHub push, environment deployment or file export without checking its result.

# 15. Master execution directive for the coding agent

Copy this entire document into the repository at:

`apps/individual-owner/docs/INDIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.md`

Then give the coding agent the following **one-time execution directive**. The agent must follow the whole document rather than require a new prompt for every module.

> **IMPLEMENTATION MANDATE — INDIVIDUAL OWNER**  
> You are implementing the CosmicForge Real Estate Individual Owner responsive web and native mobile applications using the checked-out `development`-based feature branch and the attached `INDIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.md`. Follow this document as the execution authority for sequencing and acceptance; use the real repository README/OpenAPI as architecture and API authorities, and the unmodified Stitch export at `apps/individual-owner/stitch_individual_owner_design_system.zip` as a visual source.  
> **Begin with Phase 0:** preserve the original ZIP, extract it safely, verify the 30 HTML/30 PNG/2 MD inventory, generate a clickable local gallery in `apps/individual-owner/design/stitch/index.html`, and record the SHA-256 manifest. Then implement Phase 1 (Next.js + Flutter foundations) and progress through Phases 2, 3 and 4 until the full **30-module mock** works on both platforms.  
> Use Next.js/React/TypeScript/Tailwind/TanStack Query/React Hook Form/Zod for web and Flutter/Dart/Riverpod/GoRouter/Dio for mobile. Create shared semantic design tokens, light/dark themes, navigation, fixtures and interchangeable repositories. **Use mock repositories exclusively through Phases 0–5; do not contact the backend or third-party providers.**  
> Make every important form and action work locally. Populate coherent fictional data, enforce simulated permission/status guards, and include all list/detail/create/edit/approval/empty/loading/error/restricted/unavailable states required by the 30-module register. Preserve source references and correct misleading legal/financial/verification claims and hardcoded door PINs.  
> Work in logical commits and test at every phase boundary. Do not repeat already-passed work or stop after an illustrative dashboard. Keep `FRONTEND_PROGRESS.md`, `SCREEN_COVERAGE.csv`, and `KNOWN_LIMITATIONS.md` current. Run real local checks; report exactly what passed, what could not run and the next unfinished scope. If an essential blocker occurs, explain the smallest action needed.  
> **Stop at the Phase 5 full mock review gate. Do not integrate APIs, merge to protected branches, activate providers or deploy without explicit user approval.** After approval, the separate Phase 6 integration mandate will use the pinned OpenAPI and Gateway only. Begin now by inspecting the workspace, then execute Phase 0.

# 16. Definition of done and owner approval checklist

## 16.1 Full mock approval (required before integration)

- [ ] Original reference ZIP preserved and visible in project; integrity and 30-screen gallery verified.
- [ ] Next.js project builds and runs; Flutter project analyzes/tests and runs where toolchain permits.
- [ ] All 30 modules have working mock journeys on **both** platforms or truthful provider-gated screens as defined.
- [ ] Supporting screens and modals exist beyond the 30 Stitch reference screenshots.
- [ ] Demo forms validate input, mutate mock state, and preserve cross-module consistency.
- [ ] Light and dark modes pass visual and contrast checks.
- [ ] Empty, error, loading, denied, restricted, unknown and unavailable cases are reviewable.
- [ ] No Owner backend calls, real payments, government checks, e-sign claims or door-lock commands occurred.
- [ ] All fake escrow, government registration, insurer, tax and payout guarantees are removed or explicitly labeled demo.
- [ ] No real keycodes, credentials or private tenant evidence are exposed in URLs, screenshots, source or log output.
- [ ] Contract adapters are isolated; API mappings for integration are documented but inactive.
- [ ] Functional tests, screenshot evidence, mock coverage register and outstanding issues are attached.
- [ ] User reviewed the actual local web/mobile mock and explicitly authorized API integration.

## 16.2 Integration / staging closure (separate decision)

- [ ] OpenAPI pinned and all client operations verified with correct paths, verbs and schemas.
- [ ] Authentication, MFA/step-up, role/consent and safe session handling tested against non-production backend.
- [ ] Ownership, listing, application, tenancy, wallet and operations journeys pass real Gateway/DB/service tests as available.
- [ ] No optimistic financial/verification success; readbacks and ambiguous outcomes handled.
- [ ] External integrations remain correctly gated until provider evidence is available.
- [ ] Staging deploy and security/privacy/legal release gates independently approved.

# 17. First action and next milestone

**Start with Phase 0 only:** put the untouched Stitch export into an easily viewed, structured reference gallery inside the project folder. The immediate evidence should be a local HTML gallery displaying all 30 PNGs plus links to their 30 source HTML files, with the design Markdown available beside it. Then build the reusable mock app foundation; after that, cover all 30 modules end to end. This single document governs the full sequence and avoids repeated ad hoc prompt writing. The official Postman, OpenAPI and endpoint catalog resources referenced in Appendices A–D must remain the contract authority during the later integration phase.

**Document control:** Update this plan by versioned change note only if the project scope or architecture changes. Track actual execution status separately in `FRONTEND_PROGRESS.md`; do not rewrite historical evidence to claim completion.


---

# APPENDIX A. Existing backend documentation, Postman and OpenAPI — exact paths

**This is an existing, documented backend; frontend work must not invent a replacement API specification.** Verified against the GitHub `development` source at `ee3b8bdfd15f9805a615f10948eac89481bee0b3` on 17 October 2026. The agent must re-check the working-tree SHA before doing integration work.

## A.1 Canonical files and clickable source links

| Purpose | Exact repository path | Authority |
|---|---|---|
| Official complete Postman collection | `postman/CosmicForge_RealEstate.postman_collection.json` | **Human-facing frontend / QA API handoff** |
| Postman import and request instructions | `postman/README.md` | Official workflow; environment variables; `02 Individual Owner` organization |
| Local Postman environment | `postman/environments/local.postman_environment.json` | Local gateway `http://localhost:8080` |
| Development Postman environment | `postman/environments/development.postman_environment.json` | `base_url` is a placeholder until provisioned |
| Staging Postman environment | `postman/environments/staging.postman_environment.json` | `base_url` is a placeholder until provisioned |
| Owner authentication, session and account OpenAPI | `contracts/public/openapi/individual-owner.yaml` | Machine-readable client auth contracts |
| Owner domain OpenAPI | `contracts/public/openapi/individual-owner-domain.yaml` | Machine-readable business-domain contracts |
| Gateway OpenAPI | `contracts/public/openapi/api-gateway.yaml` | Public gateway context |
| Payment OpenAPI | `contracts/public/openapi/payments.yaml` | Payment-service owner of monetary truth; not a license for Owner UI to call internal payment routes |
| Verification OpenAPI | `contracts/public/openapi/verification.yaml` | Verification-service contract / provider boundary |
| Media OpenAPI | `contracts/public/openapi/media.yaml` | Signed upload and document states |
| Notification OpenAPI | `contracts/public/openapi/notifications.yaml` | Notification-service boundary |
| Accepted API documentation policy | `docs/architecture/API_DOCUMENTATION_AND_POSTMAN.md` | States Postman is official frontend handoff; Swagger UI not required |
| Backend architecture/stack | `README.md`; `docs/architecture/TECH_STACK.md` | Technology and service boundaries |
| Owner source specification | `docs/product/individual-owner/CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.pdf` | Original immutable product/backend specification |
| Searchable specification text | `docs/product/individual-owner/CosmicForge_Real_Estate_Individual_Owner_Backend_Implementation_Spec_v1.0.txt` | Convenience extraction (PDF wins on discrepancies) |
| Owner implementation coverage | `docs/product/individual-owner/IMPLEMENTATION_COVERAGE.md` | Local backend closure versus external gaps |
| Owner final acceptance | `docs/product/individual-owner/SECTION_24_FINAL_ACCEPTANCE_AND_APPENDIX_RECONCILIATION.md` | Latest documented acceptance and staging/provider qualifications |
| Jurisdiction policy | `docs/product/individual-owner/LEGAL_POLICY.md` | Configuration and unapproved policy decisions |
| Payment handoff/constraints | `docs/product/payments/EXTERNAL_DEPENDENCIES_AND_HANDOFF.md`; `FINANCIAL_SCHEMA_AND_INVARIANTS.md` | Financial truth and external readiness |
| Owner router and public operations | `services/individual-owner-service/internal/http/router.go`; `routes_*.go` | Implemented route handlers (check against OpenAPI) |

**GitHub navigation:** `https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/tree/development/` followed by the repository-relative path above. The collection opens at `https://github.com/CosmicforgeHealthnet/cosmicforge-realestate/blob/development/postman/CosmicForge_RealEstate.postman_collection.json`. These links require repository access.

## A.2 Official Postman navigation and safe use

After importing `postman/CosmicForge_RealEstate.postman_collection.json`, navigate to:

```text
CosmicForge Real Estate (official collection)
  00 Platform
  01 Marketplace
  02 Individual Owner              <-- ALL OWNER FRONTEND HANDOFF REQUESTS
      [Owner public request subfolders]
      _Internal                    <-- NOT for public web / Flutter clients
      _Admin                       <-- NOT for public web / Flutter clients
  03 Business Operations
  04 Property Manager
  05 Artisan
  06 Professional Services
  07 Admin
  08 Verification
  09 Payment
  10 Notification
  11 Media & Documents
```

The Owner collection handoff records **365 public operations and 90 signed internal/admin requests**, including implemented specification aliases (as stated in `postman/README.md`). **365 public operations** are independently counted in OpenAPI: **43** account/security operations in `individual-owner.yaml`, and **322** domain operations in `individual-owner-domain.yaml`. This is the *API operation count*, not the number of visual screens. Do not confuse Postman internal/admin requests with UI-accessible APIs.

**Postman sequence (integration phase only):** (1) Import the collection; (2) import the environment; (3) select `Local`, `Development` or `Staging`; (4) configure `base_url` to the approved Gateway (never direct service URLs for app traffic); (5) authenticate as Owner and capture `individual_owner_token`/other tokens according to the collection; (6) select relevant IDs deliberately (`property_id`, `listing_id`, `owner_account_id`); (7) execute the public request and inspect success and error examples. `Development` and `Staging` committed URLs are placeholders, not a claim of live deployment.

**Variables:** `base_url`, `access_token`, `individual_owner_token`, `owner_refresh_token`, `owner_account_id`, `owner_account_version`, `owner_challenge_id`, `owner_mfa_token`, `owner_session_id`, `owner_device_key`, `property_id`, `listing_id`, `request_id`, `idempotency_key`. Other service- and operation-specific IDs exist in the collection. The Owner folder supports `owner_retry_idempotency_key` **only for the exact same command/body retry**, to avoid duplicate execution; clear it before any new command. Never commit real access tokens, passwords, keys, signed upload URLs or private data.

**Signature and authorization boundaries:** Public owner requests use the owner bearer token through `{{base_url}}/api/v1/owner/*`. `_Admin`, `_Internal` and signed service requests use different privileges and must **never** be exposed through web/mobile code. Each error/success example is a contract illustration, not proof that a third-party provider is operational.

## A.3 Machine-readable catalogue of every public operation

This package includes `generate_owner_endpoint_catalog.py`. It reads the **actual checked-out OpenAPI files**, enumerates **every method, full path, operationId, summary, tag and source file**, and attempts to cross-reference the public `02 Individual Owner` Postman folder without requiring third-party Python packages or network access.

Run from a checked-out repository:

```powershell
cd C:\Projects\cosmicforge-realestate
python .\apps\individual-owner\docs\generate_owner_endpoint_catalog.py --repo .
```

It generates (under `apps/individual-owner/docs/api-reference/`):

- **`OWNER_ALL_PUBLIC_ENDPOINTS.csv`** — the full method/path catalogue (365 rows at the inspected SHA; count may change on future commits).
- **`OWNER_API_INDEX.md`** — first-segment route-family inventory and locations.
- **`OWNER_API_REFERENCE_STATUS.json`** — file counts and best-effort Postman cross-reference summary.

If a Postman route is not automatically matched, inspect the actual request manually: Postman aliases, template variables and naming differences can cause false mismatches. **Do not auto-create routes** because a route is absent from the index. The checked-out OpenAPI and backend handler remain authoritative. This deliberately avoids copying 365 mutable route definitions into a manually maintained document that will go stale.

# APPENDIX B. API family index and module-to-source map

## B.1 Verified OpenAPI endpoint family counts

The following counts were determined from the checked `development` OpenAPI, not invented from Stitch. Each route below is prefixed by `/api/v1/owner/`; count = distinct public **method+path** operations, not endpoints with distinct URLs.

| Family under `/api/v1/owner/` | Public operations | Primary UI responsibility |
|---|---:|---|
| `auth` | 19 | Registration, sign-in, OTP, MFA, refresh, step-up, recovery |
| `me` | 21 | Owner account, devices, profile security and self-management |
| `sessions` | 2 | Session/account aliases and compatible client routes |
| `properties` | 63 | Asset CRUD, units, parcels, evidence, co-ownership, verification, consents, transfers |
| `listings` | 25 | Drafting, preview, publication, pricing, availability, short-let configuration |
| `tenancies` | 23 | Tenancy, rent, deposits, move-in/out, renewals, notices, utilities |
| `transactions` | 16 | Sale transactions, offers, due diligence, data rooms and closing milestones |
| `agreements` | 12 | Templates, drafts, signatures and activation |
| `maintenance` | 12 | Repairs, quotations, approvals, completion and warranty |
| `shortlet` | 12 | Reservations, stay extensions, turnovers and damage claims |
| `financial-business` | 10 | Financial-core business views and guarded finance operations |
| `financial-wallet` | 9 | Authoritative wallet, payments-facing owner status and related operations |
| `viewings` | 10 | Viewing appointments and outcome management |
| `applications` | 8 | Screening, review, evidence and tenant selection |
| `manager-assignments` | 8 | Manager permissions, activity, suspension and revocation |
| `access` | 7 | Key/device/access grant records, gated physical providers |
| `inquiries` | 6 | Prospect and lead processing |
| `insurance` | 6 | Owner insurance records and provider-dependency states |
| `privacy` | 6 | Privacy consents and data-rights requests |
| `payout-accounts` | 5 | Bank account preference and payment-service read-back |
| `professional-appointments` | 5 | Professional appointments, findings and reports |
| `rent-obligations` | 5 | Rent due items and finance collection monitoring |
| `reports` | 4 | Portfolio reporting, export and report retrieval |
| `financing` | 4 | Financing records, interest and gated provider workflows |
| `disputes` | 4 | Owner case/evidence workflows |
| `manager-proposals` | 3 | Proposals and acceptance preparation |
| `commercial-lois` | 3 | Commercial letter-of-intent negotiation |
| `viewing-slots` | 3 | Viewing slot creation and management |
| Other public families | See generated 365-row CSV | Dashboard, calendar, co-owner invitations, transfers, profiles, utilities, charges, statutory duties, audits, assistance, tax, preferences, etc. |

**Boundary:** Some provider-dependent route families exist in OpenAPI but return unavailable or guarded responses until authorized source integrations are available. Their presence does not justify a live-looking button in the mock.

## B.2 All 30 product modules — authoritative route-location crosswalk

The table uses **route families** rather than made-up one-to-one endpoints. The generator above provides exact methods and operation IDs for every request.

| # | Product module | Find public routes in OpenAPI / Postman `02 Individual Owner` |
|---|---|---|
| 01 | Dashboard & owner calendar | `dashboard`, `calendar`, `reports/portfolio` |
| 02 | Registration, security, identity | `auth/*`, `me/*`, `verification` |
| 03 | Banking, payout preferences, tax | `payout-accounts`, `payout-preferences`, `tax-profile`, `statements`, `financial-wallet` |
| 04 | Ownership, co-owners, consents | `properties/{property_id}/ownership`, `.../co-owners`, `.../consents`, `.../ownership-transfer`, `co-owner-invitations`, `consents` |
| 05 | Properties, units, land parcels | `properties`, `properties/{property_id}/units`, `.../parcels`, `.../assets`, `.../media`, `.../history` |
| 06 | Property verification | `verification`, `properties/{property_id}/verification`, `.../verification-summary` |
| 07 | Listings | `listings`, `listings/{listing_id}/preview`, `.../publish`, `.../pause`, `.../withdraw`, `.../archive` |
| 08 | Pricing & fee transparency | `listings/{listing_id}/pricing`, `.../cost-preview`, `.../price-history` |
| 09 | Availability | `listings/{listing_id}/availability`, `.../confirm-availability` |
| 10 | Marketing, inquiries, leads | `inquiries`, `campaigns` |
| 11 | Viewing, consent, access | `viewing-slots`, `viewings`, `viewings/{viewing_id}/...` |
| 12 | Tenant application and screening | `applications`, `applications/{application_id}/...`, `offers` |
| 13 | Agreements & move-in | `agreement-templates`, `agreements`, `tenancies/{tenancy_id}/move-in`, keys and checklists |
| 14 | Rent, deposits & wallet | `rent-obligations`, `tenancies/{tenancy_id}/rent-obligations`, `.../deposit`, `financial-wallet` |
| 15 | Renewals, notices, move-out | `tenancies/{tenancy_id}/renewal`, `.../notices`, `.../move-out`, `renewals`, `notices` |
| 16 | Utilities and service charges | `properties/{property_id}/utilities`, `.../meter-readings`, `.../utility-bills`, `.../service-charge-budgets`, `utilities`, `utility-bills` |
| 17 | Commercial leasing | `properties/{property_id}/commercial-terms`, `commercial-lois` |
| 18 | Land lease/rent/sale | `properties/{property_id}/parcels`, `.../land-terms`, `transactions/sales` |
| 19 | Sale, due diligence, closing | `transactions/sales`, `transactions/sales/{sale_id}/...`, data room and ownership transfer |
| 20 | Short-let management | `shortlet/bookings`, `listings/{listing_id}/shortlet/*`, turnovers, extensions and damage claims |
| 21 | Manager delegation | `managers/search`, `manager-proposals`, `manager-assignments`, activity and revocation |
| 22 | Maintenance | `maintenance`, `properties/{property_id}/preventive-schedules`, `preventive-schedules` |
| 23 | Professional services | `professional-appointments`, provider finding actions |
| 24 | Notifications and communication | Owner `properties/{property_id}/announcements`; shared `notifications.yaml`, CF Meet boundary (not an invented Owner chat API) |
| 25 | Reports and insights | `reports`, `reports/portfolio`, `reports/export`, `market-insights`, `audit/activity` |
| 26 | Fraud, privacy, disputes | `disputes`, `privacy/consents`, `privacy/requests`, `audit/activity` |
| 27 | Accessibility, assisted use, closure | `assistance`, `privacy`, `me/*`, `account/*` |
| 28 | Financing | `financing` — integration-dependent |
| 29 | Insurance | `insurance` — integration-dependent |
| 30 | Smart access | `access` — hardware/provider-dependent |

## B.3 Verified method+path examples that correct Google Stitch's false mapping

These are direct examples verified from the Owner router/route registry; the complete file/schemas remain the authority.

| Workflow | Actual Owner public operation |
|---|---|
| Owner dashboard | `GET /api/v1/owner/dashboard` |
| Owner properties | `GET /api/v1/owner/properties` |
| Owner property | `GET /api/v1/owner/properties/{property_id}` |
| Owner identity verification | `POST /api/v1/owner/verification` |
| Create draft listing | `POST /api/v1/owner/listings` |
| Publish listing | `POST /api/v1/owner/listings/{listing_id}/publish` |
| Inquiries / leads | `GET /api/v1/owner/inquiries` |
| Viewing list | `GET /api/v1/owner/viewings` |
| Tenant applications | `GET /api/v1/owner/applications` |
| Create an agreement | `POST /api/v1/owner/agreements` |
| Invite agreement signers | `POST /api/v1/owner/agreements/{agreement_id}/invite-signers` |
| Send for signature | `POST /api/v1/owner/agreements/{agreement_id}/send-for-signature` |
| Record an **offline** signature | `POST /api/v1/owner/agreements/{agreement_id}/signatures` |
| Wallet balance/state read | `GET /api/v1/owner/financial-wallet` |
| Short-let bookings | `GET /api/v1/owner/shortlet/bookings` |
| Maintenance requests | `GET /api/v1/owner/maintenance` |
| Approve maintenance quote | `POST /api/v1/owner/maintenance/{request_id}/approve` |
| Property sales | `GET /api/v1/owner/transactions/sales` |
| Manager assignments | `GET /api/v1/owner/manager-assignments` |
| Edit manager assignment | `PATCH /api/v1/owner/manager-assignments/{manager_assignment_id}` |
| Portfolio report | `GET /api/v1/owner/reports/portfolio` |

Incorrect Stitch guesses such as `POST /listings/draft`, `GET /wallet/balances`, `GET /leads`, `GET /portfolio`, `GET /maintenance/tickets`, and `POST /agreements/{id}/sign` **must not** be used. The gateway owns the public prefix; do not call internal or admin endpoints from apps.

# APPENDIX C. Integration execution checklist and Postman contract traceability

**During mock phases 0–5**, the agent should map the routes in a **read-only catalog** but *must not execute real requests or connect a service*. A mock flow is approved based on controlled fixture states and on-screen behavior. **Only after explicit owner approval** may Phase 6 use the Postman environment and API clients.

For every frontend action in Phase 6, create a traceability row containing: `module | web route | mobile route | mock scenario | Postman folder/request | OpenAPI file/operationId | HTTP method+path | request schema | success example | error statuses | role/step-up | idempotency/If-Match | external dependency | integration test | status`. Never let the UI invent a missing server operation.

**Contract verification order:**

1. Pin checked-out commit SHA for gateway, Owner service, OpenAPI and Postman.
2. Generate `OWNER_ALL_PUBLIC_ENDPOINTS.csv` from OpenAPI using the included script. Inspect all operations relevant to the current module.
3. Locate the corresponding item under `02 Individual Owner` in Postman. Compare method, path, description, sample request/response, variables and relevant saved errors.
4. Verify handler behavior and authorization, including actor/property scoping and `If-Match` concurrency requirements.
5. Build the typed Next.js and Dio/Flutter adapters only when the mock's state transitions are approved.
6. Integrate against a permitted non-production Gateway, never internal service URLs, and assert actual state readback after mutating commands where required.
7. Preserve provider-gated 503/error paths and never treat a successful local request as banking, government, e-sign, hardware or production provider certification.
8. Confirm the UI does not expose admin/internal routes, owner credentials, signed upload URLs, smart PINs, account data or private evidence.
9. Run frontend automated tests and available API contract / gateway journeys; record exact evidence and unresolved external gates.
10. Advance to staging/production only after separate release approval; no unrequested merge or deployment.

**Request reliability:** follow Postman `README.md` guidance on `X-Device-ID`, owner session scopes, generated `request_id`, idempotency keys, and `owner_retry_idempotency_key` for same-payload retries. Model `401`, `403 STEP_UP_REQUIRED`, `404`, `409/412`, `422`, `429`, `503`, timeouts and unknown financial outcomes. For media use the documented initiate → signed object upload → finalize → read-back workflow in the Media & Documents API; do not substitute direct uploads or retain public owner-document links.

## C.1 What the coding agent must produce with the final integration handoff

- Full `SCREEN_COVERAGE.csv`: 30 modules × web/mobile states × mock/real verified.
- `OWNER_ALL_PUBLIC_ENDPOINTS.csv` (generated from source OpenAPI, not manually curated).
- `API_MAPPING.md`: feature action ↔ exact OpenAPI method/path ↔ Postman folder/request ↔ response and failures.
- Auth/session, Wallet/Payment, Ownership/Consent, Media/Verification and signed provider-dependency tests.
- Provider-gated states preserved, with named dependency and owner action available.
- Exact branch/SHA, app build/test output, staging check results and unapplied blockers.

# APPENDIX D. One master mandate: source preservation → mock build → review → later integration

Use the **master execution directive in Section 15** for initial work. The following overrides any ambiguous reference to linking APIs during the mock: **the agent may inspect documentation, generate a catalog and implement repository interfaces, but may not call the backend until the full mock is tested and explicitly approved.** The full 30-module mock can take several bounded commits and verification checkpoints; it does not need a new user-written prompt for each screen. Continue unfinished in-scope work without repeating already-passed journeys.

At the beginning of execution, place these files in the repository without changing existing backend code:

```text
apps/individual-owner/docs/INDIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.md
apps/individual-owner/docs/generate_owner_endpoint_catalog.py
apps/individual-owner/Setup_Individual_Owner_Stitch_Reference.ps1
```

Run the setup script (for the gallery), run the catalog generator (for a **read-only API inventory**), then start the full mock implementation. Both files are local and do **not** connect to any running backend. Preserve the existing ZIP at `apps/individual-owner/stitch_individual_owner_design_system.zip`.

**Approval gate:** At the conclusion of the mock, produce a browsable Next.js preview and runnable Flutter build, 30-module coverage register, screen captures, mock scenarios, tests and known limitations. Stop there for review. API/Postman mapping becomes an executable integration mandate **only after approval**.
