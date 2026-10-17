# DESIGN.md — CosmicForge Real Estate / Individual Property Owner

> Version 1.0 · 16 October 2026 · Design proposal for **responsive web and Android/iOS mobile** · Based on the verified owner-domain API and V5 product scope. Color/mark system is a **provisional design direction**, not a verified existing corporate brand. Review with brand owner before public launch.

## 1. Mission and user promise

Design a coherent, premium but practical workspace where an individual can **prove property ownership, create/manage legitimate listings, handle enquiries, tenants, guests and sales, and oversee protected finances** across a personally owned portfolio — without surrendering authority to a manager. The experience must work equally for an owner with one apartment and a diaspora owner with a larger portfolio.

**Non-negotiables:** the owner remains the principal; verification and publication are separate; financial balances must distinguish **pending, held, available, restricted**, and settled; sensitive actions require step-up; delegated roles cannot redirect owner payouts or override ownership; all prices and fees are disclosed before acceptance. No invented paid/approved/verified/connected state. Route all client requests via the **public API Gateway** (`/api/v1/owner/*`) and other public service contracts; never call internal service endpoints from client apps.

## 2. Brand direction and atmosphere

- Personality: trustworthy, modern, clear, confident, owner-controlled, calm in high-stakes actions.
- Visual language: architectural geometry, generous white space, decisive sapphire accent, deep ink text, pale blue/teal supportive surfaces, restrained elevation, readable transaction records.
- Primary light canvas `#F6F8FC`, card `#FFFFFF`, ink `#17263C`, brand `#2459E0`, brand hover `#1743B8`, teal positive `#0B7466`. Dark canvas `#0C1525`, elevated card `#17253A`, text `#ECF3FF`.
- Avoid gradients on essential information, flashy animations, random decorative glassmorphism or unverified property photos.
- Brand mark in `assets/` is an **illustrative placeholder** only. Replace with approved CosmicForge Real Estate SVG/logo and lockups before publication.
- Typography: **Inter** preferred for UI (400, 500, 600, 700); fallback system UI. No proprietary fonts included.
- Editorial illustration: simple layered architectural forms, no faces or licensed imagery. Real listing images only from consented owner uploads with authorized media URLs; sample cards clearly indicated as demo.

## 3. Design tokens

Source of truth `tokens.json` → `code/tokens.css` and `code/mobile-theme.ts`. Use semantic tokens; never hard-code raw palette inside screens.

| Token | Light | Dark | Meaning |
|---|---|---|---|
| surface.canvas | #F6F8FC | #0C1525 | app background |
| surface.card | #FFFFFF | #17253A | panels, sheets |
| surface.subtle | #EDF2FA | #1E3048 | chips, nested content |
| text.primary | #17263C | #ECF3FF | primary content |
| text.secondary | #536479 | #ADC0D9 | secondary content |
| border.default | #DCE4F0 | #344966 | dividers |
| action.primary | #2459E0 | #6C96FF | primary CTAs |
| action.onPrimary | #FFFFFF | #0C1525 | readable label |
| status.success | #087A66 | #58D4BC | verified / genuinely completed |
| status.warning | #9A6507 | #F2BD69 | pending user action |
| status.danger | #C93647 | #FF8694 | errors/at risk |

- Space scale: 4, 8, 12, 16, 20, 24, 32, 40, 48, 64 px.
- Corners: 10 px controls, 16 px cards, 22 px modals, 999 px status chips.
- Shadows: subtle `0 8px 28px rgba(18,38,70,.07)` light; prefer stroke and layered surfaces in dark.
- Text sizes: 12 meta, 14 secondary, 16 base, 18 subtitle, 22 section, 28–32 page title; 40–48 hero only.
- Minimum targets: mobile 44 × 44 CSS px; text contrast WCAG AA; focus outline at least 2 px.

## 4. Layout and navigation

**Web desktop (≥ 1200 px):** fixed/collapsible 248 px left navigation, 72 px top bar, flexible primary content, optional contextual right rail for action detail. Content max-width 1440 px. Four KPI cards in a row at large widths. List detail drawers and multistep forms use a guided full page for legal/financial actions.

