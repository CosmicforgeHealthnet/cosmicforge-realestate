import '../models/dashboard.dart';

abstract interface class OwnerRepository {
  Future<Dashboard> getDashboard(DemoScenario scenario);
  Future<void> setTaskDone(String id, bool done);
  Future<void> renameWorkspace(String name);
}
