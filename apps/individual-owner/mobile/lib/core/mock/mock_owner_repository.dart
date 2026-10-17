import 'dart:convert';
import 'package:flutter/services.dart';
import '../models/dashboard.dart';
import '../repositories/owner_repository.dart';
import '../models/workspace.dart';
import 'workspace_engine.dart';

class MockOwnerRepository implements OwnerRepository {
  MockOwnerRepository({
    Future<String> Function()? load,
    Future<String> Function()? loadDefinition,
    Future<String> Function()? loadUniverse,
  }) : _load = load ?? (() => rootBundle.loadString('assets/dashboard.json')),
       _loadDefinition =
           loadDefinition ??
           (() => rootBundle.loadString('assets/workflows.json')),
       _loadUniverse =
           loadUniverse ??
           (() => rootBundle.loadString('assets/workspace-seed.json'));
  final Future<String> Function() _load;
  final Future<String> Function() _loadDefinition, _loadUniverse;
  Future<WorkspaceEngine>? _workspace;
  Future<WorkspaceEngine> domain() =>
      _workspace ??= Future.wait([_loadDefinition(), _loadUniverse()]).then(
        (values) =>
            WorkspaceEngine(jsonDecode(values[0]), jsonDecode(values[1])),
      );
  @override
  Future<WorkspaceSnapshot> readWorkspace() async =>
      (await domain()).snapshot();
  @override
  Future<Json> readDefinition() async => (await domain()).schema;
  @override
  Future<WorkspaceRecord> execute(WorkspaceCommand command) async =>
      (await domain()).execute(command);
  @override
  Future<void> setScenario(String scenario) async =>
      (await domain()).scenario(scenario);
  @override
  Future<void> setActor(String actor) async => (await domain()).actor(actor);
  @override
  Future<void> resetDemo() async => (await domain()).reset();
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
    final base = await _read(),
        engine = await domain(),
        state = engine.snapshot(),
        summary = engine.summary();
    final rows = state.records;
    final data = Dashboard(
      workspaceName: state.raw['workspaceName'],
      properties: rows
          .where((r) => r.type == 'property' && r.status != 'ARCHIVED')
          .map(
            (r) => DemoProperty(
              id: r.id,
              name: r.data['title'],
              location: r.data['location'],
              kind: r.data['kind'],
              units: rows
                  .where(
                    (u) => u.type == 'unit' && u.data['propertyId'] == r.id,
                  )
                  .length,
              occupied: rows
                  .where(
                    (u) =>
                        u.type == 'unit' &&
                        u.data['propertyId'] == r.id &&
                        u.data['occupied'] == true,
                  )
                  .length,
              verification:
                  rows.any(
                    (v) =>
                        v.type == 'verification' &&
                        v.data['propertyId'] == r.id &&
                        v.status == 'DEMO_REVIEWED',
                  )
                  ? 'Demo review recorded'
                  : 'Pending review',
            ),
          )
          .toList(),
      tasks: (state.raw['tasks'] as List)
          .map((t) => DemoTask.fromJson(t))
          .toList(),
      balances: [
        for (var i = 0; i < base.balances.length; i++)
          DemoBalance(
            label: base.balances[i].label,
            amountMinor:
                summary['buckets'][[
                  'AVAILABLE',
                  'PENDING',
                  'HELD',
                  'RESTRICTED',
                ][i]],
            description: base.balances[i].description,
          ),
      ],
    );
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
    (await domain()).setTaskDone(id, done);
  }

  @override
  Future<void> renameWorkspace(String name) async {
    final clean = name.trim();
    if (clean.length < 2 || clean.length > 60) {
      throw ArgumentError('Use 2–60 characters.');
    }
    _state = (await _read()).copyWith(workspaceName: clean);
    (await domain()).renameWorkspace(clean);
  }
}
