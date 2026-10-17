# Individual Owner · Sections 6–10 implementation report

17 October 2026 · engineering mock ready for Phase 5 review. All 30 modules have connected fictional workspaces on Next.js and native Flutter. Human acceptance is pending. No backend/provider integration, merge or deployment is authorized or performed.

## A. Delivery and Git

- Continuation base: `0eb0e7edeefd06d7ae19d6a52bbc35ce55066cd1`.
- Working branch: `feature/individual-owner-frontend-mock`.
- Verified implementation head before this evidence commit: `65f35e493af6f2980c9134cd83cb927af88c0f0b`.
- Checkpoints: `ba60e98` navigation/controls; `b12cbb2` coherent TS/Dart mock engines and core journeys; `cc38f68` complete web/native workspaces; `28cb817` native QA dropdown layout fix; `66928bd` draft/privacy/notification guards; `65f35e4` scope/calendar consistency and full rendered verification.
- The final delivery SHA is the commit containing this report and is recorded in the delivery message. Push verification uses equality of local HEAD and `origin/feature/individual-owner-frontend-mock`, plus `git ls-remote` after a normal push. No force push, reset, merge or deployment.
- Changed implementation: shared workflow definitions and linked seed; paired state engines/repository controllers; web dynamic record routes and shared RHF/Zod forms/details; native Flutter forms/details/calendars, GoRouter and shell; dashboards/search/scope/preferences; local metadata picker/export; scenario laboratory; test harnesses, safety checks and evidence registers.
- Preserved user-owned untracked files: root `README_FIRST.md`; `docs/INDIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.docx`; `docs/generate_owner_endpoint_catalog.py`; `docs/~$DIVIDUAL_OWNER_FRONTEND_MASTER_PLAN.docx`. Their contents are not staged or overwritten.
- No changed files under `services/`, `contracts/` or `postman/`. Original Stitch archive, HTML, PNG and design documents remain hash-identical.

## B. Section 6 · navigation, components, responsive/accessibility

Web: persistent grouped sidebar with a scroll path, tablet rail, five-item narrow navigation, directory, scoped property selector (including narrow layouts), global record/module search, account/notification entries, breadcrumbs, contextual linked records, dynamic detail/edit/new URLs and browser history/draft-discard handling.

Flutter: native AppBar and search sheet, account/property popup, Home/Properties/Listings/Wallet/More roots, detail deep links and root-tab restoration, native confirmations, safe areas and scrollable keyboard-safe forms. GoRouter exit guards protect drafts; direct detail links have a parent-list back fallback.

Shared presentation families: section headings/tabs, property cards/list/grid, responsive record lists, search/status filters/sort/pagination, badges, money and KPI displays, linked activity/history, validated forms and relation selectors, date/time controls, property/listing wizards, calendars, filename-only evidence selection/preview, example price breakdowns, permission capability switches, confirmations, status/error feedback and export controls. Modules consume these rather than duplicating screen implementations.

Design tokens preserve sapphire `#2459E0`, approved canvas/surface/text/semantic values, licensed local Inter/Roboto and shared JSON→CSS/Dart generation. No token source correction was required. Supporting screens intentionally extend the original design language.

Browser sweep: all 30 area entries at 1440, 1280, 1024, 768 and 390 CSS pixels; no horizontal document overflow. All 59 editable families have rendered required-title validation. Five representative screen families are captured in light/dark; foundation dashboard/phone and native previews have separate evidence.

Native sweep: all 30 areas in both themes at 360px / 160% text scale, all editable required-title forms, keyboard entry and draft cancellation. Long QA options were constrained after the sixteen-condition test found a 13px menu overflow. Large-text/reduced-motion preferences apply locally; OS text scaling is respected. Language choice records a preference while this interface remains English.

Accessibility checks include named inputs/navigation/dialogs, visible focus, browser search focus/Escape and native semantic controls/live feedback. The search dialog now explicitly focuses its input after `showModal`. This is engineering coverage, not production accessibility certification.

## C. Section 7 · architecture and state consistency

Both applications retain UI → feature controller/hook → typed OwnerRepository → MockOwnerRepository. No HTTP implementation is active. Presentation contracts remain distinct from future API DTOs.

