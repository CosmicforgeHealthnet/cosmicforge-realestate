import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'dart:io';
import 'dart:convert';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:cosmicforge_owner/app/owner_app.dart';
import 'package:cosmicforge_owner/core/models/dashboard.dart';
import 'package:cosmicforge_owner/core/router/owner_router.dart';
import 'package:cosmicforge_owner/core/theme/owner_theme.dart';
import 'package:cosmicforge_owner/features/dashboard/dashboard_controller.dart';
import 'package:cosmicforge_owner/core/mock/mock_owner_repository.dart';
import 'package:cosmicforge_owner/features/workspace/module_screen.dart';

final fixture = File('assets/dashboard.json').readAsStringSync();
final moduleFixture =
    (jsonDecode(File('assets/modules.json').readAsStringSync()) as List)
        .cast<Map<String, dynamic>>();
ProviderContainer testContainer() => ProviderContainer(
  overrides: [
    modulesProvider.overrideWith((ref) async => moduleFixture),
    ownerRepositoryProvider.overrideWithValue(
      MockOwnerRepository(
        loadDefinition: () async =>
            File('assets/workflows.json').readAsStringSync(),
        loadUniverse: () async =>
            File('assets/workspace-seed.json').readAsStringSync(),
        load: () async => fixture,
      ),
    ),
  ],
);

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUpAll(() async {
    final inter = FontLoader('Inter')
      ..addFont(rootBundle.load('assets/fonts/Inter.ttf'));
    final icons = FontLoader('MaterialIcons')
      ..addFont(rootBundle.load('fonts/MaterialIcons-Regular.otf'));
    await inter.load();
    await icons.load();
  });
  testWidgets('phone shell navigation, native detail and theme switching', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(390, 844);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final container = testContainer();
    addTearDown(container.dispose);
    await tester.pumpWidget(
      UncontrolledProviderScope(container: container, child: const OwnerApp()),
    );
    await tester.pumpAndSettle();
    expect(find.text('Welcome back, Alex.'), findsOneWidget);
    await tester.tap(find.byTooltip('Switch to dark theme'));
    await tester.pumpAndSettle();
    expect(
      Theme.of(tester.element(find.text('Welcome back, Alex.'))).brightness,
      Brightness.dark,
    );
    await tester.tap(find.text('Properties').last);
    await tester.pumpAndSettle();
    expect(find.text('Properties, units, land & assets'), findsOneWidget);
    await tester.scrollUntilVisible(
      find.byKey(const Key('record-demo-property-01')),
      300,
      scrollable: find
          .byWidgetPredicate(
            (w) => w is Scrollable && w.axisDirection == AxisDirection.down,
          )
          .last,
    );
    await tester.drag(find.byType(ListView).last, const Offset(0, -250));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('record-demo-property-01')));
    await tester.pumpAndSettle();
    expect(find.text('Sapphire Court · Demo'), findsOneWidget);
    await tester.tap(find.byTooltip('Back'));
    await tester.pumpAndSettle();
    expect(
      container.read(routerProvider).routeInformationProvider.value.uri.path,
      '/workspace/properties',
    );
    await tester.tap(find.text('More').last);
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'Insurance');
    await tester.pumpAndSettle();
    expect(find.text('Insurance & protection'), findsOneWidget);
    await tester.tap(find.text('Insurance & protection'));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'No insurer is connected. No policy issuance, coverage guarantee or underwriting is available.',
      ),
      findsOneWidget,
    );
    expect(tester.takeException(), isNull);
  });
  testWidgets(
    'controller updates checklist, empty/error/recovery and preferences',
    (tester) async {
      final container = testContainer();
      addTearDown(container.dispose);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const OwnerApp(),
        ),
      );
      await tester.pumpAndSettle();
      final controller = container.read(dashboardProvider.notifier);
      await controller.setTaskDone('demo-task-01', true);
      await tester.pumpAndSettle();
      expect(
        container
            .read(dashboardProvider)
            .requireValue
            .tasks
            .where((t) => !t.done)
            .length,
        1,
      );
      await controller.scenario(DemoScenario.empty);
      await tester.pumpAndSettle();
      expect(
        container.read(dashboardProvider).requireValue.properties,
        isEmpty,
      );
      await controller.scenario(DemoScenario.error);
      await tester.pumpAndSettle();
      expect(find.text('Unable to show demo records'), findsOneWidget);
      await controller.scenario(DemoScenario.populated);
      await tester.pumpAndSettle();
      container.read(routerProvider).go('/workspace/settings');
      await tester.pumpAndSettle();
      await tester.scrollUntilVisible(
        find.text('Save demo preference'),
        300,
        scrollable: find
            .byWidgetPredicate(
              (w) => w is Scrollable && w.axisDirection == AxisDirection.down,
            )
            .last,
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('Save demo preference'));
      await tester.pumpAndSettle();
      expect(find.text('Use 2–60 characters.'), findsOneWidget);
      await tester.enterText(
        find.byType(TextFormField),
        'My fictional workspace',
      );
      await tester.scrollUntilVisible(
        find.text('Save demo preference'),
        300,
        scrollable: find
            .byWidgetPredicate(
              (w) => w is Scrollable && w.axisDirection == AxisDirection.down,
            )
            .last,
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('Save demo preference'));
      await tester.pumpAndSettle();
      expect(find.text('Demo preference saved locally.'), findsOneWidget);
      container.read(routerProvider).go('/');
      await tester.pumpAndSettle();
      expect(
        container.read(dashboardProvider).requireValue.workspaceName,
        'My fictional workspace',
      );
      expect(tester.takeException(), isNull);
    },
  );
  for (final mode in [ThemeMode.light, ThemeMode.dark]) {
    testWidgets('phone foundation golden ${mode.name}', (tester) async {
      tester.view.physicalSize = const Size(390, 844);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final container = testContainer();
      addTearDown(container.dispose);
      container.read(themeModeProvider.notifier).state = mode;
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const OwnerApp(),
        ),
      );
      await tester.pumpAndSettle();
      await expectLater(
        find.byType(OwnerApp),
        matchesGoldenFile('goldens/dashboard-${mode.name}.png'),
      );
      expect(tester.takeException(), isNull);
    });
  }
}