**Web tablet (768–1199 px):** 76 px icon rail or drawer; two KPIs per row; details navigate or open full-height side panel.

**Web/mobile narrow (< 768 px):** bottom tabs for Home, Properties, Activity, Wallet, More; secondary hierarchy via contextual top bar; no tiny 3-column tables or hover-only controls. On mobile native apps, use stack navigation and accessible bottom sheets, one primary CTA per viewport. Offline read/cache permitted for recently viewed non-sensitive data only; never queue payout, booking, sale, agreement or sensitive confirmations offline.

**Primary IA:** Home; Properties; Listings; Leads & Viewings; Tenancies & Rent; Sales & Land; Short-Lets; Maintenance; Finance & Wallet; Reports; Managers & Access; Notifications; Account, Verification & Security. Group advanced/occasional functions into contextual property details rather than a 30-item flat nav.

## 5. Core components and states

- App shell: brand mark, account/role state, property switcher, searchable navigation, notification center, profile/security menu.
- Dashboard metric cards: label, number + unit/currency, trend only if supported by authoritative reporting, context link.
- Property card: verified owner-uploaded hero media or branded placeholder; type, location, property verification, active listing count, actionable status.
- Listing card: transaction type, visible price and frequency, readiness, publish/pause controls, freshness and source-of-truth caveats.
- Finance cards: balance bucket **label + explanation + timestamp**; no "available to withdraw" until Core says it is; show restricted money separately.
- Standard controls: input, select, segmented status filters, date picker, data table, upload progress, evidence preview, password/TOTP, step-up modal, approval modal, OTP and protected disclosure review.
- Activity states on *every* network screen: skeleton/loading, real empty, unauthorized, pending verification, 403 insufficient authority, 409 conflict, 412 version conflict, 422 validation/policy, 429 cool-down, 503 provider unavailable, connectivity loss, recovery/readback pending, success confirmed by backend.
- Distinguish **draft, ready, under review, published, paused, withdrawn, expired, blocked**; do not flatten lifecycle states into a green checkmark.
- Safe destructive actions: require clear scope, consequences, explicit confirmation, reason when needed and a transaction receipt/reference.
- Dangerous/restricted features: disabled with explanatory text and an alternative permitted action, not a clickable fake flow.

## 6. End-to-end owner journeys

**J1. New owner → verified first listing.** Register → contact verification → profile → identity/ownership evidence upload → property create + verify → units/media → draft listing → pricing/fees → availability → backend preview checklist → publish only if the real backend permits → state confirmation. Identity verification does not imply property verification; property verification does not itself publish listing.

**J2. Residential rent.** Inquiry → viewing slot/consent → application/decision → offer → approved legal agreement/evidence → tenancy → obligation/collection via Financial Core → receipt/readback → confirmed payment / deposit states. No money milestone is inferred from "collection initiated".

**J3. Commercial or land sale.** Property type → listing → lead → offer/LOI/due diligence → agreement and closing milestones → protected evidence and financial state → guarded transfer. Signatures, policy and releases must respect actual certified integration readiness.

**J4. Short-let.** Listing calendar → availability/price → booking hold → accepted payment context → booking confirmation only on canonical source truth → check-in/out, inspection, damages, extension, cancellation. Treat overlapping dates and unknown payments as recoverable exceptions.

**J5. Delegation.** Property → manager proposal → explicit capabilities/limits/duration → manager acceptance/consent where required → activity log → suspend/revoke. Never offer manager ownership transfer or owner payout-edit access.

## 7. Web and mobile parity

**Web focus:** large-portfolio oversight, dense filtering/reporting, multi-item workflows, side-by-side documents, financial histories, legal action review and exports.

**Mobile focus:** quick status checks, push alerts, document scan/upload, inquiry response, viewing attendance, maintenance evidence, property share/preview, booking operations and quick approval with step-up. Complex agreement or multi-property reports may open mobile-friendly full-screen flows; no capability is "missing" simply because it is nested.

