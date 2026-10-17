import 'package:go_router/go_router.dart';
import '../../features/dashboard/dashboard_controller.dart';
import 'workspace_controls.dart';
import '../../features/workspace/workspace_controller.dart';
import '../models/workspace.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../theme/owner_theme.dart';

class OwnerShell extends ConsumerWidget {
  const OwnerShell({required this.child, super.key});
  final Widget child;
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final location = GoRouterState.of(context).uri.path;
    final blocked = [
      'permission_denied',
      'expired_session',
    ].contains(ref.watch(workspaceProvider).asData?.value.scenario);
    const paths = [
      '/',
      '/workspace/properties',
      '/workspace/listings',
      '/workspace/financial',
      '/more',
    ];
    final exact = paths.indexOf(location);
    final index = exact >= 0
        ? exact
        : (location.startsWith('/properties/') ||
              location.startsWith('/workspace/properties/'))
        ? 1
        : location.startsWith('/workspace/listings/')
        ? 2
        : location.startsWith('/workspace/financial/')
        ? 3
        : 4;
    return Scaffold(
      appBar: AppBar(
        leading: location == '/'
            ? const Icon(Icons.apartment_rounded)
            : BackButton(
                onPressed: () => context.canPop()
                    ? context.pop()
                    : context.go(
                        location.startsWith('/workspace/') &&
                                location.split('/').length > 3
                            ? '/workspace/${location.split('/')[2]}'
                            : '/',
                      ),
              ),
        title: const Text(
          'CosmicForge',
          style: TextStyle(fontSize: 18, fontWeight: FontWeight.w700),
        ),
        actions: [
          IconButton(
            tooltip: 'Search workspace',
            icon: const Icon(Icons.search),
            onPressed: () {
              final definition = ref
                  .read(workspaceDefinitionProvider)
                  .asData
                  ?.value;
              final modules = (definition?['modules'] as List? ?? [])
                  .cast<Map<String, dynamic>>();
              showModalBottomSheet(
                context: context,
                isScrollControlled: true,
                useSafeArea: true,
                builder: (context) => _WorkspaceSearch(
                  modules,
                  ['permission_denied', 'expired_session'].contains(
                        ref.read(workspaceProvider).asData?.value.scenario,
                      )
                      ? []
                      : ref.read(workspaceProvider).asData?.value.records ?? [],
                ),
              );
            },
          ),
          PopupMenuButton<String>(
            tooltip: 'Account and portfolio',
            onSelected: (value) {
              if (value.startsWith('/')) {
                context.push(value);
              } else {
                ref.read(propertyScopeProvider.notifier).select(value);
              }
            },
            itemBuilder: (context) => [
              const PopupMenuItem(value: 'all', child: Text('All properties')),
              if (!blocked)
                ...?ref
                    .watch(dashboardProvider)
                    .asData
                    ?.value
                    .properties
                    .map(
                      (p) => PopupMenuItem(value: p.id, child: Text(p.name)),
                    ),
              const PopupMenuDivider(),
              const PopupMenuItem(
                value: '/workspace/notifications',
                child: Text('Notifications'),
              ),
              const PopupMenuItem(
                value: '/demo-lab',
                child: Text('Developer QA'),
              ),
              const PopupMenuItem(
                value: '/workspace/identity',
                child: Text('Profile & security'),
              ),
              const PopupMenuItem(
                value: '/workspace/settings',
                child: Text('Settings'),
              ),
            ],
          ),
          const Center(
            child: Text(
              'MOCK',
              style: TextStyle(fontSize: 10, fontWeight: FontWeight.bold),
            ),
          ),
          IconButton(
            tooltip: Theme.of(context).brightness == Brightness.dark
                ? 'Switch to light theme'
                : 'Switch to dark theme',
            icon: Icon(
              Theme.of(context).brightness == Brightness.dark
                  ? Icons.light_mode_outlined
                  : Icons.dark_mode_outlined,
            ),
            onPressed: () => ref
                .read(themeModeProvider.notifier)
                .toggle(Theme.of(context).brightness),
          ),
        ],
      ),
      body: SafeArea(child: child),
      bottomNavigationBar: NavigationBar(
        selectedIndex: index,
        onDestinationSelected: (i) => context.go(paths[i]),
        destinations: const [
          NavigationDestination(
            icon: Icon(Icons.dashboard_outlined),
            label: 'Home',
          ),
          NavigationDestination(
            icon: Icon(Icons.apartment_outlined),
            label: 'Properties',
          ),
          NavigationDestination(
            icon: Icon(Icons.storefront_outlined),
            label: 'Listings',
          ),
          NavigationDestination(
            icon: Icon(Icons.account_balance_wallet_outlined),
            label: 'Wallet',
          ),
          NavigationDestination(icon: Icon(Icons.more_horiz), label: 'More'),
        ],
      ),
    );
  }
}

class _WorkspaceSearch extends StatefulWidget {
  const _WorkspaceSearch(this.modules, this.records);
  final List<WorkspaceRecord> records;
  final List<Map<String, dynamic>> modules;
  @override
  State<_WorkspaceSearch> createState() => _WorkspaceSearchState();
}

class _WorkspaceSearchState extends State<_WorkspaceSearch> {
  String search = '';
  @override
  Widget build(BuildContext context) => Padding(
    padding: EdgeInsets.fromLTRB(
      16,
      24,
      16,
      MediaQuery.viewInsetsOf(context).bottom + 16,
    ),
    child: SizedBox(
      height: MediaQuery.sizeOf(context).height * .65,
      child: Column(
        children: [
          TextField(
            autofocus: true,
            decoration: const InputDecoration(labelText: 'Search workspace'),
            onChanged: (v) => setState(() => search = v),
          ),
          Expanded(
            child: ListView(
              children: [
                if (search.trim().isNotEmpty)
                  ...widget.records
                      .where(
                        (r) => r.data['title']
                            .toString()
                            .toLowerCase()
                            .contains(search.toLowerCase()),
                      )
                      .take(25)
                      .map(
                        (r) => ListTile(
                          title: Text(r.data['title']),
                          subtitle: Text(r.type),
                          onTap: () {
                            Navigator.pop(context);
                            context.push(
                              '/workspace/${widget.modules.firstWhere((m) => (m['types'] as List? ?? []).contains(r.type), orElse: () => {'slug': 'properties'})['slug']}/${r.type}/${r.id}',
                            );
                          },
                        ),
                      ),
                ...widget.modules
                    .where(
                      (m) => (m['title'] as String).toLowerCase().contains(
                        search.toLowerCase(),
                      ),
                    )
                    .map(
                      (m) => ListTile(
                        title: Text(m['title']),
                        onTap: () {
                          Navigator.pop(context);
                          context.push(
                            m['id'] == '01' ? '/' : '/workspace/${m["slug"]}',
                          );
                        },
                      ),
                    ),
              ],
            ),
          ),
        ],
      ),
    ),
  );
}
