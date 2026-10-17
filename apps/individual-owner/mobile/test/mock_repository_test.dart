import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:cosmicforge_owner/core/mock/mock_owner_repository.dart';
import 'package:cosmicforge_owner/core/models/dashboard.dart';

void main() {
  MockOwnerRepository repository() => MockOwnerRepository(
    load: () => File('assets/dashboard.json').readAsString(),
  );
  test(
    'deterministic isolated fixtures, immutable snapshots and local mutation',
    () async {
      final a = repository(), b = repository();
      final snapshot = await a.getDashboard(DemoScenario.populated);
      expect(snapshot.properties.length, 3);
      expect(() => snapshot.properties.clear(), throwsUnsupportedError);
      await a.setTaskDone('demo-task-01', true);
      expect(
        (await a.getDashboard(DemoScenario.populated)).tasks.first.done,
        isTrue,
      );
      expect(snapshot.tasks.first.done, isFalse);
      expect(
        (await b.getDashboard(DemoScenario.populated)).tasks.first.done,
        isFalse,
      );
    },
  );
  test('empty/error recovery and mutation guards', () async {
    final repo = repository();
    await repo.renameWorkspace('Sample workspace');
    final empty = await repo.getDashboard(DemoScenario.empty);
    expect(empty.properties, isEmpty);
    expect(empty.balances.every((b) => b.amountMinor == 0), isTrue);
    await expectLater(repo.getDashboard(DemoScenario.error), throwsStateError);
    expect(
      (await repo.getDashboard(DemoScenario.populated)).workspaceName,
      'Sample workspace',
    );
    await expectLater(repo.renameWorkspace(' '), throwsArgumentError);
    await expectLater(repo.setTaskDone('missing', true), throwsArgumentError);
  });
}