**Platform behavior:** Android back and iOS swipe respect pending form drafts; safe-area and keyboard handling; screen reader labeling on status values; support low-bandwidth image loading; avoid location permission until a specific mapped feature actually needs it.

## 8. Complete 30-area screen inventory

The backend product traceability uses §13.1–§13.30. Each area must have at least an overview/list, detail, relevant create/edit actions, real status/blocked state and empty/error states. The more granular route/view breakdown lives in `docs/SCREEN_INVENTORY.csv`.

| # | Product area | Route group | Gate | Main experience |
|---:|---|---|:---:|---|
| 01 | Dashboard & portfolio | `dashboard` | A | Web management + mobile task flow |
| 02 | Sign-up, login, MFA & verification | `identity` | A | Web management + mobile task flow |
| 03 | Payout account & tax profile | `financial` | A | Web management + mobile task flow |
| 04 | Ownership & legal authority | `ownership` | A | Web management + mobile task flow |
| 05 | Properties, units, land & assets | `properties` | A | Web management + mobile task flow |
| 06 | Property verification | `verification` | A | Web management + mobile task flow |
| 07 | Listings across property types | `listings` | A | Web management + mobile task flow |
| 08 | Pricing and fee disclosure | `pricing` | A | Web management + mobile task flow |
| 09 | Availability & listing freshness | `availability` | A | Web management + mobile task flow |
| 10 | Inquiries, leads & campaigns | `inquiries` | B | Web management + mobile task flow |
| 11 | Viewings & property access | `viewings` | A | Web management + mobile task flow |
| 12 | Rental applications & screening | `applications` | A | Web management + mobile task flow |
| 13 | Tenancy agreements & move-in | `tenancies` | B | Web management + mobile task flow |
| 14 | Rent, deposits & financial lifecycle | `rent` | B | Web management + mobile task flow |
| 15 | Renewals, notices & move-out | `renewals` | B | Web management + mobile task flow |
| 16 | Utilities & service charges | `utilities` | B | Web management + mobile task flow |
| 17 | Commercial leasing & sales | `commercial` | B | Web management + mobile task flow |
| 18 | Land rent, lease & sale | `land` | B | Web management + mobile task flow |
| 19 | Property sale, offers & closing | `sales` | B | Web management + mobile task flow |
| 20 | Owner-operated short-lets | `shortlets` | B | Web management + mobile task flow |
| 21 | Manager delegation & controls | `managers` | B | Web management + mobile task flow |
| 22 | Maintenance & preventive care | `maintenance` | B | Web management + mobile task flow |
| 23 | Professional services & due diligence | `professionals` | B | Web management + mobile task flow |
| 24 | Notifications & communication | `notifications` | B | Web management + mobile task flow |
| 25 | Statements, reports & insights | `reports` | A | Web management + mobile task flow |
| 26 | Fraud, disputes & privacy | `safety` | A | Web management + mobile task flow |
| 27 | Accessibility, assisted use & closure | `settings` | A | Web management + mobile task flow |
| 28 | Financing & payment plans | `financing` | C | Gated provider overview; no unverified live action |
| 29 | Insurance & protection | `insurance` | C | Gated provider overview; no unverified live action |
| 30 | Smart access & remote controls | `access` | C | Gated provider overview; no unverified live action |

**Gate interpretation:** A = Owner-domain functionality locally covered (still requires deployed validation); B = domain workflow locally covered but external/legal/provider step may be restricted; C = partner/device-driven capability gated, never fake activation. All gates are subject to deployment and independent external approvals. Marketing language cannot override an API gate.

## 9. Page-level details

### 9.1 Dashboard

Greeting + current portfolio scope, health/security banners, four KPI cards (Properties, Active Listings, Outstanding Rent, Open Tasks) with actual data sources; property grid; latest inquiries; upcoming viewings/renewals; transaction status summary; maintenance alerts; owner calendar. Empty new-account mode: a guided checklist with one call to create a property. Charts must never invent trends. Data fetched from the owner dashboard/calendar, supplemented with authenticated authoritative financial reads.

### 9.2 Property detail

