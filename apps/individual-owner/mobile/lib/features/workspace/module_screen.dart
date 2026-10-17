import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/widgets/async_states.dart';
import '../dashboard/dashboard_controller.dart';
import '../dashboard/dashboard_screen.dart';

final modulesProvider = FutureProvider<List<Map<String, dynamic>>>(
  (ref) async =>
      (jsonDecode(await rootBundle.loadString('assets/modules.json')) as List)
          .cast<Map<String, dynamic>>(),
);

class MoreScreen extends ConsumerStatefulWidget {
  const MoreScreen({super.key});
  @override
  ConsumerState<MoreScreen> createState() => _MoreScreenState();
}

class _MoreScreenState extends ConsumerState<MoreScreen> {
  String _search = '';
  @override
  Widget build(BuildContext context) => ListView(
    padding: const EdgeInsets.all(16),
    children: [
      Text(
        'All product areas',
        style: Theme.of(context).textTheme.headlineMedium,
      ),
      const SizedBox(height: 8),
      const Text(
        '30 planned modules. Dashboard foundation and local preferences are interactive.',
      ),
      const SizedBox(height: 16),
      TextField(
        decoration: const InputDecoration(
          labelText: 'Find a product area',
          prefixIcon: Icon(Icons.search),
        ),
        onChanged: (value) => setState(() => _search = value),
      ),
      const SizedBox(height: 16),
      ref
          .watch(modulesProvider)
          .when(
            loading: () => const LoadingView(),
            error: (e, _) => ErrorView(message: e.toString()),
            data: (modules) {
              final filtered = modules
                  .where(
                    (m) => (m['title'] as String).toLowerCase().contains(
                      _search.toLowerCase(),
                    ),
                  )
                  .toList();
              return Column(
                children: [
                  if (filtered.isEmpty) const Text('No product areas match.'),
                  ...filtered.map(
                    (m) => Card(
                      margin: const EdgeInsets.only(bottom: 12),
                      child: ListTile(
                        title: Text(m['title']),
                        subtitle: Text(
                          'Area ${m["id"]} · ${m["id"] == "01" ? "IN_PROGRESS" : "NOT_STARTED"}',
                        ),
                        trailing: const Icon(Icons.chevron_right),
                        onTap: () => m['slug'] == 'dashboard'
                            ? context.go('/')
                            : context.push('/workspace/${m["slug"]}'),
                      ),
                    ),
                  ),
                ],
              );
            },
          ),
    ],
  );
}

class ModuleScreen extends ConsumerWidget {
  const ModuleScreen({required this.slug, super.key});
  final String slug;
  @override
  Widget build(BuildContext context, WidgetRef ref) => slug == 'dashboard'
      ? const DashboardScreen()
      : ref
            .watch(modulesProvider)
            .when(
              loading: () => const LoadingView(),
              error: (e, _) => ErrorView(message: e.toString()),
              data: (modules) {
                final matches = modules.where((m) => m['slug'] == slug);
                if (matches.isEmpty) {
                  return const EmptyView(
                    title: 'Page not found',
                    message: 'This route is not part of the foundation.',
                  );
                }
                final module = matches.first;
                return ListView(
                  padding: const EdgeInsets.all(16),
                  children: [
                    Text(
                      module['title'],
                      style: Theme.of(context).textTheme.headlineMedium,
                    ),
                    const SizedBox(height: 16),
                    DemoPanel(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          const Text(
                            'NOT_STARTED',
                            style: TextStyle(fontWeight: FontWeight.bold),
                          ),
                          const SizedBox(height: 12),
                          Text(
                            module['gate'] == 'C'
                                ? 'External capability unavailable'
                                : 'Workflow implementation pending',
                          ),
                          const SizedBox(height: 12),
                          const Text(
                            'This route establishes navigation only. No live payments, verification, signatures, messaging or device commands.',
                          ),
                          const SizedBox(height: 12),
                          Text(
                            (module['pairIds'] as List).isEmpty
                                ? 'Supporting design screens pending.'
                                : 'Visual references: ${(module["pairIds"] as List).join(", ")}',
                          ),
                        ],
                      ),
                    ),
                    if (slug == 'settings')
                      const Padding(
                        padding: EdgeInsets.only(top: 16),
                        child: WorkspacePreference(),
                      ),
                    if (slug == 'properties')
                      TextButton(
                        onPressed: () =>
                            context.push('/properties/demo-property-01'),
                        child: const Text('View sample property overview'),
                      ),
                  ],
                );
              },
            );
}

class WorkspacePreference extends ConsumerStatefulWidget {
  const WorkspacePreference({super.key});
  @override
  ConsumerState<WorkspacePreference> createState() =>
      _WorkspacePreferenceState();
}

class _WorkspacePreferenceState extends ConsumerState<WorkspacePreference> {
  final _form = GlobalKey<FormState>();
  final _name = TextEditingController();
  String _message = '';
  bool _saving = false;
  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => DemoPanel(
    child: Form(
      key: _form,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            'Demo workspace preference',
            style: Theme.of(context).textTheme.titleLarge,
          ),
          const SizedBox(height: 8),
          Text(
            'Current label: ${ref.watch(dashboardProvider).asData?.value.workspaceName ?? "Loading…"}',
          ),
          const SizedBox(height: 16),
          TextFormField(
            controller: _name,
            maxLength: 60,
            decoration: const InputDecoration(
              labelText: 'Workspace label',
              helperText: '2–60 characters. Use fictional information.',
            ),
            validator: (value) {
              final length = value?.trim().length ?? 0;
              return length < 2 || length > 60 ? 'Use 2–60 characters.' : null;
            },
          ),
          const SizedBox(height: 8),
          FilledButton(
            onPressed: _saving
                ? null
                : () async {
                    if (!_form.currentState!.validate()) return;
                    setState(() => _saving = true);
                    try {
                      await ref
                          .read(dashboardProvider.notifier)
                          .rename(_name.text);
                      if (mounted) {
                        setState(
                          () => _message = 'Demo preference saved locally.',
                        );
                      }
                    } catch (error) {
                      if (mounted) {
                        setState(
                          () => _message = 'Local update failed. Try again.',
                        );
                      }
                    } finally {
                      if (mounted) setState(() => _saving = false);
                    }
                  },
            child: const Text('Save demo preference'),
          ),
          Semantics(liveRegion: true, child: Text(_message)),
        ],
      ),
    ),
  );
}
