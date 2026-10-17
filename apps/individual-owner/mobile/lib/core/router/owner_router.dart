import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../widgets/owner_shell.dart';
import '../widgets/async_states.dart';
import '../../features/dashboard/dashboard_screen.dart';
import '../../features/workspace/module_screen.dart';
import '../../features/properties/property_screen.dart';
import '../../features/workspace/workspace_screen.dart';
import '../../features/workspace/demo_lab.dart';
import '../widgets/workspace_controls.dart';

final routerProvider = Provider<GoRouter>((ref) {
  final router = GoRouter(
    routes: [
      ShellRoute(
        builder: (context, state, child) => OwnerShell(child: child),
        routes: [
          GoRoute(
            path: '/demo-lab',
            builder: (context, state) => const DemoLab(),
          ),
          GoRoute(
            path: '/workspace/:module/:type/:id',
            onExit: (context, state) async {
              if (!ref.read(draftGuardProvider)) {
                return true;
              }
              final discard = await confirmDemoAction(
                context,
                'Discard draft?',
                message: 'Discard unsaved local form changes?',
              );
              if (discard) {
                ref.read(draftGuardProvider.notifier).mark(false);
              }
              return discard;
            },
            builder: (context, state) => WorkspaceScreen(
              key: ValueKey(state.uri.path),
              slug: state.pathParameters['module']!,
              type: state.pathParameters['type'],
              id: state.pathParameters['id'],
              property: state.uri.queryParameters['property'] ?? 'all',
            ),
          ),
          GoRoute(
            path: '/workspace/:module/:type/:id/edit',
            onExit: (context, state) async {
              if (!ref.read(draftGuardProvider)) {
                return true;
              }
              final discard = await confirmDemoAction(
                context,
                'Discard draft?',
                message: 'Discard unsaved local form changes?',
              );
              if (discard) {
                ref.read(draftGuardProvider.notifier).mark(false);
              }
              return discard;
            },
            builder: (context, state) => WorkspaceScreen(
              key: ValueKey(state.uri.path),
              slug: state.pathParameters['module']!,
              type: state.pathParameters['type'],
              id: state.pathParameters['id'],
              edit: true,
            ),
          ),
          GoRoute(
            path: '/',
            builder: (context, state) => const DashboardScreen(),
          ),
          GoRoute(
            path: '/more',
            builder: (context, state) => const MoreScreen(),
          ),
          GoRoute(
            path: '/workspace/:module',
            builder: (context, state) =>
                ModuleScreen(slug: state.pathParameters['module']!),
          ),
          GoRoute(
            path: '/properties/:id',
            builder: (context, state) =>
                PropertyScreen(id: state.pathParameters['id']!),
          ),
        ],
      ),
    ],
    errorBuilder: (context, state) => Scaffold(
      appBar: AppBar(title: const Text('CosmicForge')),
      body: const EmptyView(
        title: 'Page not found',
        message: 'This route is not part of the workspace.',
      ),
    ),
  );
  ref.onDispose(router.dispose);
  return router;
});