Header image, ownership & verification badges (distinct), property location and type, shared ownership visibility, tabs: Overview, Units/Assets, Documents, Listings, Occupancy, Maintenance, Financials, Activity. High-stakes actions require policy/readiness gates. Avoid showing an individual's confidential ownership documents in public preview.

### 9.3 Create listing wizard

Steps: choose verified property/unit → transaction type → location/basic characteristics → controlled images/media → pricing and fee disclosure → availability → preview checklist → publish. Sticky progress, save-draft, field-level validation, version conflict resolution. The backend preview checklist is authority; front-end checks are only UX assistance.

### 9.4 Lead center

Kanban optional on wide screens; mobile status list; filters by listing, property, priority and pipeline state. Avoid discriminatory customizable screening questions. Show viewing time, access/occupant consent and next-action deadline. Human chat/video buttons only where CF Meet integration is truly ready.

### 9.5 Finance and Wallet

Overview with `Pending`, `Held/Protected`, `Available` and `Restricted` **only if those precise values exist in Core response**; no client-side aggregation of contradictory sources. Distinguish `collection requested`, `funding observed`, `allocated`, `eligible for release`, `payout processing`, `payout confirmed`, `disputed` and `reversed`. Step-up for sensitive changes, immutable receipt reference, no optimistic settled/withdrawn state.

### 9.6 Tenant/short-let/sale detail

Use guided status timeline with actions derived from backend allowed-state and role permissions. Keep attachments with authorization-expiring links; show legal and payment status separately.

### 9.7 Owner controls

Sessions, trusted devices, email/phone verification, MFA setup, step-up, property delegation capabilities, evidence access history, privacy request and account closure holds. Use no fabricated "approved" KYC/finance state.

## 10. Interaction and motion

Use 140–220 ms functional transitions; respect `prefers-reduced-motion`; do not animate money counters before confirmation. Toasts report *accepted* versus *completed* honestly. Save visible drafts where permitted; capture idempotency keys stable across retries; after ambiguous outcomes show "Checking latest status" and refresh original resource instead of issuing an unrelated duplicate command.

## 11. Accessibility and localization

WCAG 2.2 AA ambition: keyboard navigation, visible focus, landmarks, readable error summaries, numeric tabular alignment, color-independent statuses, meaningful labels and correct timezones. Nigerian currency uses `NGN`/₦ and minor-unit conversion only at a typed formatting boundary; other currencies only when allowed. Date display shows owner-selected timezone and the event property's timezone if relevant. Never assume every owner lives in Nigeria.

## 12. Asset, icon and media rules

Only use approved logo in final product. Until supplied, `assets/brand-mark-placeholder.svg` is for layout sizing and icon token previews. Default iconography: one consistent 24 px stroke set (Lucide licensing review); no mixed emoji iconography. Property thumbnails are synthetic vector examples for design only. Production listing images must be uploaded with authorization via media-document service workflow; never hotlink scraped property photography.

## 13. Source and implementer rules

- Verified references: `docs/product/individual-owner/SECTION_24_FINAL_ACCEPTANCE_AND_APPENDIX_RECONCILIATION.md`, the published OpenAPI pair and `apps/individual-owner/README.md` in development branch.
- Use **OpenAPI-generated types and endpoint definitions**, not invented DTO fields. Examples in this package are illustrative and require generation/verification against the checked-out exact commit.
- Keep `owner` / property-manager / Business Operations / marketplace actor boundaries separate.
- Build native web + mobile view components from one token system, not copy-and-paste page pixels.
- UI must never call `/internal/*`, bypass the Gateway, directly update Core ledger, infer protected release from received funds, or suggest a provider works when it returns 503.
- Add review gates for real logo assets, jurisdiction-specific legal copy, staging URL, auth/session storage strategy, OpenAPI version pinning, external provider rollout decisions and deployed QA.

## 14. Acceptance standard

A feature is *frontend complete* only when it has responsive web and native mobile interaction designs, accessible states, exact contract-correct request/response handling, denied/pending/unavailable/unknown-outcome behavior, tested owners-only authorization, empty/loading/offline designs, and QA against deployed non-production backend. Mock-only dashboards and screenshots are **not** frontend completion.