`shared/workflows.json`: **60 conceptual model families**, **147 declared transitions**, **30 modules**, **16 QA scenarios**. `shared/workspace-seed.json`: **72 stable linked fictional records**, four properties (residential, short-let, commercial, land), six units and coherent listings/claims/verification/rent/booking/maintenance references. Demo clock: 2026-10-17 UTC. Reset/scenario changes restore deterministic seed state.

The TS and Dart engines validate fields/foreign references, dates, shares, purpose-specific consent, unit/property types, monotonic readings, version conflicts, availability/booking overlap, lease closure, financial unknown outcomes and manager capability/expiry/revocation. Commands are transactional: failed operations restore the whole state and revision. In-app alert preferences suppress subsequent automatic demo notifications when disabled. Successful commands create linked offers, agreements, tenancies, quote alternatives, turnover, transfer reviews, notifications and activity. Dashboard/report values derive from these records; cache/controller refresh propagates changes.

QA conditions: normal, empty, pending verification, correction required, restricted, permission denied, expired session, offline, provider/service unavailable, duplicate/conflict, invalid input, unknown financial outcome, insufficient delegation, stale availability, expired consent and failed operation. Both native and browser controls exercise all sixteen. Relevant mutations enforce the selected condition rather than merely changing a label. Offline is read-only; no command queue exists.

Core parity: 179 shared journey steps; supplementary coverage: 49 steps across utilities, professional services, evidence/assets/viewing slots, insurance/access and authority/quote conflicts. The independent engines and the rendered UI harnesses all consume the same specifications.

## D. Section 8 · module register

MOCK_PASS means the relevant local workflow and gates function; integration is NOT_STARTED for every module. External execution remains EXTERNAL_PENDING wherever a provider is needed. Financing intentionally exposes unavailable/read-only context, with no fictitious application or rate success.

