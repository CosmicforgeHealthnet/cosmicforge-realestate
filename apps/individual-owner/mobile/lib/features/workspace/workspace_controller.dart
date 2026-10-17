import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/workspace.dart';
import '../../core/mock/workspace_engine.dart';
import '../dashboard/dashboard_controller.dart';

final workspaceDefinitionProvider = FutureProvider<Json>(
  (ref) => ref.read(ownerRepositoryProvider).readDefinition(),
);
final workspaceProvider =
    AsyncNotifierProvider<WorkspaceController, WorkspaceSnapshot>(
      WorkspaceController.new,
    );

class WorkspaceController extends AsyncNotifier<WorkspaceSnapshot> {
  @override
  Future<WorkspaceSnapshot> build() =>
      ref.read(ownerRepositoryProvider).readWorkspace();
  Future<void> refresh() async {
    state = AsyncData(await ref.read(ownerRepositoryProvider).readWorkspace());
    ref.invalidate(dashboardProvider);
  }

  Future<WorkspaceRecord> execute(WorkspaceCommand command) async {
    final result = await ref.read(ownerRepositoryProvider).execute(command);
    await refresh();
    return result;
  }

  Future<void> scenario(String value) async {
    await ref.read(ownerRepositoryProvider).setScenario(value);
    await refresh();
  }

  Future<void> actor(String value) async {
    await ref.read(ownerRepositoryProvider).setActor(value);
    await refresh();
  }

  Future<void> reset() async {
    await ref.read(ownerRepositoryProvider).resetDemo();
    await refresh();
  }
}

String recordPath(Json definition, String type, String id) {
  final spec = (definition['types'] as List).firstWhere((s) => s['id'] == type);
  return '/workspace/${spec["module"]}/$type/$id';
}

WorkspaceEngine deriveWorkspace(Json definition, WorkspaceSnapshot state) =>
    WorkspaceEngine(definition, state.raw);
