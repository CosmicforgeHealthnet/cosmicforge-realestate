import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:cosmicforge_owner/core/mock/workspace_engine.dart';
import 'package:cosmicforge_owner/core/models/workspace.dart';

Json readJson(String name) =>
    jsonDecode(File('assets/$name.json').readAsStringSync()) as Json;
dynamic at(dynamic value, String path) =>
    path.split('.').fold<dynamic>(value, (v, k) => v[k]);
void main() {
  final schema = readJson('workflows'), seed = readJson('workspace-seed');
  final stories = [
    ...jsonDecode(File('../shared/journeys.json').readAsStringSync()) as List,
    ...jsonDecode(
          File('../shared/supplementary-journeys.json').readAsStringSync(),
        )
        as List,
  ];
  for (final story in stories) {
    test('${story["id"]}: ${story["title"]}', () {
      final engine = WorkspaceEngine(schema, seed),
          aliases = <String, String>{};
      dynamic resolve(dynamic value) =>
          value is String && value.startsWith(r'$')
          ? aliases[value.substring(1)]
          : value;
      for (final raw in story['steps']) {
        final step = Json.from(raw);
        if (step['op'] == 'scenario') {
          engine.scenario(step['value']);
          continue;
        }
        if (step['op'] == 'actor') {
          engine.actor(step['value']);
          continue;
        }
        if (step['op'] == 'alias') {
          aliases[step['alias']] = step['id'];
          continue;
        }
        if (step['op'] == 'bind') {
          final rows = engine.records.where(
            (r) =>
                r['type'] == step['type'] &&
                r['data'][step['key']] == resolve(step['value']),
          );
          expect(rows, isNotEmpty);
          aliases[step['alias']] = rows.last['id'];
          continue;
        }
        if (step['op'] == 'assert') {
          expect(
            at(engine.record(aliases[step['target']]!), step['key']),
            resolve(step['value']),
          );
          continue;
        }
        if (step['op'] == 'summary') {
          expect(at(engine.summary(), step['key']), step['value']);
          continue;
        }
        final target = step['target'] != null
            ? engine.record(aliases[step['target']]!)
            : null;
        final command = WorkspaceCommand(
          op: step['op'],
          type: target?['type'] ?? step['type'],
          id: target?['id'],
          version: target?['version'],
          data: Json.from(
            step['data'] ?? {},
          ).map((k, v) => MapEntry(k, resolve(v))),
          action: step['action'],
          confirmed: true,
          challenge: 'DEMO',
        );
        final revision = engine.snapshot().revision;
        if (step['error'] != null) {
          expect(
            () => engine.execute(command),
            throwsA(
              isA<MockFault>().having((e) => e.code, 'code', step['error']),
            ),
          );
          expect(engine.snapshot().revision, revision);
        } else {
          final row = engine.execute(command);
          if (step['alias'] != null) {
            aliases[step['alias']] = row.id;
          }
        }
      }
    });
  }
  test('all seeded foreign references resolve', () {
    final engine = WorkspaceEngine(schema, seed);
    for (final row in engine.records) {
      for (final f in engine.spec(row['type'])['fields']) {
        if (f['kind'] == 'relation' &&
            row['data'][f['key']] != null &&
            row['data'][f['key']] != '') {
          expect(
            engine.record(row['data'][f['key']], f['target'])['id'],
            row['data'][f['key']],
          );
        }
      }
    }
  });
  test('faults roll back and provider activation is denied', () {
    final codes = {
      'restricted': 'RESTRICTED',
      'permission_denied': 'DENIED',
      'expired_session': 'EXPIRED_SESSION',
      'offline': 'OFFLINE',
      'provider_unavailable': 'UNAVAILABLE',
      'conflict': 'CONFLICT',
      'invalid_input': 'INVALID',
      'insufficient_delegation': 'DENIED',
      'failed_operation': 'FAILED',
    };
    for (final entry in codes.entries) {
      final engine = WorkspaceEngine(schema, seed)..scenario(entry.key);
      final before = engine.snapshot();
      expect(
        () => engine.execute(
          const WorkspaceCommand(
            op: 'update',
            type: 'property',
            id: 'demo-property-01',
            version: 1,
            data: {'notes': 'Changed'},
          ),
        ),
        throwsA(isA<MockFault>().having((e) => e.code, 'code', entry.value)),
      );
      expect(engine.snapshot().raw, before.raw);
    }
    final engine = WorkspaceEngine(schema, seed);
    expect(
      () => engine.execute(
        const WorkspaceCommand(
          op: 'create',
          type: 'financing',
          data: {'title': 'No lender'},
        ),
      ),
      throwsA(isA<MockFault>().having((e) => e.code, 'code', 'UNAVAILABLE')),
    );
  });
}
