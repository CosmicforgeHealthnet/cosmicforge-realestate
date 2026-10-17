# CosmicForge Individual Owner · Next.js foundation

From this directory (Node >=22.12; tested on 25.2.1):

```powershell
npm ci
npm run dev
```

Open http://127.0.0.1:3000. For a production preview, run `npm run build` then `npm run start`.

Checks: `npm run typecheck`, `npm run lint`, `npm test`, `npm run build`, `npm run test:e2e`. Browser tests use the installed Chrome (`channel: chrome`) and start a local production server when one is not already running. Build before browser testing. Screenshots are written to `../docs/evidence/`.

After building Flutter web, `npx playwright test --config=playwright.flutter.config.ts` serves that build on loopback port 4173 and verifies Flutter rendering, theme switching, root navigation and local-only asset requests. Python 3 is used solely as this static test server.

`src/app` owns routes and error boundaries; `components` owns the shell, states and local property illustrations; `features` owns dashboard state and the React Hook Form/Zod preference form. UI → TanStack Query feature hook → `OwnerRepository` → `MockOwnerRepository`. No HTTP adapter or backend environment is needed. Providers create isolated repository/query-client instances. Refresh resets fictional task/preference changes. Theme choice persists under the versioned localStorage key, falling back to memory if storage is unavailable.

`../shared` contains presentation fixtures and the 30-area registry. `npm run tokens` generates theme files and copies licensed fonts/fixtures; `node ../scripts/generate-tokens.mjs --check` verifies parity. The bundler root includes this owner workspace so shared JSON imports resolve; see the official [Turbopack root documentation](https://nextjs.org/docs/app/api-reference/config/next-config-js/turbopack#root-directory). Original Stitch HTML/screenshots are never imported into the application bundle.

All financial/provider controls are unavailable. The directory and most routes establish navigation only. No module workflow is accepted from the existence of its route or design reference.
