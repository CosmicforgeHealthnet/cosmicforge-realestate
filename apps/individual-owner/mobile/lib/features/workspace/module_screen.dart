import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/widgets/async_states.dart';
import 'workspace_screen.dart';

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
        'Connected fictional workflows. Providers remain unavailable.',
      ),
      const SizedBox(height: 16),
      TextButton.icon(
        onPressed: () => context.push('/demo-lab'),
        icon: const Icon(Icons.science_outlined),
        label: const Text('Developer QA'),
      ),
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
                        subtitle: Text('Area ${m["id"]} · Mock workspace'),
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

class ModuleScreen extends StatelessWidget {
  const ModuleScreen({required this.slug, super.key});
  final String slug;
  @override
  Widget build(BuildContext context) => WorkspaceScreen(
    slug: slug,
    initialTab: GoRouterState.of(context).uri.queryParameters['tab'],
  );
}
