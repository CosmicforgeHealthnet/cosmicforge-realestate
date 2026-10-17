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

IconData _areaIcon(String slug) => switch (slug) {
  'dashboard' => Icons.space_dashboard_outlined,
  'identity' => Icons.verified_user_outlined,
  'financial' || 'rent' => Icons.account_balance_wallet_outlined,
  'ownership' => Icons.gavel_outlined,
  'properties' => Icons.apartment_outlined,
  'verification' => Icons.fact_check_outlined,
  'listings' => Icons.home_work_outlined,
  'pricing' => Icons.price_change_outlined,
  'availability' => Icons.event_available_outlined,
  'inquiries' => Icons.mark_email_unread_outlined,
  'viewings' => Icons.calendar_month_outlined,
  'applications' => Icons.people_outline,
  'tenancies' => Icons.description_outlined,
  'renewals' => Icons.autorenew_outlined,
  'utilities' => Icons.electric_bolt_outlined,
  'commercial' => Icons.business_outlined,
  'land' => Icons.landscape_outlined,
  'sales' => Icons.real_estate_agent_outlined,
  'shortlets' => Icons.bed_outlined,
  'managers' => Icons.admin_panel_settings_outlined,
  'maintenance' => Icons.handyman_outlined,
  'professionals' => Icons.engineering_outlined,
  'notifications' => Icons.notifications_none_outlined,
  'reports' => Icons.analytics_outlined,
  'safety' => Icons.shield_outlined,
  'settings' => Icons.tune_outlined,
  'financing' => Icons.account_balance_outlined,
  'insurance' => Icons.health_and_safety_outlined,
  'access' => Icons.key_outlined,
  _ => Icons.grid_view_rounded,
};

class MoreScreen extends ConsumerStatefulWidget {
  const MoreScreen({super.key});
  @override
  ConsumerState<MoreScreen> createState() => _MoreScreenState();
}

class _MoreScreenState extends ConsumerState<MoreScreen> {
  String _search = '';

  List<Widget> _section(
    BuildContext context,
    List<Map<String, dynamic>> modules,
    String heading,
    int first,
    int last,
  ) {
    final items = modules.where((m) {
      final number = int.tryParse(m['id'].toString()) ?? 0;
      return number >= first && number <= last;
    }).toList();
    if (items.isEmpty) return [];
    final scheme = Theme.of(context).colorScheme;
    return [
      Padding(
        padding: const EdgeInsets.fromLTRB(2, 22, 2, 13),
        child: Row(
          children: [
            Text(
              heading.toUpperCase(),
              style: Theme.of(context).textTheme.labelSmall?.copyWith(
                fontWeight: FontWeight.w800,
                letterSpacing: 1.2,
                color: scheme.primary,
              ),
            ),
            const SizedBox(width: 12),
            Expanded(child: Divider(color: scheme.outlineVariant)),
          ],
        ),
      ),
      ...items.map((m) => Padding(
        padding: const EdgeInsets.only(bottom: 11),
        child: Card(
          child: ListTile(
            minVerticalPadding: 15,
            leading: Container(
              width: 45,
              height: 45,
              decoration: BoxDecoration(
                color: scheme.primary.withValues(alpha: .1),
                borderRadius: BorderRadius.circular(13),
              ),
              child: Icon(_areaIcon(m['slug']), color: scheme.primary, size: 23),
            ),
            title: Text(
              m['title'],
              style: Theme.of(context).textTheme.titleSmall,
            ),
            subtitle: Padding(
              padding: const EdgeInsets.only(top: 5),
              child: Text('WORKSPACE  ${m["id"]}  ·  Interactive demo'),
            ),
            trailing: Icon(Icons.arrow_forward_ios_rounded, size: 16, color: scheme.primary),
            onTap: () => m['slug'] == 'dashboard'
                ? context.go('/')
                : context.push('/workspace/${m["slug"]}'),
          ),
        ),
      )),
    ];
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return ListView(
      padding: const EdgeInsets.fromLTRB(16, 17, 16, 30),
      children: [
        Container(
          padding: const EdgeInsets.all(24),
          decoration: BoxDecoration(
            gradient: const LinearGradient(
              begin: Alignment.topLeft,
              end: Alignment.bottomRight,
              colors: [Color(0xFF112B56), Color(0xFF2459E0)],
            ),
            borderRadius: BorderRadius.circular(22),
          ),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Container(
                padding: const EdgeInsets.all(10),
                decoration: BoxDecoration(
                  color: Colors.white.withValues(alpha: .13),
                  borderRadius: BorderRadius.circular(12),
                ),
                child: const Icon(Icons.apps_rounded, color: Colors.white, size: 25),
              ),
              const SizedBox(height: 16),
              const Text(
                'All product areas',
                style: TextStyle(
                  color: Colors.white,
                  fontFamily: 'Inter',
                  fontSize: 25,
                  fontWeight: FontWeight.w800,
                  letterSpacing: -.7,
                ),
              ),
              const SizedBox(height: 7),
              const Text(
                'A complete owner workspace, organized around your property journey.',
                style: TextStyle(color: Color(0xFFCFDDFC), fontSize: 13, height: 1.5),
              ),
              const SizedBox(height: 12),
              const Text(
                'INTERACTIVE MOCK · FICTIONAL DATA',
                style: TextStyle(
                  color: Color(0xFFCFDDFC),
                  fontSize: 10,
                  letterSpacing: 1.0,
                  fontWeight: FontWeight.w700,
                ),
              ),
            ],
          ),
        ),
        const SizedBox(height: 19),
        TextField(
          decoration: const InputDecoration(
            labelText: 'Find a product area',
            prefixIcon: Icon(Icons.search_rounded),
          ),
          onChanged: (value) => setState(() => _search = value),
        ),
        Align(
          alignment: Alignment.centerRight,
          child: TextButton.icon(
            onPressed: () => context.push('/demo-lab'),
            icon: const Icon(Icons.science_outlined, size: 18),
            label: const Text('Developer QA'),
          ),
        ),
        ref.watch(modulesProvider).when(
          loading: () => const LoadingView(),
          error: (e, _) => ErrorView(message: e.toString()),
          data: (modules) {
            final filtered = modules.where(
              (m) => (m['title'] as String).toLowerCase().contains(_search.toLowerCase()),
            ).toList();
            return Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [
                if (filtered.isEmpty)
                  Padding(
                    padding: const EdgeInsets.all(25),
                    child: Text(
                      'No product areas match.',
                      style: Theme.of(context).textTheme.bodyMedium,
                    ),
                  ),
                ..._section(context, filtered, 'Portfolio & identity', 1, 6),
                ..._section(context, filtered, 'Listings & tenancy', 7, 16),
                ..._section(context, filtered, 'Operations & partners', 17, 23),
                ..._section(context, filtered, 'Intelligence & settings', 24, 30),
              ],
            );
          },
        ),
        const SizedBox(height: 8),
        Text(
          'No backend or third-party services are connected.',
          textAlign: TextAlign.center,
          style: TextStyle(color: scheme.onSurfaceVariant, fontSize: 11),
        ),
      ],
    );
  }
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
