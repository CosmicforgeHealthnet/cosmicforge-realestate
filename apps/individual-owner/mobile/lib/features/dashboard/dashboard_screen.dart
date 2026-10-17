import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/dashboard.dart';
import '../../core/widgets/async_states.dart';
import 'dashboard_controller.dart';
import 'dashboard_workbench.dart';
import 'dashboard_quick_access.dart';
import '../workspace/workspace_controller.dart';
import '../../core/widgets/workspace_controls.dart';

String demoMoney(int minor) {
  final digits = (minor ~/ 100).toString().replaceAllMapped(
    RegExp(r'\B(?=(\d{3})+(?!\d))'),
    (_) => ',',
  );
  return 'NGN $digits';
}

class DashboardScreen extends ConsumerStatefulWidget {
  const DashboardScreen({super.key});
  @override
  ConsumerState<DashboardScreen> createState() => _DashboardScreenState();
}

class _DashboardScreenState extends ConsumerState<DashboardScreen> {
  DemoScenario _scenario = DemoScenario.populated;
  String _kind = 'All';
  String? _actionError;
  bool _busy = false;
  @override
  void initState() {
    super.initState();
    _scenario = ref.read(dashboardProvider.notifier).currentScenario;
  }

  @override
  Widget build(BuildContext context) {
    final dashboard = ref.watch(dashboardProvider);
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        Container(
          padding: const EdgeInsets.fromLTRB(22, 27, 22, 23),
          decoration: BoxDecoration(
            gradient: const LinearGradient(
              begin: Alignment.topLeft,
              end: Alignment.bottomRight,
              colors: [Color(0xFF102549), Color(0xFF1D4383), Color(0xFF2865DA)],
            ),
            borderRadius: BorderRadius.circular(23),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Text(
                'COSMICFORGE / INDIVIDUAL OWNER',
                style: TextStyle(
                  color: Color(0xFFB9D6FF),
                  fontFamily: 'Inter',
                  fontSize: 10,
                  letterSpacing: 1.25,
                  fontWeight: FontWeight.w700,
                ),
              ),
              const SizedBox(height: 17),
              const Text(
                'Welcome back, Alex.',
                style: TextStyle(
                  color: Colors.white,
                  fontFamily: 'Inter',
                  fontSize: 30,
                  letterSpacing: -1.15,
                  fontWeight: FontWeight.w800,
                  height: 1.13,
                ),
              ),
              const SizedBox(height: 10),
              const Text(
                'Your properties, finances and next decisions — all in one place.',
                style: TextStyle(
                  color: Color(0xFFDEEBFF),
                  fontFamily: 'Inter',
                  fontSize: 13,
                  height: 1.6,
                ),
              ),
              const SizedBox(height: 23),
              Wrap(
                spacing: 10,
                runSpacing: 9,
                children: [
                  FilledButton.icon(
                    onPressed: () => context.push('/workspace/properties'),
                    style: FilledButton.styleFrom(
                      backgroundColor: Colors.white,
                      foregroundColor: const Color(0xFF153B7A),
                    ),
                    icon: const Icon(Icons.apartment_outlined, size: 18),
                    label: const Text('Manage portfolio'),
                  ),
                  OutlinedButton.icon(
                    onPressed: () => context.push('/workspace/properties/property/new'),
                    style: OutlinedButton.styleFrom(
                      foregroundColor: Colors.white,
                      side: const BorderSide(color: Color(0xFF91B3F1)),
                    ),
                    icon: const Icon(Icons.add_rounded, size: 18),
                    label: const Text('Add property'),
                  ),
                ],
              ),
            ],
          ),
        ),
        const SizedBox(height: 15),
        ExpansionTile(
          title: const Text('Developer QA scenarios'),
          children: [
            DropdownButtonFormField<DemoScenario>(
              initialValue: _scenario,
              decoration: const InputDecoration(labelText: 'Demo scenario'),
              items: const [
                DropdownMenuItem(
                  value: DemoScenario.populated,
                  child: Text('Sample portfolio'),
                ),
                DropdownMenuItem(
                  value: DemoScenario.empty,
                  child: Text('Empty portfolio'),
                ),
                DropdownMenuItem(
                  value: DemoScenario.error,
                  child: Text('Repository error'),
                ),
              ],
              onChanged: (value) {
                if (value == null) return;
                ref.read(propertyScopeProvider.notifier).select('all');
                setState(() => _scenario = value);
                ref
                    .read(workspaceProvider.notifier)
                    .scenario(
                      value == DemoScenario.empty
                          ? 'empty'
                          : value == DemoScenario.error
                          ? 'failed_operation'
                          : 'normal',
                    )
                    .then(
                      (_) =>
                          ref.read(dashboardProvider.notifier).scenario(value),
                    );
              },
            ),
          ],
        ),
        const SizedBox(height: 16),
        const DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Icon(Icons.shield_outlined),
              SizedBox(height: 8),
              Text(
                'Mock mode · Fictional records',
                style: TextStyle(fontWeight: FontWeight.bold),
              ),
              SizedBox(height: 8),
              Text(
                'Verification is pending. Financial and provider services are unavailable. Local changes reset when the app restarts.',
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        const DashboardQuickAccess(),
        const SizedBox(height: 17),
        dashboard.when(
          loading: () => const LoadingView(),
          error: (e, _) => ErrorView(
            message: e.toString(),
            retry: () =>
                ref.read(dashboardProvider.notifier).scenario(_scenario),
          ),
          data: (data) => _content(context, data),
        ),
        const SizedBox(height: 24),
        const Text(
          'Interactive mock · No backend connected',
          textAlign: TextAlign.center,
        ),
      ],
    );
  }

  Widget _content(BuildContext context, Dashboard data) {
    final scenario = ref.watch(workspaceProvider).asData?.value.scenario;
    if (['permission_denied', 'expired_session'].contains(scenario)) {
      return DemoPanel(
        child: WorkspaceHeading(
          scenario == 'permission_denied'
              ? 'Permission denied'
              : 'Demo session expired',
          subtitle: 'No protected property or task details are displayed.',
        ),
      );
    }
    final scope = ref.watch(propertyScopeProvider);
    data = Dashboard(
      workspaceName: data.workspaceName,
      properties: data.properties
          .where((p) => scope == 'all' || p.id == scope)
          .toList(),
      tasks: data.tasks
          .where((t) => scope == 'all' || t.propertyId == scope)
          .toList(),
      balances: data.balances,
    );
    final properties = data.properties
        .where((p) => _kind == 'All' || p.kind == _kind)
        .toList();
    final tasks = data.tasks.where((t) => !t.done).length;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        LayoutBuilder(
          builder: (context, constraints) {
            final width = (constraints.maxWidth - 12) / 2;
            return Wrap(
              spacing: 12,
              runSpacing: 12,
              children: [
                _metric(
                  width,
                  'Portfolio properties',
                  '${data.properties.length}',
                  Icons.apartment,
                ),
                _metric(
                  width,
                  'Portfolio units',
                  '${data.properties.fold<int>(0, (n, p) => n + p.units)}',
                  Icons.layers_outlined,
                ),
                _metric(
                  width,
                  'Occupied units',
                  '${data.properties.fold<int>(0, (n, p) => n + p.occupied)}',
                  Icons.key_outlined,
                ),
                _metric(width, 'Open demo tasks', '$tasks', Icons.checklist),
              ],
            );
          },
        ),
        const SizedBox(height: 16),
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(
                'Finance overview',
                style: Theme.of(context).textTheme.titleLarge,
              ),
              const SizedBox(height: 4),
              const Text(
                'DEMONSTRATION LEDGER',
                style: TextStyle(fontSize: 10),
              ),
              const SizedBox(height: 16),
              ...data.balances.map(
                (balance) => Padding(
                  padding: const EdgeInsets.only(bottom: 16),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(balance.label),
                      Text(
                        demoMoney(balance.amountMinor),
                        style: Theme.of(context).textTheme.titleLarge?.copyWith(
                          fontWeight: FontWeight.bold,
                        ),
                      ),
                      Text(
                        balance.description,
                        style: Theme.of(context).textTheme.bodySmall,
                      ),
                    ],
                  ),
                ),
              ),
              const Text(
                'Fixed sample · 17 Oct 2026, 09:00 UTC',
                style: TextStyle(fontSize: 11),
              ),
              const SizedBox(height: 8),
              const FilledButton(
                onPressed: null,
                child: Text('Payout unavailable'),
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(
                data.workspaceName,
                style: Theme.of(context).textTheme.labelSmall,
              ),
              const SizedBox(height: 6),
              Text(
                'Portfolio properties',
                style: Theme.of(context).textTheme.titleLarge,
              ),
              const SizedBox(height: 12),
              Wrap(
                spacing: 6,
                children:
                    ['All', 'Residential', 'Commercial', 'Short-let', 'Land']
                        .map(
                          (kind) => FilterChip(
                            label: Text(kind),
                            selected: _kind == kind,
                            onSelected: (_) => setState(() => _kind = kind),
                          ),
                        )
                        .toList(),
              ),
              if (properties.isEmpty)
                const EmptyView(
                  title: 'No sample properties here',
                  message:
                      'Choose another type, or switch to Sample portfolio. Create a fictional property to begin.',
                ),
              ...properties.map(
                (property) => Padding(
                  padding: const EdgeInsets.only(top: 10),
                  child: Material(
                    color: Theme.of(context).colorScheme.surfaceContainerLow,
                    borderRadius: BorderRadius.circular(16),
                    child: InkWell(
                      borderRadius: BorderRadius.circular(16),
                      onTap: () => context.push('/properties/${property.id}'),
                      child: Container(
                        padding: const EdgeInsets.all(11),
                        decoration: BoxDecoration(
                          border: Border.all(color: Theme.of(context).colorScheme.outlineVariant),
                          borderRadius: BorderRadius.circular(16),
                        ),
                        child: Row(
                          children: [
                            Container(
                              width: 75,
                              height: 80,
                              decoration: BoxDecoration(
                                gradient: LinearGradient(
                                  colors: [
                                    Theme.of(context).colorScheme.primary.withValues(alpha: .24),
                                    Theme.of(context).colorScheme.primary.withValues(alpha: .06),
                                  ],
                                ),
                                borderRadius: BorderRadius.circular(12),
                              ),
                              child: Icon(
                                property.kind == 'Land'
                                    ? Icons.landscape_outlined
                                    : property.kind == 'Short-let'
                                    ? Icons.villa_outlined
                                    : property.kind == 'Commercial'
                                    ? Icons.business_outlined
                                    : Icons.apartment_outlined,
                                size: 34,
                                color: Theme.of(context).colorScheme.primary,
                              ),
                            ),
                            const SizedBox(width: 12),
                            Expanded(
                              child: Column(
                                crossAxisAlignment: CrossAxisAlignment.start,
                                children: [
                                  Text(
                                    property.kind.toUpperCase(),
                                    style: Theme.of(context).textTheme.labelSmall?.copyWith(
                                      color: Theme.of(context).colorScheme.primary,
                                      letterSpacing: .75,
                                    ),
                                  ),
                                  const SizedBox(height: 6),
                                  Text(
                                    property.name,
                                    style: Theme.of(context).textTheme.titleSmall,
                                  ),
                                  const SizedBox(height: 5),
                                  Text(
                                    '${property.units} units · ${property.verification}',
                                    style: Theme.of(context).textTheme.bodySmall,
                                  ),
                                ],
                              ),
                            ),
                            Icon(
                              Icons.arrow_outward_rounded,
                              color: Theme.of(context).colorScheme.primary,
                              size: 19,
                            ),
                          ],
                        ),
                      ),
                    ),
                  ),
                ),
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(
                'Needs your attention',
                style: Theme.of(context).textTheme.titleLarge,
              ),
              const SizedBox(height: 8),
              const Text('Checking a task updates this demo session only.'),
              if (_actionError != null)
                Text(
                  _actionError!,
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              if (data.tasks.isEmpty) const Text('No demo tasks yet.'),
              ...data.tasks.map(
                (task) => CheckboxListTile(
                  contentPadding: EdgeInsets.zero,
                  title: Text(task.title),
                  subtitle: Text(
                    '${task.category} · ${task.done ? "Complete in demo" : "Open in demo"}',
                  ),
                  value: task.done,
                  onChanged: _busy
                      ? null
                      : (value) async {
                          setState(() {
                            _busy = true;
                            _actionError = null;
                          });
                          try {
                            await ref
                                .read(dashboardProvider.notifier)
                                .setTaskDone(task.id, value ?? false);
                          } catch (error) {
                            if (mounted) {
                              setState(
                                () => _actionError =
                                    'Local update failed. Try again.',
                              );
                            }
                          } finally {
                            if (mounted) setState(() => _busy = false);
                          }
                        },
                ),
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        const DashboardWorkbench(),
        TextButton(
          onPressed: () => context.go('/more'),
          child: const Text('Browse all 30 product areas →'),
        ),
      ],
    );
  }

  Widget _metric(double width, String label, String value, IconData icon) =>
      SizedBox(
        width: width,
        child: InkWell(
          onTap: () => context.push(
            label.contains('tasks')
                ? '/workspace/maintenance'
                : label.contains('properties')
                ? '/workspace/properties'
                : '/workspace/properties?tab=unit',
          ),
          child: DemoPanel(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(icon, color: Theme.of(context).colorScheme.primary),
                const SizedBox(height: 12),
                Text(
                  value,
                  style: Theme.of(context).textTheme.headlineMedium?.copyWith(
                    fontWeight: FontWeight.bold,
                  ),
                ),
                Text(label, style: Theme.of(context).textTheme.bodySmall),
              ],
            ),
          ),
        ),
      );
}

class DemoPanel extends StatelessWidget {
  const DemoPanel({required this.child, super.key});
  final Widget child;
  @override
  Widget build(BuildContext context) => Card(
    child: Padding(padding: const EdgeInsets.all(20), child: child),
  );
}
