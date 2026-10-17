import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/widgets/workspace_controls.dart';
import '../workspace/workspace_controller.dart';
import 'dashboard_screen.dart';

class DashboardWorkbench extends ConsumerWidget {
  const DashboardWorkbench({super.key});
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final state = ref.watch(workspaceProvider).asData?.value,
        definition = ref.watch(workspaceDefinitionProvider).asData?.value,
        scope = ref.watch(propertyScopeProvider);
    if (state == null || definition == null) {
      return const SizedBox.shrink();
    }
    if (['permission_denied', 'expired_session'].contains(state.scenario)) {
      return DemoPanel(
        child: Column(
          children: [
            Text(
              state.scenario == 'permission_denied'
                  ? 'Permission denied'
                  : 'Demo session expired',
            ),
            TextButton(
              onPressed: () => context.go('/demo-lab'),
              child: const Text('Review developer QA'),
            ),
          ],
        ),
      );
    }
    final projection = deriveWorkspace(definition, state),
        summary = projection.summary(propertyId: scope),
        records = state.records
            .where(
              (r) =>
                  scope == 'all' ||
                  projection.propertyOf({
                        'id': r.id,
                        'type': r.type,
                        'data': r.data,
                      }) ==
                      scope,
            )
            .toList(),
        upcoming = records
            .where(
              (r) =>
                  [
                    'viewing',
                    'booking',
                    'maintenance',
                    'appointment',
                    'preventive',
                    'loi',
                  ].contains(r.type) &&
                  ![
                    'COMPLETED',
                    'CANCELLED',
                    'CLOSED',
                    'CHECKED_OUT',
                    'DECLINED',
                    'ACCEPTED',
                    'RESOLVED',
                    'REVOKED',
                    'NO_SHOW',
                  ].contains(r.status),
            )
            .toList();
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const SizedBox(height: 16),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            FilledButton(
              onPressed: () =>
                  context.push('/workspace/properties/property/new'),
              child: const Text('Add fictional property'),
            ),
            TextButton(
              onPressed: () => context.push('/workspace/listings/listing/new'),
              child: const Text('Create listing draft'),
            ),
            TextButton(
              onPressed: () =>
                  context.push('/workspace/maintenance/maintenance/new'),
              child: const Text('Report maintenance'),
            ),
          ],
        ),
        const SizedBox(height: 16),
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const WorkspaceHeading('Owner operations & alerts'),
              Text(
                '${summary["bookings"]} active sample bookings · ${summary["maintenanceOpen"]} open maintenance requests',
              ),
              TextButton(
                onPressed: () => context.go('/workspace/reports'),
                child: const Text('Open portfolio reports'),
              ),
              const SizedBox(height: 12),
              ...upcoming
                  .take(6)
                  .map(
                    (r) => ListTile(
                      contentPadding: EdgeInsets.zero,
                      title: Text(r.data['title']),
                      subtitle: Text(
                        '${r.data["date"] ?? r.data["start"] ?? "Date pending"} · ${r.status}',
                      ),
                      trailing: const Icon(Icons.chevron_right),
                      onTap: () =>
                          context.push(recordPath(definition, r.type, r.id)),
                    ),
                  ),
              TextButton(
                onPressed: () async {
                  final date = await showDatePicker(
                    context: context,
                    initialDate: DateTime(2026, 10, 17),
                    firstDate: DateTime(2020),
                    lastDate: DateTime(2040),
                  );
                  if (date != null && context.mounted) {
                    final matching = upcoming
                        .where((r) => isDemoCalendarDate(r, date))
                        .toList();
                    showModalBottomSheet(
                      context: context,
                      useSafeArea: true,
                      builder: (context) => ListView(
                        padding: const EdgeInsets.all(16),
                        children: [
                          WorkspaceHeading(
                            'Owner calendar · ${date.toIso8601String().substring(0, 10)}',
                          ),
                          if (matching.isEmpty)
                            const Text(
                              'No events for this demonstration date.',
                            ),
                          ...matching.map(
                            (r) => ListTile(
                              title: Text(r.data['title']),
                              subtitle: Text(
                                '${r.status}${r.data["unitId"] == null ? "" : " · ${r.data["unitId"]}"}',
                              ),
                              onTap: () {
                                Navigator.pop(context);
                                context.push(
                                  recordPath(definition, r.type, r.id),
                                );
                              },
                            ),
                          ),
                        ],
                      ),
                    );
                  }
                },
                child: const Text('Open owner calendar'),
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const WorkspaceHeading('Portfolio activity feed'),
              ...state.activity
                  .where((a) => scope == 'all' || a['propertyId'] == scope)
                  .take(10)
                  .map(
                    (a) => TextButton(
                      onPressed: () => context.push(
                        recordPath(definition, a['type'], a['recordId']),
                      ),
                      child: Text('${a["date"]} · ${a["message"]}'),
                    ),
                  ),
              if (state.activity.isEmpty)
                const Text(
                  'No local changes yet. Connected activity appears as workflows change.',
                ),
            ],
          ),
        ),
      ],
    );
  }
}
