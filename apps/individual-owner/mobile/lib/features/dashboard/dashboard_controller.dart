import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/mock/mock_owner_repository.dart';
import '../../core/models/dashboard.dart';
import '../../core/repositories/owner_repository.dart';

final ownerRepositoryProvider = Provider<OwnerRepository>(
  (ref) => MockOwnerRepository(),
);
final dashboardProvider = AsyncNotifierProvider<DashboardController, Dashboard>(
  DashboardController.new,
);

class DashboardController extends AsyncNotifier<Dashboard> {
  DemoScenario _scenario = DemoScenario.populated;
  DemoScenario get currentScenario => _scenario;
  @override
  Future<Dashboard> build() =>
      ref.read(ownerRepositoryProvider).getDashboard(_scenario);
  Future<void> scenario(DemoScenario value) async {
    _scenario = value;
    state = const AsyncLoading();
    state = await AsyncValue.guard(
      () => ref.read(ownerRepositoryProvider).getDashboard(value),
    );
  }

  Future<void> setTaskDone(String id, bool done) async {
    final repository = ref.read(ownerRepositoryProvider);
    await repository.setTaskDone(id, done);
    state = AsyncData(await repository.getDashboard(_scenario));
  }

  Future<void> rename(String name) async {
    final repository = ref.read(ownerRepositoryProvider);
    await repository.renameWorkspace(name);
    state = AsyncData(await repository.getDashboard(_scenario));
  }
}
