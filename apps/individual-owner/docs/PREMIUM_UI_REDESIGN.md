# Individual Owner — Sapphire Atelier premium UI redesign

**Status:** FIRST IMPLEMENTATION PASS — VISUAL QA AND LOCAL BUILD REVALIDATION REQUIRED  
**Source branch:** `feature/individual-owner-premium-redesign`  
**Based on:** `feature/individual-owner-frontend-mock` @ `0ba25aaa88bf4053c6400789f28751762302c09d`  
**Scope:** Presentation only; mock state and 30 module definitions preserved.

## Direction

Make the Owner experience look and feel like a premium, contemporary real-estate operating suite. Preserve CosmicForge sapphire, Inter, the approved light/dark palette and reliable task navigation. Reject purely decorative complexity, invented data and false provider execution.

Inspiration was reviewed for **hierarchy and design patterns**, not copied assets or screens:

- [Premium real estate property management dashboard](https://dribbble.com/shots/27687118-Real-Estate-Dashboard-Modern-Property-Management-SaaS-UI)
- [Emura real estate platform](https://www.behance.net/gallery/238790693/Emura-Real-Estate-Management-Platform-UIUX)
- [Real estate dashboard](https://in.pinterest.com/pin/modern-real-estate-admin-dashboard-ui-design--920282505109692761/)
- [Proplistic product case study](https://skycrestcreative.com/case-studies/proplistic)

Design choices: signature midnight-blue sidebar, sapphire focus/action hierarchy, translucent topbar, editorial section headings, elevated data cards, understated motion, refined transaction buckets, visually organized record lists, custom geometric architectural artwork, semantic state chips and genuinely native Flutter surfaces.

## Implemented on the redesign branch

- **All 30 web module screens, via their shared styles:** upgraded page headings, grouped navigation, record cards, property grids, financial metrics, list filters, tabs, forms, dialogs, timelines, calendars and responsive bottom navigation.
- **Web dashboard:** new portfolio/task action hierarchy; corrected outdated “Foundation preview” copy.
- **Property artwork:** locally rendered original SVG illustrations for three fictional property variants, replacing the initial basic diagrams. No remote images.
- **Flutter UI:** improved Material 3 typography, cards, button and input styles, navigation bar, sheets, chips, and dialogs; premium four-group mobile module directory; editorial module headings and semantic status chips.
- **Architecture unchanged:** Next.js + React web, Flutter native mobile, shared approved brand tokens and strictly local mock repositories. Original Stitch files preserved.

## Review and acceptance gate — not yet closed

The improvements above are a cross-application **first pass**, not individual sign-off of every page. The shared workspace pattern affects 30 modules but module-specific art direction, contextual panels and microinteractions still require product-owner review.

The following checks must be run on the actual Windows checkout of this branch before any merge:

```powershell
cd C:\Projects\cosmicforge-realestate
git fetch origin
git switch feature/individual-owner-premium-redesign
git pull --ff-only origin feature/individual-owner-premium-redesign

cd apps\individual-owner\web
npm ci
npm run lint
npm run typecheck
npm test
npm run build
npm run test:workflows

cd ..\mobile
flutter pub get
flutter analyze
flutter test
flutter build apk --debug
```

**Expected change to golden tests:** Flutter's existing pixel-exact golden baselines reflect the previous design and may fail after the intentional Material theme change. Compare actual renders to the reference; only update goldens after visual approval. Never blanket-update golden files just to hide defects.

**Visual QA required:** dashboard, property list/detail/new, listings wizard, finance/wallet, tenancy, agreement, short-let calendar, applications, maintenance, ownership/delegation, commercial/land/sales, verification, reports, identity/security, notifications, settings, and provider-gated modules, on desktop/tablet/narrow web and Flutter phones. Check all 30 modules in both themes, including form error, restricted, empty and nontrivial data states.

**Acceptance:** no regression in the ten owner journeys; no clipping or overflow at narrow widths/large text; readable dark-mode contrast; graceful keyboard and gesture navigation; no invented financial or legal claims; no network/provider requests; human screenshots reviewed; no merge until explicit approval.

## Execution status

- GitHub source changes pushed: **YES** (confirm actual SHA with `git rev-parse HEAD`).
- Local build/test on redesigned source: **NOT YET EXECUTED IN THIS SESSION**.
- Physical Android and iOS tests: **PENDING**.
- Mock functionality: previously tested on base revision; **needs regression revalidation on redesign**.
- Full 30-screen bespoke visual acceptance: **PENDING**.
- Backend/API integration: **NOT STARTED**.
- Merge/deployment: **NOT AUTHORIZED**.

Follow-on work: individually refine the dashboard, property and listing journeys first, then continue across each module family, preserving each approved visual pattern and functionality. Capture actual screenshots from the redesigned runtime; do not substitute Stitch reference PNGs.
