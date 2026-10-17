# Foundation decisions · 17 October 2026

The execution mandate limits this delivery to Sections 1–5 and app foundations. The broader instructions in the master plan remain future work.

`design/tokens/tokens.json` uses the explicit semantic palette and 10/16/22/999 px component radii described in both source documents. These take precedence over the generic YAML rem radius scale and Material palette. The mandate's success teal is #087A66. Dark actions use #6C96FF for contrast while the sapphire brand remains #2459E0. Generated CSS and Dart are checked for drift. Inter is bundled from the official Google Fonts repository under the included SIL Open Font License (`shared/fonts/OFL.txt`); the generator copies identical font bytes to both apps. No font CDN is used.

The existing valid `design/stitch/source/` library was verified byte-for-byte against the ZIP, then moved to the required `original/` path. No original file was edited. The setup script now refuses source changes rather than extracting with `-Force`. Source HTML may contain unverified claims and example access codes; it is archived reference material only, never served by either application. The original archive cannot be sanitized without violating preservation. Runtime fixtures contain no access codes, credentials or copied customer records.

Archive: 11,323,929 bytes; largest source PNG: 710,825 bytes. No repository LFS configuration or binary-size restriction was found. One archive plus one extraction is retained in Git, as explicitly requested. Later LFS migration is possible without changing local source bytes. Reference assets are excluded from app bundles.

Dashboard shapes and panel hierarchy follow pair 01. Synthetic architectural drawings replace externally hosted photos. Legal, banking, payout, government verification, signature and smart-lock claims are replaced by pending/restricted/unavailable labels. This is a private mock foundation, not an approved public brand or completed workflow.
