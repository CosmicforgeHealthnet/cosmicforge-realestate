import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

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
  String build() => 'all';
  void select(String value) => state = value;
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
  Widget build(BuildContext context) => Chip(
    label: Text(
      status.replaceAll('_', ' '),
      style: Theme.of(context).textTheme.labelSmall,
    ),
  );
}

class WorkspaceHeading extends StatelessWidget {
  const WorkspaceHeading(this.title, {this.subtitle, super.key});
  final String title;
  final String? subtitle;
  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(bottom: 16),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(title, style: Theme.of(context).textTheme.headlineSmall),
        if (subtitle != null) Text(subtitle!),
      ],
    ),
  );
}
