import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/dashboard.dart';
import '../../core/widgets/async_states.dart';
import 'dashboard_controller.dart';

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
        Text(
          'YOUR PORTFOLIO, AT A GLANCE',
          style: Theme.of(context).textTheme.labelSmall,
        ),
        const SizedBox(height: 8),
        Text(
          'Welcome back, Alex.',
          style: Theme.of(
            context,
          ).textTheme.headlineMedium?.copyWith(fontWeight: FontWeight.bold),
        ),
        const SizedBox(height: 8),
        const Text('A clear view of your properties and what needs attention.'),
        const SizedBox(height: 20),
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
            setState(() => _scenario = value);
            ref.read(dashboardProvider.notifier).scenario(value);
          },
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
          'Foundation preview · No backend connected',
          textAlign: TextAlign.center,
        ),
      ],
    );
  }

  Widget _content(BuildContext context, Dashboard data) {
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
                children: ['All', 'Residential', 'Commercial', 'Short-let']
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
                      'Choose another type, or switch to Sample portfolio. Creation workflows follow in later sections.',
                ),
              ...properties.map(
                (property) => ListTile(
                  contentPadding: EdgeInsets.zero,
                  leading: Icon(
                    Icons.apartment_outlined,
                    color: Theme.of(context).colorScheme.primary,
                  ),
                  title: Text(property.name),
                  subtitle: Text(
                    '${property.kind} · ${property.units} units\n${property.verification}',
                  ),
                  isThreeLine: true,
                  trailing: const Icon(Icons.chevron_right),
                  onTap: () => context.push('/properties/${property.id}'),
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