| Module | Web | Flutter | Implemented local records/workflows | Execution boundary |
|---|---|---|---|---|
| 01 · Dashboard & portfolio | MOCK_PASS | MOCK_PASS | Scope, derived KPIs/drilldowns, tasks, alerts, calendar, activity, quick actions | Local fictional workflow |
| 02 · Sign-up, login, MFA & verification | MOCK_PASS | MOCK_PASS | Owner accounts; Sessions & devices; Identity evidence cases | Local fictional workflow |
| 03 · Payout account & tax profile | MOCK_PASS | MOCK_PASS | Masked payout preferences; Tax profile; Rent obligations & receipts; Deposit cases; Statements & reports | Local fictional workflow |
| 04 · Ownership & legal authority | MOCK_PASS | MOCK_PASS | Ownership claims; Co-owners & invitations; Owner consents; Ownership transfer requests | Local fictional workflow |
| 05 · Properties, units, land & assets | MOCK_PASS | MOCK_PASS | Properties; Units; Property assets; Property media & documents; Land parcels | Local fictional workflow |
| 06 · Property verification | MOCK_PASS | MOCK_PASS | Property verification cases | Local fictional workflow |
| 07 · Listings across property types | MOCK_PASS | MOCK_PASS | Property listings | Local fictional workflow |
| 08 · Pricing and fee disclosure | MOCK_PASS | MOCK_PASS | Pricing & fee models | Local fictional workflow |
| 09 · Availability & listing freshness | MOCK_PASS | MOCK_PASS | Availability blocks | Local fictional workflow |
| 10 · Inquiries, leads & campaigns | MOCK_PASS | MOCK_PASS | Inquiry center | Provider-dependent execution unavailable |
| 11 · Viewings & property access | MOCK_PASS | MOCK_PASS | Viewing slots; Viewings & slots | Local fictional workflow |
| 12 · Rental applications & screening | MOCK_PASS | MOCK_PASS | Rental applications; Rental offers | Local fictional workflow |
| 13 · Tenancy agreements & move-in | MOCK_PASS | MOCK_PASS | Agreements; Tenancies & handover | Provider-dependent execution unavailable |
| 14 · Rent, deposits & financial lifecycle | MOCK_PASS | MOCK_PASS | Rent obligations & receipts; Deposit cases | Provider-dependent execution unavailable |
| 15 · Renewals, notices & move-out | MOCK_PASS | MOCK_PASS | Renewal proposals; Notices; Move-out inspections | Provider-dependent execution unavailable |
| 16 · Utilities & service charges | MOCK_PASS | MOCK_PASS | Utility accounts; Meter readings; Utility bills; Service-charge budgets; Service-charge items | Provider-dependent execution unavailable |
| 17 · Commercial leasing & sales | MOCK_PASS | MOCK_PASS | Commercial letters of intent | Provider-dependent execution unavailable |
| 18 · Land rent, lease & sale | MOCK_PASS | MOCK_PASS | Land parcels; Land terms & inspections | Provider-dependent execution unavailable |
| 19 · Property sale, offers & closing | MOCK_PASS | MOCK_PASS | Sales & closing room; Document-room grants | Provider-dependent execution unavailable |
| 20 · Owner-operated short-lets | MOCK_PASS | MOCK_PASS | Short-let bookings; Short-let rates & settings; Turnover jobs; Damage claims; Availability blocks | Provider-dependent execution unavailable |
| 21 · Manager delegation & controls | MOCK_PASS | MOCK_PASS | Manager proposals; Manager assignments | Provider-dependent execution unavailable |
| 22 · Maintenance & preventive care | MOCK_PASS | MOCK_PASS | Maintenance requests; Quote comparison; Preventive schedules | Provider-dependent execution unavailable |
| 23 · Professional services & due diligence | MOCK_PASS | MOCK_PASS | Professional appointments | Provider-dependent execution unavailable |
| 24 · Notifications & communication | MOCK_PASS | MOCK_PASS | Notification inbox; Notification & account preferences | Provider-dependent execution unavailable |
| 25 · Statements, reports & insights | MOCK_PASS | MOCK_PASS | Statements & reports | Local fictional workflow |
| 26 · Fraud, disputes & privacy | MOCK_PASS | MOCK_PASS | Dispute cases; Privacy & data-rights requests | Local fictional workflow |
| 27 · Accessibility, assisted use & closure | MOCK_PASS | MOCK_PASS | Notification & account preferences; Assisted-use controls; Account closure requests; Privacy & data-rights requests | Local fictional workflow |
| 28 · Financing & payment plans | MOCK_PASS | MOCK_PASS | Financing availability | Provider-dependent execution unavailable |
| 29 · Insurance & protection | MOCK_PASS | MOCK_PASS | Existing insurance demo records | Provider-dependent execution unavailable |
| 30 · Smart access & remote controls | MOCK_PASS | MOCK_PASS | Fictional access devices; Temporary access simulations | Provider-dependent execution unavailable |

`SCREEN_COVERAGE.csv` retains exactly 60 rows (30 web, 30 mobile), with workflow scope and evidence per row. Property/detail/create and all supporting module screens are runtime components, not imported reference HTML.

## E. Section 9 · ten full journeys

| Core journey | Web UI | Native Flutter UI | Evidence |
|---|---|---|---|
| 01 · Owner onboarding | PASS | PASS | 12 shared steps; rendered inputs/actions and linked state assertions |
| 02 · Property to listing | PASS | PASS | 18 shared steps; rendered inputs/actions and linked state assertions |
| 03 · Prospect to tenancy | PASS | PASS | 41 shared steps; rendered inputs/actions and linked state assertions |
| 04 · Rent and deposit | PASS | PASS | 13 shared steps; rendered inputs/actions and linked state assertions |
| 05 · Tenancy lifecycle | PASS | PASS | 16 shared steps; rendered inputs/actions and linked state assertions |
| 06 · Short-let | PASS | PASS | 15 shared steps; rendered inputs/actions and linked state assertions |
| 07 · Maintenance | PASS | PASS | 12 shared steps; rendered inputs/actions and linked state assertions |
| 08 · Ownership and delegation | PASS | PASS | 19 shared steps; rendered inputs/actions and linked state assertions |
| 09 · Commercial, land and sale | PASS | PASS | 19 shared steps; rendered inputs/actions and linked state assertions |
| 10 · Insights and privacy | PASS | PASS | 14 shared steps; rendered inputs/actions and linked state assertions |

