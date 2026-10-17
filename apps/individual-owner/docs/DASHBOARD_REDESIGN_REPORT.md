# Individual Owner — Premium dashboard command center

**Status:** IMPLEMENTED ON DESIGN REVIEW BRANCH — LOCAL BUILD, GOLDEN AND VISUAL ACCEPTANCE PENDING  
**Branch:** `feature/individual-owner-premium-redesign`  
**Base:** `0ba25aaa88bf4053c6400789f28751762302c09d`  
**Scope:** Web and Flutter dashboard presentation and navigation; strictly mock only.

## Delivered features

### Web dashboard

- Premium midnight-blue personalized welcome area with primary action to manage the portfolio and secondary demo property creation; developer QA stays accessible.
- Data-derived executive overview: sample property count, sample listings, fictional unit occupancy percentage and locally tracked owner tasks. No fabricated revenue trend.
- Property command center with eight pinned owner destinations, grouped expandable directory containing **all 30** actual product module routes and link to the full directory.
- Dedicated owner priority checklist retaining existing mock repository mutations and accessible action labels.
- Visually improved featured-property cards using original locally rendered artwork; property-type filters and real existing detail routes.
- Finance panel showing the existing fictional balance classifications and links to wallet and rent/deposits; no real payments or payouts.
- Unified upcoming viewings/bookings/maintenance/alerts, actionable activity list, owner calendar and calculated mock occupancy insight.
- Responsive CSS for desktop, tablet, narrow web, dark/light appearance and reduced-motion. No external images, new backend APIs or framework replacement.

### Native Flutter dashboard

- Executive sapphire welcome panel and clear property actions.
- Eight native owner shortcuts with real GoRouter destinations, plus access to the 30-area directory.
- Upgraded contextual property cards and clearer compact financial classification tiles.
- Inherits enhanced Material 3 theme, semantic state chips and grouped module directory from the initial premium redesign.
- Retains existing Riverpod/mock repository, owner task interactions, scenario switcher, dashboard workbook/calendar and safe provider gates.

## Architecture and correctness

- Application data model and mock state engines unchanged.
- Existing properties, balances and task records remain fictional; dashboard links route to the actual 30-module mock workspaces.
- Denied/expired web states do not render protected property or finance cards.
- State is not promoted to actual financial, government, legal, verification or provider success.
- The original archived Stitch library is untouched.
- No service, contracts, Postman or backend infrastructure files changed.
- No merge, live provider integration or deployment.

## Checks required on Windows before approval

```powershell
cd C:\Projects\cosmicforge-realestate
git status --short
git fetch origin
git switch feature/individual-owner-premium-redesign
git pull --ff-only origin feature/individual-owner-premium-redesign
cd apps\individual-owner\web
npm ci
npm run typecheck
npm run lint
npm test
npm run build
npm run test:e2e
npm run test:workflows

cd ..\mobile
flutter pub get
flutter analyze
flutter test
flutter build apk --debug
```

Review actual dashboard screenshots on web at 1440, 1280, 1024, 768, 390 and 360px, with light and dark themes, ordinary and empty scenarios, offline/permission-denied conditions, large text and focus/tab navigation. Verify actual link destinations and all 30 navigation routes. Review native Flutter widget goldens before updating expected baselines, since changing the theme and dashboard layout intentionally changes pixels.

**Validation truth:** GitHub source commits and source-level checks can be verified; the Windows Next.js/Flutter builds and screenshots have **not** been executed in this chat. The branch must not be merged or described as Phase 5 accepted until the actual validation commands and product-owner review pass.

## Preview

Use a free local port instead of interrupting existing processes:

```powershell
cd C:\Projects\cosmicforge-realestate\apps\individual-owner\web
npm run dev -- --port 3200
```

Open `http://127.0.0.1:3200/`. Flutter: `flutter run -d chrome` from `apps/individual-owner/mobile/`. These are local previews, not public deployments.
