import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

/// Premium first-level navigation for all major owner tasks.
///
/// Every destination opens a real mock workspace. The complete 30-area catalogue
/// stays reachable through More; this widget is intentionally not a flat sitemap.
class DashboardQuickAccess extends StatelessWidget {
  const DashboardQuickAccess({super.key});

  static const _destinations = <_OwnerShortcut>[
    _OwnerShortcut('Properties', 'Manage your assets', '/workspace/properties', Icons.apartment_outlined),
    _OwnerShortcut('New listing', 'Publish a draft', '/workspace/listings/listing/new', Icons.add_home_work_outlined),
    _OwnerShortcut('Applications', 'Review prospects', '/workspace/applications', Icons.fact_check_outlined),
    _OwnerShortcut('Tenancies', 'Agreements & move-in', '/workspace/tenancies', Icons.assignment_outlined),
    _OwnerShortcut('Wallet', 'Example balances', '/workspace/financial', Icons.account_balance_wallet_outlined),
    _OwnerShortcut('Short-lets', 'Manage bookings', '/workspace/shortlets', Icons.hotel_outlined),
    _OwnerShortcut('Maintenance', 'Repairs & care', '/workspace/maintenance', Icons.handyman_outlined),
    _OwnerShortcut('Reports', 'Owner insights', '/workspace/reports', Icons.analytics_outlined),
  ];

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Card(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(17, 21, 17, 16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              'YOUR PROPERTY COMMAND CENTER',
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: scheme.primary,
                letterSpacing: 1.15,
                fontWeight: FontWeight.w800,
              ),
            ),
            const SizedBox(height: 7),
            Text(
              'Everything you need, one tap away.',
              style: Theme.of(context).textTheme.titleLarge,
            ),
            const SizedBox(height: 5),
            Text(
              'Act on your properties, tenants and finances without hunting through menus.',
              style: Theme.of(context).textTheme.bodySmall,
            ),
            const SizedBox(height: 18),
            LayoutBuilder(
              builder: (context, constraints) {
                final columns = constraints.maxWidth >= 650 ? 4 : 2;
                const spacing = 9.0;
                final width = (constraints.maxWidth - (columns - 1) * spacing) / columns;
                return Wrap(
                  spacing: spacing,
                  runSpacing: spacing,
                  children: _destinations.map((shortcut) {
                    return SizedBox(
                      width: width,
                      child: Material(
                        color: scheme.primary.withValues(alpha: 0.05),
                        borderRadius: BorderRadius.circular(14),
                        child: InkWell(
                          borderRadius: BorderRadius.circular(14),
                          onTap: () => context.push(shortcut.path),
                          child: Container(
                            constraints: const BoxConstraints(minHeight: 108),
                            padding: const EdgeInsets.all(12),
                            decoration: BoxDecoration(
                              borderRadius: BorderRadius.circular(14),
                              border: Border.all(color: scheme.outlineVariant.withValues(alpha: .72)),
                            ),
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.start,
                              children: [
                                Icon(shortcut.icon, color: scheme.primary, size: 22),
                                const SizedBox(height: 11),
                                Text(
                                  shortcut.title,
                                  style: Theme.of(context).textTheme.titleSmall,
                                ),
                                const SizedBox(height: 3),
                                Text(
                                  shortcut.subtitle,
                                  style: Theme.of(context).textTheme.labelSmall,
                                ),
                              ],
                            ),
                          ),
                        ),
                      ),
                    );
                  }).toList(),
                );
              },
            ),
            const SizedBox(height: 12),
            OutlinedButton.icon(
              onPressed: () => context.push('/more'),
              icon: const Icon(Icons.apps_outlined, size: 19),
              label: const Text('Explore all 30 workspaces'),
            ),
          ],
        ),
      ),
    );
  }
}

class _OwnerShortcut {
  const _OwnerShortcut(this.title, this.subtitle, this.path, this.icon);
  final String title, subtitle, path;
  final IconData icon;
}
