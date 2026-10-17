# CosmicForge Individual Owner · Flutter foundation

Native Flutter widgets, Riverpod controllers/repository injection, GoRouter shell and five root destinations: Home, Properties, Listings, Wallet, More. Both ThemeData variants use generated brand colors and the bundled OFL Inter font. `Dio` is reserved for later integration; no HTTP client is active.

From this directory:

```powershell
flutter pub get
flutter analyze
flutter test
flutter run -d <android-or-ios-device-id>
```

On this Windows workstation, `flutter run -d chrome` is an available preview of the Flutter widgets. It does not certify Android/iOS execution. `flutter build web --no-web-resources-cdn` bundles renderer resources locally. For a static preview, serve `build/web` at the host root; GoRouter uses its default hash URL strategy.

The demo dashboard reads local fixtures through `OwnerRepository`. Task toggles and workspace labels change in memory, reset on restart, and never execute external actions. The scenario control demonstrates populated/empty/error states. Product-area routes are explicitly unfinished. See `../docs/MOCK_ACCEPTANCE.md`, `KNOWN_LIMITATIONS.md` and `SCREEN_COVERAGE.csv`.

The project includes generated Android/iOS runners. Store IDs, approved artwork, signing, release hardening and native device acceptance are later work. Generated debug signing is development-only. No production credentials are included.
