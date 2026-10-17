# Sections 1–5 mock foundation acceptance

These checks accept the foundation only. No row grants full module workflow acceptance, production readiness, provider certification or approval to integrate.

| Check | Result | Reproduction / evidence |
|---|---|---|
| Source bytes and 15 pairs | MOCK_PASS | `node scripts/verify-references.mjs`: ZIP plus 62 extracted hashes; all HTML/PNG gallery links exist |
| Setup safety/idempotence | MOCK_PASS | `powershell -File scripts/test-reference-setup.ps1`: isolated extraction, repeat manifest/inventory stability, altered-source refusal and traversal refusal |
| Gallery behavior | MOCK_PASS | `web/e2e/foundation.spec.ts`: local file URL, 30 previews, 15 mobile filtered previews, dashboard search, pair navigation, all PNG decodes |
| Token/fixture/font parity | MOCK_PASS | `node scripts/generate-tokens.mjs --check` |
| Runtime mock boundary | MOCK_PASS | `node scripts/verify-boundaries.mjs`; no HTTP client calls, provider URLs, DSNs or credential/PIN patterns in runtime sources |
| Web package installation | MOCK_PASS | Clean `npx --yes npm@11 ci` completed; original npm 10 encountered an edgesOut resolver error while changing dev dependencies; npm 11 resolved it |
| Web compile/lint | MOCK_PASS | `npm run typecheck`, `npm run lint`, `npm run build` |
| Web repository/components | MOCK_PASS | `npm test`: 5 passing tests, isolated repositories, immutable snapshots, mutations, empty/error recovery, RHF/Zod validation, theme persistence |
| Web browser behavior | MOCK_PASS | `npm run test:e2e`: 3 passing Chrome tests; desktop, phone and gallery; zero external requests/page errors in application flow |
| Flutter dependency resolution | MOCK_PASS | `flutter pub get`, committed lockfile |
| Flutter static analysis | MOCK_PASS | `flutter analyze`: no issues |
| Flutter repository/widgets | MOCK_PASS | `flutter test`: 6 tests pass: repository isolation/guards; shell navigation/back/themes; state/scenarios/preferences; light/dark goldens |
| Flutter compiled preview | MOCK_PASS | `flutter build web --no-web-resources-cdn`; browser test configured in `web/playwright.flutter.config.ts` |
| Flutter compiled browser behavior | MOCK_PASS | `npx playwright test --config=playwright.flutter.config.ts`: 1 passing test; local renderer/fonts/fixtures only, light/dark appearance, native-widget root navigation |
| Android debug APK compilation | MOCK_PASS | `flutter build apk --debug`; artifact is under ignored `mobile/build/app/outputs/flutter-apk/` |
| Native-device validation | EXTERNAL_PENDING | See IMPLEMENTATION_REPORT.md; widget/Chrome tests do not certify Android/iOS devices |
| Backend/provider integration | NOT_STARTED | No backend files or public contracts changed; no production request or provider action executed |

From `web/`, run `npx playwright test --config=playwright.flutter.config.ts` after building Flutter web. It verifies the actual compiled widgets, both themes, root navigation and local-only renderer/fonts/assets. Screenshot evidence lives under `docs/evidence/`. Flutter goldens are under `mobile/test/goldens/` and use the bundled Inter/Material icon fonts; update only after reviewing intentional visual changes.

## Manual reproduction

1. Start Next.js (`cd web; npm run dev`) and open loopback port 3000. Confirm MOCK MODE and fictional/pending/unavailable copy.
2. Complete/reopen a demo task. The open-task KPI and checklist update together. No money, documents, messages or device states change.
3. Filter property type, open a sample detail and return. Choose empty/error/populated scenarios to see actual repository states.
4. Change theme, refresh and verify persistence. Test the narrow layout's five root navigation links.
5. Open Settings, submit a blank workspace label, correct it, save and return to Home. The portfolio label changes in local memory. Refresh resets it.
6. Run Flutter on a supported device or Chrome. Exercise the same shell, scenario, theme, task and label behavior. In Flutter, theme override lasts for the session.
7. Open the offline gallery separately. Use desktop/mobile filtering and pair links; original screenshot references stay available without external assets.

Source HTML intentionally remains unmodified and may contain misleading demo claims or example access codes. Neither app renders those templates, uses their credentials/links, or presents those claims as fact.