Web harness: real navigation via workspace search/tabs, real form fields/wizard steps, confirmations/security input and decisions; observes fictional snapshots through the opt-in QA inspection view. No direct state injection. It asserts revisions, links and expected statuses, and rejects page errors/external requests.

Native harness: real native forms, dropdowns, switches, decisions, confirmations and security dialogs; GoRouter deep-link navigation between steps; fixture repository injection only at app setup. It checks visible success/error feedback, statuses, linked effects and rollback. This is native widget testing, not emulator/device integration testing.

The engine suites separately assert exact derived totals and all core shared outcomes. Supplementary journeys verify standalone utility/bill/budget/charge flows; appointments/deliverables and viewing slots; insurance reminder/device/document/access metadata; and rejected consent/competing-quote decisions. All are fictional.

## F. Section 10 · safety corrections

- Financial buckets are examples derived from rent classifications; receipts are explicitly not real payment proof. Unknown outcomes prohibit optimistic balance changes. No payout/escrow release/settlement success claims.
- Evidence upload is metadata selection; claim submission, review and publication are separate. No government/BVN/NIN/biometric or legal-title certification.
- Listing preview, separate review, pricing, availability and current co-owner consent guard demo publication. Closing/diligence, separate transfer consent and financial settlement are distinct; settlement remains unconfirmed/unavailable.
- Denied/expired sessions hide protected details and remove property-name suggestions from portfolio menus; identity evidence remains guarded while simulated login stays reachable.
- Every accepted co-owner needs current purpose-specific consent for publication/transfer. Representatives have zero title share. Manager write capabilities are limited to property listing/viewing/maintenance and expire/revoke immediately; owner title and payout powers are never delegated.
- Viewing consent, conflicting bookings/blocks, single approved maintenance quote, grant expiry and move-out inspection/keys/deposit resolution are enforced in both engines.
- Human screening remains human review notes/access grants. No invented bank feeds, court/background results or automated discriminatory applicant ranking.
- CF Meet, actual notifications/signing/provider payments, financing/insurance issuance and hardware are disabled with clear unavailable explanations. No door codes or insecure sharing URLs are generated. No actual email/SMS/push/video occurs.
- Runtime source scan passes: no HTTP/provider adapters or remote data URLs, database DSNs, credential patterns or access-PIN assignments. Browser journeys observe local requests only. Flutter's default CanvasKit CDN dependency was found by the compiled-browser test and replaced with the bundled local `canvaskit/` bootstrap.
- Originals are preserved, including their unsafe assertions. Changed runtime copy removes connected Gateway, certified title/tax/custody, settlement and live-device claims. Remaining production/legal/security acceptance limitations are explicit in KNOWN_LIMITATIONS.md.

## G. Testing and visual evidence

Environment: Windows PowerShell, Node/npm, Chrome via Playwright 1.64.1; Next.js 16.4.2/React 19.3.1; Flutter 3.44.2/Dart 3.12.2. No configured emulator/connected physical phone; no macOS/Xcode/iOS build. Android evidence is a debug build, not installation/runtime or store signing.

Commands run from their application directory unless stated:

| Command | Result |
|---|---|
| `web: npm test` | 24 passed (3 files): repository/components and 14 shared story engine tests plus guards |
| `web: npm run lint` | PASS, zero warnings/errors |
| `web: npm run typecheck` | PASS |
| `web: npm run build` | PASS, static module entries and dynamic record routes |
| `web: npm run test:e2e` | 3 passed: desktop/phone controls and original offline gallery |
| `web: npm run test:workflows` | 18 passed: 14 rendered journeys, all-area/required-form/five-width sweep, sixteen scenarios/keyboard/draft/export, guarded visual/unknown-financial and portfolio scope/reset/archive checks |
| `web: npx playwright test --config playwright.flutter.config.ts` | 1 passed: compiled native Flutter web, local assets, light/dark and navigation; zero external requests |
| `mobile: flutter pub get` | PASS; resolved lockfile includes local picker support |
| `mobile: flutter analyze` | PASS, no issues |
| `mobile: flutter test` | 50 passed: 17 engine + 2 repository + 4 foundation + 14 UI stories + 3 broad coverage/scenario/scope + 10 additional visual tests |
| `mobile: flutter build web --release` | PASS; build/web. The default release build also passed its Wasm dry run |
| `mobile: flutter build apk --debug` | PASS; build/app/outputs/flutter-apk/app-debug.apk |
| `node scripts/generate-tokens.mjs --check` | PASS, token/fixture parity |
| `node scripts/verify-references.mjs` | PASS, ZIP + 62 original hashes, 30 HTML/PNG links, 15 exact pairs |
| `node scripts/verify-boundaries.mjs` | PASS, runtime source boundary checks |
| `git diff --check` | PASS |
| `web: npm audit --omit=dev --json` | Zero runtime vulnerabilities |
| `web: npm audit --json` | Five high-severity development-tool findings; retained as an explicit limitation |

