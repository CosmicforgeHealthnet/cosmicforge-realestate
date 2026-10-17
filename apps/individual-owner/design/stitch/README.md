# Stitch design references - Individual Owner

These files were exported by Google Stitch and are preserved as reference inputs, **not application source**.

- Open `index.html` to browse the screens (offline screenshots and HTML preview links).
- `original/stitch_individual_owner_design_system/*/screen.png`: static image references.
- `original/stitch_individual_owner_design_system/*/code.html`: original static HTML templates.
- `original/stitch_individual_owner_design_system/individual_owner_design.md` and `cosmicforge_individual_owner/DESIGN.md`: exported specifications.
- Keep this design reference separate from runnable Next.js (`apps/individual-owner/web`) and Flutter (`apps/individual-owner/mobile`) application code.
- The exported files contain fictional payment, legal, and security claims. Do not reuse these as verified production data or real actions.
- Before committing, review assets for license/usage restrictions and sensitive information. Never embed real access PINs, credentials or customer data.

Run `powershell -File apps/individual-owner/Setup_Individual_Owner_Stitch_Reference.ps1` from the repository root. It validates the exact archive catalogue, rejects traversal/duplicate paths, checks existing source bytes before extraction, and regenerates the gallery/inventory/manifest. It never overwrites altered originals. The optional `-Repo` and `-Zip` parameters support isolated verification.

Run `node apps/individual-owner/scripts/verify-references.mjs` to verify all 63 SHA-256 entries and every gallery source/preview path. Manifest paths are relative to `apps/individual-owner/`. The archive contains example legal, financial and access claims; preserve these only as archival material. Neither application serves the original HTML or bundles these screenshots.
