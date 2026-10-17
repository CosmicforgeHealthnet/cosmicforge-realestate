import 'dart:convert';
import 'package:flutter/services.dart';
import '../models/dashboard.dart';
import '../repositories/owner_repository.dart';

class MockOwnerRepository implements OwnerRepository {
  MockOwnerRepository({Future<String> Function()? load})
    : _load = load ?? (() => rootBundle.loadString('assets/dashboard.json'));
  final Future<String> Function() _load;
  Future<Dashboard>? _initial;
  Dashboard? _state;
  Future<Dashboard> _read() async => _state ??= await (_initial ??= _load()
      .then((text) => Dashboard.fromJson(jsonDecode(text))));
  @override
  Future<Dashboard> getDashboard(DemoScenario scenario) async {
    if (scenario == DemoScenario.error) {
      throw StateError(
        'Simulated repository failure. Choose Sample portfolio to recover.',
      );
    }
    final data = await _read();
    if (scenario == DemoScenario.empty) {
      return Dashboard(
        workspaceName: data.workspaceName,
        properties: [],
        tasks: [],
        balances: data.balances
            .map(
              (b) => DemoBalance(
                label: b.label,
                amountMinor: 0,
                description: b.description,
              ),
            )
            .toList(),
      );
    }
    return data;
  }

  @override
  Future<void> setTaskDone(String id, bool done) async {
    final data = await _read();
    if (!data.tasks.any((t) => t.id == id)) {
      throw ArgumentError('Sample task not found.');
    }
    _state = data.copyWith(
      tasks: data.tasks.map((t) => t.id == id ? t.withDone(done) : t).toList(),
    );
  }

  @override
  Future<void> renameWorkspace(String name) async {
    final clean = name.trim();
    if (clean.length < 2 || clean.length > 60) {
      throw ArgumentError('Use 2–60 characters.');
    }
    _state = (await _read()).copyWith(workspaceName: clean);
  }
}
