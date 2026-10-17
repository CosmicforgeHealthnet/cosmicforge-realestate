import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:cosmicforge_owner/app/owner_app.dart';
import 'package:cosmicforge_owner/core/widgets/workspace_controls.dart';
import 'package:cosmicforge_owner/core/models/workspace.dart';
import 'package:cosmicforge_owner/features/dashboard/dashboard_screen.dart'
    show DemoPanel;
import 'package:cosmicforge_owner/core/theme/owner_theme.dart';
import 'package:cosmicforge_owner/core/router/owner_router.dart';
import 'workspace_ui_journeys_test.dart' as ui;
import 'package:cosmicforge_owner/features/workspace/workspace_controller.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  testWidgets(
    'All 30 areas and editable forms at narrow width, large text, both themes',
    (tester) async {
      tester.view.physicalSize = const Size(360, 1000);
      tester.view.devicePixelRatio = 1;
      tester.platformDispatcher.textScaleFactorTestValue = 1.6;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);
      final container = ui.uiContainer();
      addTearDown(container.dispose);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const OwnerApp(),
        ),
      );
      await tester.pumpAndSettle();
      for (final theme in [ThemeMode.light, ThemeMode.dark]) {
        container.read(themeModeProvider.notifier).state = theme;
        await tester.pumpAndSettle();
        for (final area in ui.definition['modules']) {
          await ui.route(
            tester,
            container,
            area['id'] == '01' ? '/' : '/workspace/${area["slug"]}',
          );
          expect(
            tester.takeException(),
            isNull,
            reason: '${area["slug"]} ${theme.name}',
          );
        }
      }
      for (final spec in ui.definition['types']) {
        if (spec['readOnly'] == true) continue;
        await ui.route(
          tester,
          container,
          '/workspace/${spec["module"]}/${spec["id"]}/new',
        );
        expect(find.byKey(const Key('field-title')), findsOneWidget);
        await ui.tapVisible(
          tester,
          find.text(spec['wizard'] == true ? 'Next step' : 'Save demo record'),
        );
        expect(
          find.text('Record title is required.'),
          findsOneWidget,
          reason: spec['id'],
        );
        expect(tester.takeException(), isNull, reason: spec['id']);
      }
      await ui.route(tester, container, '/workspace/properties/property/new');
      await tester.showKeyboard(find.byKey(const Key('field-title')));
      await tester.enterText(
        find.byKey(const Key('field-title')),
        'Keyboard draft',
      );
      await ui.tapVisible(tester, find.text('Next step'));
      expect(tester.takeException(), isNull);
      container.read(routerProvider).go('/more');
      await tester.pumpAndSettle();
      expect(find.text('Discard draft?'), findsOneWidget);
      await tester.tap(find.text('Cancel').last);
      await tester.pumpAndSettle();
      expect(
        container.read(routerProvider).routeInformationProvider.value.uri.path,
        contains('/new'),
      );
    },
  );
  testWidgets(
    'All sixteen QA conditions reset deterministic state through native controls',
    (tester) async {
      tester.view.physicalSize = const Size(390, 1000);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final container = ui.uiContainer();
      addTearDown(container.dispose);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const OwnerApp(),
        ),
      );
      await tester.pumpAndSettle();
      await ui.route(tester, container, '/demo-lab');
      await ui.tapVisible(tester, find.text('Enable developer QA'));
      for (final scenario in ui.definition['scenarios']) {
        final field = find.byWidgetPredicate(
          (w) =>
              w is DropdownButtonFormField<String> &&
              w.decoration.labelText == 'QA scenario',
        );
        await ui.tapVisible(tester, field);
        await ui.tapVisible(
          tester,
          find.text(scenario.toString().replaceAll('_', ' ')).last,
        );
        expect(
          container.read(workspaceProvider).requireValue.scenario,
          scenario,
        );
        expect(tester.takeException(), isNull, reason: scenario);
      }
    },
  );
  testWidgets(
    'Property scope filters tasks, retains account controls, resets through QA',
    (tester) async {
      tester.view.physicalSize = const Size(390, 1000);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final container = ui.uiContainer();
      addTearDown(container.dispose);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const OwnerApp(),
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('Account and portfolio'));
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(
          of: find.byType(PopupMenuItem<String>),
          matching: find.text('Palm Studio · Demo'),
        ),
      );
      await tester.pumpAndSettle();
      expect(container.read(propertyScopeProvider), 'demo-property-02');
      await tester.scrollUntilVisible(
        find.text('Prepare a fictional viewing checklist'),
        300,
        scrollable: find
            .byWidgetPredicate(
              (w) => w is Scrollable && w.axisDirection == AxisDirection.down,
            )
            .last,
      );
      expect(find.text('Review a sample maintenance note'), findsNothing);
      for (var i = 0; i < 13; i++) {
        await container
            .read(workspaceProvider.notifier)
            .execute(
              WorkspaceCommand(
                op: 'create',
                type: 'asset',
                data: {
                  'title': 'Scope asset ${i.toString().padLeft(2, '0')}',
                  'propertyId': 'demo-property-02',
                  'category': 'Furniture',
                  'condition': 'Good',
                  'notes': '',
                },
              ),
            );
      }
      await ui.route(tester, container, '/workspace/properties?tab=asset');
      await tester.scrollUntilVisible(
        find.text('Next page'),
        300,
        scrollable: find
            .byWidgetPredicate(
              (w) => w is Scrollable && w.axisDirection == AxisDirection.down,
            )
            .last,
      );
      await ui.tapVisible(tester, find.text('Next page'));
      await tester.tap(find.byTooltip('Account and portfolio'));
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(
          of: find.byType(PopupMenuItem<String>),
          matching: find.text('Sapphire Court · Demo'),
        ),
      );
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('record-demo-asset-01')), findsOneWidget);
      expect(find.text('Next page'), findsNothing);
      expect(tester.takeException(), isNull);
      await ui.route(tester, container, '/workspace/identity');
      await tester.scrollUntilVisible(
        find.byKey(const Key('record-demo-account-01')),
        300,
        scrollable: find
            .byWidgetPredicate(
              (w) => w is Scrollable && w.axisDirection == AxisDirection.down,
            )
            .last,
      );
      expect(find.byKey(const Key('record-demo-account-01')), findsOneWidget);
      await tester.tap(find.byTooltip('Account and portfolio'));
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(
          of: find.byType(PopupMenuItem<String>),
          matching: find.text('All properties'),
        ),
      );
      await tester.pumpAndSettle();
      await ui.route(tester, container, '/workspace/viewings');
      await ui.tapVisible(
        tester,
        find.byWidgetPredicate(
          (w) => w is DropdownButton<String> && w.value == 'ALL',
        ),
      );
      await ui.tapVisible(tester, find.text('OPEN').last);
      await tester.scrollUntilVisible(
        find.byKey(const Key('record-demo-slot-01')),
        300,
        scrollable: find
            .byWidgetPredicate(
              (w) => w is Scrollable && w.axisDirection == AxisDirection.down,
            )
            .last,
      );
      await ui.tapVisible(tester, find.byKey(const Key('record-demo-slot-01')));
      await ui.tapVisible(tester, find.byKey(const Key('action-request')));
      await tester.tap(find.byTooltip('Back'));
      await tester.pumpAndSettle();
      expect(
        find.text('No matching records. Change the search or status filter.'),
        findsOneWidget,
      );
      expect(tester.takeException(), isNull);
      await ui.route(tester, container, '/workspace/shortlets');
      await ui.tapVisible(tester, find.text('Calendar view'));
      await tester.ensureVisible(find.byType(CalendarDatePicker));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(ValueKey<DateTime>(DateTime(2026, 10, 26))));
      await tester.pumpAndSettle();
      final panel = find
          .ancestor(
            of: find.byType(CalendarDatePicker),
            matching: find.byType(DemoPanel),
          )
          .first;
      expect(
        find.descendant(
          of: panel,
          matching: find.text('Short-let bookings · Demo'),
        ),
        findsOneWidget,
      );
      await tester.ensureVisible(find.byType(CalendarDatePicker));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(ValueKey<DateTime>(DateTime(2026, 10, 28))));
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: panel,
          matching: find.text('Short-let bookings · Demo'),
        ),
        findsNothing,
      );
      await ui.route(tester, container, '/workspace/commercial');
      expect(find.text('Calendar view'), findsOneWidget);
      await ui.route(tester, container, '/demo-lab');
      await ui.tapVisible(tester, find.text('Enable developer QA'));
      await ui.tapVisible(
        tester,
        find.byWidgetPredicate(
          (w) =>
              w is DropdownButtonFormField<String> &&
              w.decoration.labelText == 'QA scenario',
        ),
      );
      await ui.tapVisible(tester, find.text('empty').last);
      expect(container.read(propertyScopeProvider), 'all');
      expect(tester.takeException(), isNull);
    },
  );
}
