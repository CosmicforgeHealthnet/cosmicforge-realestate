# Interactive mock acceptance · engineering evidence

Engineering status: MOCK_PASS for Sections 6–10 and all 30 module workspaces on both platforms. Human Phase 5 acceptance: NOT_STARTED. API integration: NOT_STARTED. Provider execution: EXTERNAL_PENDING. MOCK_PASS is a local workflow result and never an integrated or production certification.

## Review procedure

1. Start Next.js with `npm run dev -- --port 3100` in `web/`, or production `npm run build` then `npm run start -- --port 3100`. Use `/more` to review all areas. Native Flutter: `flutter run -d <device-id>` from `mobile/`.
2. Open Developer QA from the directory or account menu; opt in, select Normal and Reset Demo Data. Scenario changes reset fixtures deterministically. Select a property to inspect contextual records.
3. Review the ten core journeys from `shared/journeys.json`: onboarding; property→listing; prospect→tenancy; rent/deposit; tenancy lifecycle; short-let; maintenance; authority/delegation; commercial/land/sale; insights/privacy.
4. Review `shared/supplementary-journeys.json` for utilities/budgets, appointments/assets/evidence/viewing slots, insurance/access metadata and negative consent/quote guards.
5. Try invalid required fields, stale/duplicate conflict, expired consent, restricted/offline/session/permission scenarios and an unknown financial outcome. Failed operations must leave revision and records unchanged. Provider buttons must explain their unavailable status.
6. Review both themes, keyboard tab/Escape behavior, cancel/discard, deep links/back navigation, property scope, calendars, evidence metadata and JSON report exports. Inspect screenshots and exact commands in IMPLEMENTATION_REPORT.md.

## Engineering acceptance checks

- Thirty web + thirty native coverage rows; all areas have working fictional records/forms and relevant decisions, with financing deliberately provider-gated.
- Shared stable references and linked side effects; independent TS/Dart engines pass the same journey contracts. Invalid writes roll back the whole state.
- Financial bucket totals derive from rent statuses. Review/closing/transfer/settlement and evidence/submission/review remain distinct states.
- Managers have scoped listing/viewing/maintenance capabilities only. Revocation/suspension/expiry removes simulated write permission; title and payout actions remain owner-only. Representatives have zero title shares.
- Every accepted co-owner requires current purpose-specific consent for publication/transfer. Competing quote approval and overlapping bookings/blocks are rejected.
- Source boundary scan and browser network checks reject service/provider requests, remote runtime data endpoints, secret/access-code patterns and real execution claims. Flutter CanvasKit loads from local bundled files.
- Five web widths (1440/1280/1024/768/390); native 360px at 160% text scale in both themes, required-field forms, keyboard/draft protection and reviewed visual baselines.

Record reviewer decisions separately; automated tests do not grant permission for integration, merge or deployment.
