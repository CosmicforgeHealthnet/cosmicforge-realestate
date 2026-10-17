import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:cosmicforge_owner/app/owner_app.dart';
import 'package:cosmicforge_owner/core/theme/owner_theme.dart';
import 'workspace_ui_journeys_test.dart' as ui;
import 'package:cosmicforge_owner/features/workspace/workspace_controller.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUpAll(() async {
    await (FontLoader(
      'Inter',
    )..addFont(rootBundle.load('assets/fonts/Inter.ttf'))).load();
    await (FontLoader(
      'MaterialIcons',
    )..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'))).load();
  });
  for (final mode in [ThemeMode.light, ThemeMode.dark]) {
    for (final entry in {
      'listings': '/workspace/listings',
      'property-form': '/workspace/properties/property/new',
      'finance': '/workspace/rent',
      'calendar': '/workspace/viewings',
    }.entries) {
      testWidgets('Native visual ${entry.key} ${mode.name}', (tester) async {
        tester.view.physicalSize = const Size(390, 1000);
        tester.view.devicePixelRatio = 1;
        addTearDown(tester.view.resetPhysicalSize);
        addTearDown(tester.view.resetDevicePixelRatio);
        final container = ui.uiContainer();
        addTearDown(container.dispose);
        container.read(themeModeProvider.notifier).state = mode;
        await tester.pumpWidget(
          UncontrolledProviderScope(
            container: container,
            child: const OwnerApp(),
          ),
        );
        await tester.pumpAndSettle();
        await ui.route(tester, container, entry.value);
        if (entry.key == 'calendar') {
          await ui.tapVisible(tester, find.text('Calendar view'));
        }
        await expectLater(
          find.byType(OwnerApp),
          matchesGoldenFile(
            '../../docs/screenshots/interactive/flutter-${entry.key}-${mode.name}.png',
          ),
        );
        expect(tester.takeException(), isNull);
      });
    }
  }
  for (final scenario in ['permission_denied', 'restricted']) {
    testWidgets('Native guarded visual $scenario', (tester) async {
      tester.view.physicalSize = const Size(390, 1000);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final container = ui.uiContainer();
      addTearDown(container.dispose);
      container.read(themeModeProvider.notifier).state =
          scenario == 'restricted' ? ThemeMode.dark : ThemeMode.light;
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const OwnerApp(),
        ),
      );
      await tester.pumpAndSettle();
      await container.read(workspaceProvider.notifier).scenario(scenario);
      await ui.route(
        tester,
        container,
        scenario == 'permission_denied' ? '/' : '/workspace/properties',
      );
      if (scenario == 'permission_denied') {
        expect(find.text('Sapphire Court · Demo'), findsNothing);
      }
      await expectLater(
        find.byType(OwnerApp),
        matchesGoldenFile(
          '../../docs/screenshots/interactive/flutter-$scenario.png',
        ),
      );
      expect(tester.takeException(), isNull);
    });
  }
}
