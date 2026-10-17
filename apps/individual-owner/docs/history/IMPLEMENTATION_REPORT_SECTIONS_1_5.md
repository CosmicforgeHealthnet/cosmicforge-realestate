# Individual Owner · Sections 1–5 implementation report

17 October 2026 · Mock foundation delivery. The complete 30-module frontend is **not** accepted or complete.

## A. Git baseline and delivery

- Initial checkout: `development` at `0d8d14fe3a50cdee90a38ca6ca28f338ceed5d4a`, with pre-existing untracked design/doc work.
- Fetched origin; implementation base: `ee3b8bdfd15f9805a615f10948eac89481bee0b3` (`origin/development`). No reset, clean or merge was performed.
- Working branch: `feature/individual-owner-frontend-mock`.
- Source commits: `2efcf42` reference preservation; `ea2d5a8` tokens/fixtures; `931dcbf` Next.js; `d4ed727` Flutter. Follow-up validation/handoff changes are committed after these slices. The final documentation commit SHA and actual push result are reported in the delivery chat; `git rev-parse HEAD` reproduces the delivered revision.
- Only `apps/individual-owner/` is changed relative to the base. Backend, Gateway, OpenAPI and Postman files are unchanged. No protected branch was merged or deployed.
- Implementation files are committed. Pre-existing `README_FIRST.md`, the master-plan DOCX, `docs/generate_owner_endpoint_catalog.py`, and any active Word lock file are preserved untracked. Consequently the overall working tree can remain dirty with user-owned files; these are not implementation leftovers.

## B. Stitch preservation

| Item | Verified result |
|---|---|
| Original ZIP | `stitch_individual_owner_design_system.zip`, 11,323,929 bytes |
| ZIP SHA-256 | `ba0ee9fa12f3d276d2b2f29327dc7c39c985dd8a439ff77fdb1dcac660891337` |
| Original source files | 30 HTML, 30 PNG, two Markdown; all bytes checked against archive |
| Screen pairing | 15 desktop + 15 mobile, 15 stable pair identifiers |
| Gallery | [design/stitch/index.html](../design/stitch/index.html) |
| Inventory | [design/stitch/inventory.json](../design/stitch/inventory.json) |
| Integrity manifest | [design/stitch/SHA256SUMS.txt](../design/stitch/SHA256SUMS.txt), 63 entries relative to the owner directory |

The prior valid `source/` extraction was checked and moved to `original/`; no original HTML, PNG or Markdown was edited. The existing setup script was corrected to reject unsafe/duplicate archive paths, unexpected/missing catalogue entries, changed archives and altered original files. It preserves existing bytes and reports files restored from the archive. Isolated tests verified repeat inventory/manifest stability and refusal of tampered sources and traversal ZIPs. Local Git attributes preserve original bytes on Windows checkouts.

The gallery is responsive and keyboard accessible, displays all pairs, filters by platform and screen name, links to paired cards, original HTML and full PNGs, and exposes both original documents. Browser tests opened it via `file:` and decoded all 30 local previews. Archived HTML may load external design assets; screenshots are independent. The gallery is never used as either application's runtime.

## C. Web foundation

Stack: Next.js **16.4.2**, React **19.3.1**, TypeScript **6.0.3**, Tailwind **4.3.3**, TanStack Query, React Hook Form and Zod. App Router, exact direct dependency versions and a committed lockfile.

Implemented files and responsibilities:

- `web/src/app/`: layout/providers, dashboard route, product-area routes, directory, sample property route, loading/error/not-found/global-error boundaries.
- `web/src/components/`: responsive sidebar/header/bottom navigation shell, shared state widgets and synthetic property SVGs.
- `web/src/features/`: Query feature controller, dashboard cards/list/filter/checklist, and RHF/Zod workspace-label preference form.
- `web/src/design/`: generated semantic CSS tokens and light/dark theme controller.
- `web/src/lib/`: typed mock presentation models, repository interface, isolated deterministic mock repository and currency formatting.
- `web/tests/`, `web/e2e/`: repository/component tests and Chrome navigation/theme/mock/gallery checks, plus the compiled Flutter preview check.

Working behavior: desktop/sidebar and narrow five-tab navigation, searchable 30-area directory, read-only sample property overview, property-type filtering, task completion/reopening reflected in the dashboard, validated workspace-label edits, populated/empty/error scenarios and persistent web theme choice. Unsupported workflows display pending/unavailable/NOT_STARTED states. Changes to demo records reset on reload.

Commands from `web/`: `npm ci`, `npm run dev`; or `npm run build` then `npm run start`. The actual production preview was started at **http://127.0.0.1:3000** and left running for review.

Validation: clean npm 11 installation; TypeScript and ESLint pass; five unit/component tests pass; production build passes (34 generated pages plus dynamic sample-property route); three Chrome tests pass for desktop, phone and gallery. The application-flow browser check observed zero external requests and zero uncaught page errors. `npm audit --omit=dev`: zero vulnerabilities. Five development-tool audit findings remain documented in KNOWN_LIMITATIONS.md; no unsafe force downgrade was applied.