Reviewed visual baselines were generated once using `flutter test test/visual_workspaces_test.dart --update-goldens`, then checked by normal `flutter test`; dashboard baselines were deliberately updated for the added four-property/QA/navigation UI.

Actual implementation screenshots (not reference images): `docs/evidence/web-desktop-{light,dark}.png`, `web-phone-{light,dark}.png`, `flutter-browser-{light,dark}.png`; `docs/screenshots/interactive/` includes desktop listings/finance/calendar, property wizard and narrow maintenance in both themes, native form/list/finance/calendar pairs, native denied/restricted views and web denied/restricted/unknown-financial views.

Visual comparison inspected actual original PNG and HTML (including treasury/finance and listing wizard/mobile, plus the preserved dashboard foundation references). Sapphire, Inter, light/dark surfaces, rounded cards, grouped navigation and semantic statuses carry through. Intentional deviations: responsive lists and native pickers replace wide reference tables/custom controls; unsafe authoritative/certified/legal/escrow assertions are removed; forms are three-step property/listing wizards with separate pricing/review/consent records. Native calendar captures are scrolled to the calendar. This is representative comparison, not a claim of pixel identity or exhaustive state screenshot coverage.

Defects found and fixed during validation: unreachable sidebar QA footer; final wizard step submitting early; security input accessible naming; streamed dashboard hydration race; dialog focus; narrow native dropdown overflow; obsolete foundation assertions; default Flutter CanvasKit CDN loading; stale dashboard/workspace refresh; scope reset/archive and owner-level record filtering; pagination after narrowing scope; native calendar page-storage collision; and range-calendar checkout exclusion. Booking ranges include occupied nights and exclude the checkout date. No outstanding failures remain in the listed final checks.

## H. Local previews and artifacts

- Next.js production preview: `http://127.0.0.1:3100` (started for review after production build). Directory: `/more`; opt-in QA: `/demo-lab`. Original user processes/ports were not terminated.
- Native Flutter: `cd apps/individual-owner/mobile` then `flutter run -d <device-id>`; for browser-only inspection use `flutter run -d chrome`. Built web can be served with `python -m http.server 4173 --bind 127.0.0.1 --directory build/web`.
- Android artifact: `apps/individual-owner/mobile/build/app/outputs/flutter-apk/app-debug.apk` (generated locally, not committed).
- Begin with Normal/reset, select a property, open a record or New action, then follow contextual related records. Financial/auth/identity/security prompts use literal `DEMO`; never enter real credentials, documents or customer information.
- Evidence source: `shared/journeys.json`, `shared/supplementary-journeys.json`, web `e2e/workflows/`, native `test/workspace_ui_journeys_test.dart`, state tests, coverage and golden files.

## I. Closure

Sections 6–10 are implemented for engineering mock review. The 30×2 register is MOCK_PASS and ten core/four supplementary local journeys are verified on both renderers. Human Phase 5 acceptance is still NOT_STARTED. API integration remains NOT_STARTED; provider execution and physical-device acceptance remain EXTERNAL_PENDING. Real authentication, durable customer data, localization, live signing/payments/verification/notifications/hardware, production certification, merge and deployment are outside this delivery. See KNOWN_LIMITATIONS.md for precise practical limits.
