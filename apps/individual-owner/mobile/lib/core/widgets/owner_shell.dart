import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../theme/owner_theme.dart';

class OwnerShell extends ConsumerWidget {
  const OwnerShell({required this.child, super.key});
  final Widget child;
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final location = GoRouterState.of(context).uri.path;
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
        : location.startsWith('/properties/')
        ? 1
        : 4;
    return Scaffold(
      appBar: AppBar(
        leading: location == '/'
            ? const Icon(Icons.apartment_rounded)
            : BackButton(
                onPressed: () =>
                    context.canPop() ? context.pop() : context.go('/'),
              ),
        title: const Text(
          'CosmicForge',
          style: TextStyle(fontSize: 18, fontWeight: FontWeight.w700),
        ),
        actions: [
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
