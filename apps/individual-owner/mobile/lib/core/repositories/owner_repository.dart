import '../models/dashboard.dart';
import '../models/workspace.dart';

abstract interface class OwnerRepository {
  Future<WorkspaceSnapshot> readWorkspace();
  Future<Json> readDefinition();
  Future<WorkspaceRecord> execute(WorkspaceCommand command);
  Future<void> setScenario(String scenario);
  Future<void> setActor(String actor);
  Future<void> resetDemo();
  Future<Dashboard> getDashboard(DemoScenario scenario);
  Future<void> setTaskDone(String id, bool done);
  Future<void> renameWorkspace(String name);
}
