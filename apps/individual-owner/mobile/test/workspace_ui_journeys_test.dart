import 'dart:convert';
import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:cosmicforge_owner/app/owner_app.dart';
import 'package:cosmicforge_owner/core/mock/mock_owner_repository.dart';
import 'package:cosmicforge_owner/core/models/workspace.dart';
import 'package:cosmicforge_owner/core/router/owner_router.dart';
import 'package:cosmicforge_owner/features/dashboard/dashboard_controller.dart';
import 'package:cosmicforge_owner/features/workspace/workspace_controller.dart';

final definition =
    jsonDecode(File('assets/workflows.json').readAsStringSync()) as Json;
final stories = [
  ...jsonDecode(File('../shared/journeys.json').readAsStringSync()) as List,
  ...jsonDecode(
        File('../shared/supplementary-journeys.json').readAsStringSync(),
      )
      as List,
];
dynamic pathValue(dynamic value, String path) =>
    path.split('.').fold<dynamic>(value, (v, k) => v[k]);
ProviderContainer uiContainer() => ProviderContainer(
  overrides: [
    ownerRepositoryProvider.overrideWithValue(
      MockOwnerRepository(
        load: () async => File('assets/dashboard.json').readAsStringSync(),
        loadDefinition: () async =>
            File('assets/workflows.json').readAsStringSync(),
        loadUniverse: () async =>
            File('assets/workspace-seed.json').readAsStringSync(),
      ),
    ),
  ],
);
Future<void> tapVisible(WidgetTester tester, Finder finder) async {
  await tester.ensureVisible(finder);
  await tester.pumpAndSettle();
  await tester.tap(finder);
  await tester.pumpAndSettle();
}

Future<void> route(
  WidgetTester tester,
  ProviderContainer container,
  String path,
) async {
  container.read(routerProvider).go(path);
  await tester.pumpAndSettle();
  if (find.text('Discard draft?').evaluate().isNotEmpty) {
    await tester.tap(find.text('Confirm demo action'));
    await tester.pumpAndSettle();
  }
}

