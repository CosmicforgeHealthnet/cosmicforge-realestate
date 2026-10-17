# Inter font provenance

Source: https://github.com/google/fonts/tree/main/ofl/inter

Variable font: `Inter[opsz,wght].ttf`, saved as `Inter.ttf`, obtained 17 October 2026. Copyright The Inter Project Authors. Licensed under SIL OFL 1.1; see `OFL.txt`. No modifications to font bytes. The token generator copies this font and its license to the web and mobile apps and verifies byte parity with `--check`.

Roboto fallback: unmodified `roboto-regular.ttf` from Flutter 3.44.2's `bin/cache/artifacts/material_fonts/`, with the accompanying `roboto_license.txt` (Apache 2.0), saved as `Roboto-Regular.ttf` / `Roboto-LICENSE.txt`. Flutter's renderer expects this default fallback family even with an Inter theme. Registering it locally avoids the observed fonts.gstatic.com request in the compiled Flutter preview. Inter remains the UI typeface.
