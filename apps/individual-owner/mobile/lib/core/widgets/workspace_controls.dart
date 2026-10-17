import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../features/workspace/workspace_controller.dart';
import '../models/workspace.dart';

final draftGuardProvider = NotifierProvider<DraftGuard, bool>(DraftGuard.new);

class DraftGuard extends Notifier<bool> {
  @override
  bool build() => false;
  void mark(bool value) => state = value;
}

final propertyScopeProvider = NotifierProvider<PropertyScope, String>(
  PropertyScope.new,
);

class PropertyScope extends Notifier<String> {
  @override
  String build() {
    ref.listen(workspaceProvider, (_, next) {
      final records = next.asData?.value.records;
      if (records != null &&
          state != 'all' &&
          !records.any(
            (r) =>
                r.type == 'property' && r.id == state && r.status != 'ARCHIVED',
          )) {
        state = 'all';
      }
    });
    return 'all';
  }

  void select(String value) {
    final records = ref.read(workspaceProvider).asData?.value.records;
    state =
        value == 'all' ||
            records?.any(
                  (r) =>
                      r.type == 'property' &&
                      r.id == value &&
                      r.status != 'ARCHIVED',
                ) ==
                true
        ? value
        : 'all';
  }
}

Future<bool> confirmDemoAction(
  BuildContext context,
  String title, {
  String message =
      'Record this local demonstration only? No real transaction or external action occurs.',
}) async =>
    await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(title),
        content: Text(message),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Confirm demo action'),
          ),
        ],
      ),
    ) ??
    false;

class StatusChip extends StatelessWidget {
  const StatusChip(this.status, {super.key});
  final String status;
  @override
  Widget build(BuildContext context) {
    final colors = Theme.of(context).colorScheme;
    final upper = status.toUpperCase();
    final tone = [
      'ACTIVE', 'APPROVED', 'COMPLETED', 'PUBLISHED', 'PAID',
      'ACCEPTED', 'RESOLVED', 'VERIFIED',
    ].any(upper.contains)
        ? (Theme.of(context).brightness == Brightness.dark
            ? const Color(0xFF58D4BC) : const Color(0xFF087A66))
        : ['REJECT', 'CANCEL', 'DENIED', 'DISPUT', 'RESTRICT', 'FAILED']
                .any(upper.contains)
            ? colors.error
            : ['PENDING', 'REVIEW', 'DRAFT', 'CORRECTION', 'AWAIT']
                    .any(upper.contains)
                ? (Theme.of(context).brightness == Brightness.dark
                    ? const Color(0xFFF2BD69) : const Color(0xFF9A6507))
                : colors.primary;
    return Chip(
      materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
      visualDensity: VisualDensity.compact,
      backgroundColor: tone.withValues(alpha: 0.10),
      side: BorderSide(color: tone.withValues(alpha: 0.20)),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(9)),
      label: Text(
        status.replaceAll('_', ' '),
        style: Theme.of(context).textTheme.labelSmall?.copyWith(
          color: tone,
          fontWeight: FontWeight.w700,
        ),
      ),
    );
  }
}

class WorkspaceHeading extends StatelessWidget {
  const WorkspaceHeading(this.title, {this.subtitle, super.key});
  final String title;
  final String? subtitle;
  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    return Padding(
      padding: const EdgeInsets.only(bottom: 19),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Container(
            width: 4,
            height: 46,
            decoration: BoxDecoration(
              borderRadius: BorderRadius.circular(8),
              gradient: LinearGradient(
                begin: Alignment.topCenter,
                end: Alignment.bottomCenter,
                colors: [primary, primary.withValues(alpha: 0.28)],
              ),
            ),
          ),
          const SizedBox(width: 13),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(title, style: Theme.of(context).textTheme.headlineSmall),
                if (subtitle != null) ...[
                  const SizedBox(height: 7),
                  Text(
                    subtitle!,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ],
              ],
            ),
          ),
        ],
      ),
    );
  }
}

bool isDemoCalendarDate(WorkspaceRecord record, DateTime date) {
  final day = date.toIso8601String().substring(0, 10),
      start = (record.data['date'] ?? record.data['start'] ?? '').toString(),
      end = (record.data['end'] ?? '').toString();
  return start.isNotEmpty &&
      (start == day ||
          (end.isNotEmpty &&
              start.compareTo(day) <= 0 &&
              day.compareTo(end) < 0));
}
