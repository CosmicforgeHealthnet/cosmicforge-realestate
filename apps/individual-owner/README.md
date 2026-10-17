# Individual Property Owner

Primary product surface for the **Individual Property Owner** domain.

Backend authority: `individual-owner-service`.

Shared capabilities are consumed through the API Gateway/shared platform services; human messaging/calling/video uses CF Meet.

## Interactive frontend mock · Sections 1–10

`design/stitch/index.html` is the offline reference gallery (30 originals / 15 pairs); it is separate from either runnable application. Keep the archive and `original/` files unchanged. Rebuild using `Setup_Individual_Owner_Stitch_Reference.ps1`; verify with `node scripts/verify-references.mjs`.

- Web: `cd web`, `npm ci`, `npm run dev` → http://127.0.0.1:3000.
- Mobile: `cd mobile`, `flutter pub get`, `flutter run -d <device-id>`; Chrome is also available for local widget preview.
- Shared design: `design/tokens/tokens.json`; regenerate using `node scripts/generate-tokens.mjs`, or verify with `--check`.
- Shared fictional fixtures: `shared/`; all writes stay in application memory.
- Acceptance/evidence: `docs/MOCK_ACCEPTANCE.md`, `docs/IMPLEMENTATION_REPORT.md`, `docs/evidence/`.
- Scope/status: `docs/FRONTEND_PROGRESS.md`, `docs/SCREEN_COVERAGE.csv`, `docs/KNOWN_LIMITATIONS.md`.
- Future contract authority: `docs/API_MAPPING.md`. Integration has not started.

All 30 areas now have connected local workflows on web and Flutter, including ten core and four supplementary tested journeys. Use More → Developer QA to reset data, reproduce 16 scenarios or inspect fictional state. All writes are in memory. Human mock acceptance is pending; API/provider integration remains NOT_STARTED/EXTERNAL_PENDING. No merge or deployment has occurred.

Web verification: `npm test`, `npm run lint`, `npm run typecheck`, `npm run build`, `npm run test:e2e`, `npm run test:workflows`. The browser suites use isolated port 3100. Native verification: `flutter analyze`, `flutter test`, `flutter build web --release --no-wasm-dry-run`, `flutter build apk --debug`. See the report for exact evidence and environmental limits.
