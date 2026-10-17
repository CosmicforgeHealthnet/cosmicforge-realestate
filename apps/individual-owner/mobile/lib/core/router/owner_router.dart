import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../widgets/owner_shell.dart';
import '../widgets/async_states.dart';
import '../../features/dashboard/dashboard_screen.dart';
import '../../features/workspace/module_screen.dart';
import '../../features/properties/property_screen.dart';

final routerProvider = Provider<GoRouter>((ref) {
  final router = GoRouter(
    routes: [
      ShellRoute(
        builder: (context, state, child) => OwnerShell(child: child),
        routes: [
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
        message: 'This route is not part of the foundation.',
      ),
    ),
  );
  ref.onDispose(router.dispose);
  return router;
});