Visual evidence, captured from the actual production preview:

- [Desktop light](evidence/web-desktop-light.png) / [desktop dark](evidence/web-desktop-dark.png)
- [Phone light](evidence/web-phone-light.png) / [phone dark](evidence/web-phone-dark.png)

## D. Flutter foundation

Stack: Flutter **3.44.2**, Dart **3.12.2**, Riverpod **3.4.3**, GoRouter **18.1.0**, Dio **5.11.1** installed for later integration, Cupertino icons for native adaptive controls. Dio is not instantiated. Native Flutter widgets; no WebView or React Native.

Implemented `mobile/lib/app`, `core/theme`, `core/router`, `core/models`, `core/repositories`, `core/mock`, `core/widgets`, `features/dashboard`, `features/properties`, and `features/workspace`; Android and iOS runners; local fixture/font assets; six tests and light/dark image baselines. Native app bar, five root tabs, back navigation, scoped providers/controllers, shared card/loading/error widgets, sample dashboard/detail, task state, scenario switching and label validation are implemented. Theme follows system appearance with a session override.

Commands from `mobile/`: `flutter pub get`, `flutter run -d <device-id>`. Available preview: `flutter run -d chrome`. A static compiled preview is built by `flutter build web --no-web-resources-cdn` and can be served from `build/web`. It previews Flutter widgets and does not certify native-device behavior.

Actual results:

- Dependency resolution: pass.
- `flutter analyze`: no issues.
- `flutter test`: **6 pass**, including state, repository guards, navigation/back, themes, forms and two reviewed light/dark goldens.
- `flutter build web --no-web-resources-cdn`: pass; renderer, Inter, fallback Roboto, icons and fixtures load locally.
- Compiled Flutter Chrome check: **1 pass**, verifies rendered summary, theme switching and root navigation; zero external requests and uncaught page errors. The test exposed Flutter's default Google Fonts fallback; bundling Roboto with its Apache license resolved that real request.
- `flutter build apk --debug`: **pass**, including an incremental build after the final asset/launcher configuration. Artifact: `mobile/build/app/outputs/flutter-apk/app-debug.apk`, **158,254,305 bytes**, SHA-256 `ab8e2f3f29a5714cef1ea2c5fa84019c5f4f83e303a0d5c7bc427634a07a807e`. This ignored local debug artifact is not committed to Git.
- `flutter devices`: Windows and Chrome only. `flutter emulators`: no configured emulators. No Android/iOS device execution was performed. iOS build is unavailable on this Windows host without macOS/Xcode. Some Android SDK licenses are reported unaccepted, but this did not block the debug APK; no license acceptance was performed.

Actual compiled-widget evidence: [Flutter light](evidence/flutter-browser-light.png), [Flutter dark](evidence/flutter-browser-dark.png). Widget golden baselines: `mobile/test/goldens/dashboard-light.png` and `dashboard-dark.png`. These are not device screenshots.

## E. Architecture and safeguards

Both apps use **UI → feature state/controller → repository interface → mock repository**. Repository providers are scoped to the app; source fixtures are deterministic and clearly fictional. Presentation types are labeled as mock models, not claimed to be generated API DTOs. Later Gateway adapters can implement the interfaces while mapping actual schemas/guards.

`design/tokens/tokens.json` holds approved brand/semantic tokens. The generator maps these to CSS/Dart and checks shared fixture/font parity. Inter is locally bundled under SIL OFL; Roboto fallback under Apache 2.0. No app hotlinks fonts or property photography. Design decisions are recorded separately; originals remain untouched.

No backend/provider adapter, credential, usable access PIN, direct database connection, live payment, KYC submission, email, lock command or human message was added. No payout, government verification, legal-title or custody success is fabricated. Real provider controls remain unavailable. Runtime source-boundary checks and browser-request evidence support these statements. Package downloads, Git metadata, font acquisition and local build/test servers are development tooling, not calls to production services.

All authoritative Postman/OpenAPI locations were verified to exist and are recorded in API_MAPPING.md. Their inspection was read-only; integration remains NOT_STARTED.

## F. Remaining work and acceptance scope

Sections 1–5 foundation requirements are implemented and locally verified where tooling permits. The outstanding environment qualification is Android/iOS device testing and an iOS build; no full-device or production certification is claimed. Known audit/brand/localization/accessibility/release limitations are recorded explicitly.

The next unfinished blueprint section is **6 — Information architecture and reusable UI system**: extend the starter shell and component system into the full contextual owner experience. Then expand Section 7's mock scenarios/architecture and implement Section 8's detailed acceptance register, beginning with Phase 2 modules 01–12 and Section 9 journeys. SCREEN_COVERAGE.csv contains 60 web/mobile rows and keeps foundation acceptance separate from workflow status. Module 01 is IN_PROGRESS; remaining full module workflows are NOT_STARTED. None is INTEGRATED_PASS.

No merge, deployment, backend integration or full mock approval was performed. The full mock must be implemented, tested, reviewed and explicitly approved before real service integration.