Future<void> fill(WidgetTester tester, Json spec, Json data) async {
  final groups = spec['wizard'] == true ? 3 : 1;
  for (var step = 0; step < groups; step++) {
    for (final f in spec['fields']) {
      final key = f['key'] as String;
      if (!data.containsKey(key)) {
        continue;
      }
      final finder = find.byKey(Key('field-$key'));
      if (finder.evaluate().isEmpty) {
        continue;
      }
      await tester.ensureVisible(finder);
      await tester.pumpAndSettle();
      if (f['kind'] == 'boolean') {
        final tile = tester.widget<SwitchListTile>(finder);
        if (tile.value != data[key]) {
          await tester.tap(finder);
          await tester.pumpAndSettle();
        }
      } else if (['select', 'relation'].contains(f['kind'])) {
        await tester.tap(finder);
        await tester.pumpAndSettle();
        final item = f['kind'] == 'relation'
            ? find.textContaining(data[key].toString()).last
            : find.text(data[key].toString()).last;
        await tester.ensureVisible(item);
        await tester.tap(item);
        await tester.pumpAndSettle();
      } else {
        await tester.enterText(finder, data[key].toString());
        await tester.pump();
      }
    }
    if (step < groups - 1) {
      await tapVisible(tester, find.text('Next step'));
    }
  }
  if (spec['sensitive'] == true) {
    final challenge = find.byKey(const Key('security-confirmation'));
    await tester.ensureVisible(challenge);
    await tester.enterText(challenge, 'DEMO');
  }
  await tapVisible(tester, find.text('Save demo record'));
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUpAll(() async {
    final font = FontLoader('Inter')
      ..addFont(rootBundle.load('assets/fonts/Inter.ttf'));
    await font.load();
  });
  for (final story in stories) {
    testWidgets(
      'UI Journey ${story["id"]}: ${story["title"]}',
      (tester) async {
        tester.view.physicalSize = const Size(390, 1000);
        tester.view.devicePixelRatio = 1;
        addTearDown(tester.view.resetPhysicalSize);
        addTearDown(tester.view.resetDevicePixelRatio);
        final container = uiContainer();
        addTearDown(container.dispose);
        await tester.pumpWidget(
          UncontrolledProviderScope(
            container: container,
            child: const OwnerApp(),
          ),
        );
        await tester.pumpAndSettle();
        final aliases = <String, String>{};
        dynamic resolve(dynamic value) =>
            value is String && value.startsWith(r'$')
            ? aliases[value.substring(1)]
            : value;
        WorkspaceSnapshot snapshot() =>
            container.read(workspaceProvider).requireValue;
        for (final raw in story['steps']) {
          final step = Json.from(raw);
          if (step['op'] == 'scenario' || step['op'] == 'actor') {
            await route(tester, container, '/demo-lab');
            await tapVisible(tester, find.text('Enable developer QA'));
            final field = find.byWidgetPredicate(
              (w) =>
                  w is DropdownButtonFormField<String> &&
                  w.decoration.labelText ==
                      (step['op'] == 'scenario'
                          ? 'QA scenario'
                          : 'Simulated actor'),
            );
            await tapVisible(tester, field);
            final label = step['op'] == 'scenario'
                ? step['value'].toString().replaceAll('_', ' ')
                : step['value'] == 'owner'
                ? 'Property owner'
                : 'Delegated manager';
            await tapVisible(tester, find.text(label).last);
            continue;
          }
          if (step['op'] == 'alias') {
            aliases[step['alias']] = step['id'];
            continue;
          }
          if (step['op'] == 'bind') {
            final rows = snapshot().records.where(
              (r) =>
                  r.type == step['type'] &&
                  r.data[step['key']] == resolve(step['value']),
            );
            expect(rows, isNotEmpty);
            aliases[step['alias']] = rows.last.id;
            continue;
          }
          if (step['op'] == 'assert') {
            final r = snapshot().records.firstWhere(
              (r) => r.id == aliases[step['target']],
            );
            expect(
              pathValue({'status': r.status, 'data': r.data}, step['key']),
              resolve(step['value']),
            );
            continue;
          }
          if (step['op'] == 'summary') {
            expect(
              pathValue(
                deriveWorkspace(definition, snapshot()).summary(),
                step['key'],
              ),
              step['value'],
            );
            continue;
          }
          final current = snapshot(),
              target = step['target'] != null
                  ? current.records.firstWhere(
                      (r) => r.id == aliases[step['target']],
                    )
                  : null;
          final type = target?.type ?? step['type'],
              spec = (definition['types'] as List).cast<Json>().firstWhere(
                (s) => s['id'] == type,
              ),
              previous = current.revision;
          if (step['op'] == 'create' || step['op'] == 'update') {
            final url =
                '/workspace/${spec["module"]}/$type/${target?.id ?? "new"}${target != null ? "/edit" : ""}';
            await route(tester, container, url);
            await fill(
              tester,
              spec,
              Json.from(
                step['data'] ?? {},
              ).map((k, v) => MapEntry(k, resolve(v))),
            );
            if (step['error'] == null && step['alias'] != null) {
              aliases[step['alias']] = snapshot().records
                  .lastWhere(
                    (r) =>
                        r.type == type &&
                        r.data['title'] == resolve(step['data']['title']),
                  )
                  .id;
            }
          } else {
            await route(
              tester,
              container,
              recordPath(definition, type, target!.id),
            );
            final action = (spec['actions'] as List).cast<Json>().firstWhere(
              (a) => a['id'] == step['action'],
            );
            await tapVisible(
              tester,
              find.byKey(Key('action-${step["action"]}')),
            );
            if (action['stepUp'] == true || action['confirm'] == true) {
              if (action['stepUp'] == true) {
                await tester.enterText(
                  find.byKey(const Key('action-challenge')),
                  'DEMO',
                );
              }
              await tester.tap(find.text('Confirm demo action'));
              await tester.pumpAndSettle();
            }
          }
          if (step['error'] != null) {
            expect(
              find.textContaining('${step["error"]}:'),
              findsAtLeastNWidgets(1),
            );
            expect(snapshot().revision, previous);
          } else {
            expect(snapshot().revision, previous + 1);
          }
          expect(tester.takeException(), isNull);
        }
      },
      timeout: const Timeout(Duration(minutes: 3)),
    );
  }
}
