import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:file_picker/file_picker.dart';
import '../../core/models/workspace.dart';
import '../../core/widgets/workspace_controls.dart';
import '../dashboard/dashboard_screen.dart';
import 'workspace_controller.dart';

class RecordDetail extends ConsumerStatefulWidget {
  const RecordDetail({
    required this.spec,
    required this.definition,
    required this.snapshot,
    required this.record,
    super.key,
  });
  final Json spec, definition;
  final WorkspaceSnapshot snapshot;
  final WorkspaceRecord record;
  @override
  ConsumerState<RecordDetail> createState() => _RecordDetailState();
}

class _RecordDetailState extends ConsumerState<RecordDetail> {
  String message = '';
  bool busy = false;
  Future<void> transition(Json action) async {
    var challenge = '';
    if (action['stepUp'] == true || action['confirm'] == true) {
      final result = await showDialog<String>(
        context: context,
        builder: (context) => _SecurityDialog(action),
      );
      if (result == null) {
        return;
      }
      challenge = result;
    }
    if (!mounted) {
      return;
    }
    setState(() {
      busy = true;
      message = '';
    });
    try {
      await ref
          .read(workspaceProvider.notifier)
          .execute(
            WorkspaceCommand(
              op: 'transition',
              type: widget.record.type,
              id: widget.record.id,
              version: widget.record.version,
              action: action['id'],
              confirmed: true,
              challenge: challenge,
            ),
          );
      if (mounted) {
        setState(
          () => message = 'Recorded in demo — no real transaction occurred.',
        );
      }
    } catch (error) {
      if (mounted) {
        setState(() => message = error.toString());
      }
    } finally {
      if (mounted) {
        setState(() => busy = false);
      }
    }
  }

  Future<void> documentPreview(String text) async => showDialog<void>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Fictional evidence preview'),
      content: SelectableText(text),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Close preview'),
        ),
      ],
    ),
  );
  Future<void> exportReport() async {
    final text = const JsonEncoder.withIndent('  ').convert({
      'notice': 'DEMO DATA — NO REAL TRANSACTION',
      'date': widget.snapshot.date,
      'record': {
        'id': widget.record.id,
        'data': widget.record.data,
        'status': widget.record.status,
      },
      'summary': deriveWorkspace(widget.definition, widget.snapshot).summary(
        propertyId: widget.record.data['propertyId'] ?? 'all',
        start: widget.record.data['start'] ?? '0000-00-00',
        end: widget.record.data['end'] ?? '9999-99-99',
      ),
    });
    try {
      final path = await FilePicker.saveFile(
        dialogTitle: 'Save demonstration report',
        fileName: 'cosmicforge-demo-report.json',
        type: FileType.custom,
        allowedExtensions: ['json'],
        bytes: Uint8List.fromList(utf8.encode(text)),
      );
      if (mounted) {
        setState(
          () => message = path == null
              ? 'Export cancelled.'
              : 'Demonstration report saved locally.',
        );
      }
    } catch (_) {
      if (mounted) {
        showDialog<void>(
          context: context,
          builder: (context) => AlertDialog(
            title: const Text('Demo report export'),
            content: SingleChildScrollView(child: SelectableText(text)),
            actions: [
              TextButton(
                onPressed: () async {
                  await Clipboard.setData(ClipboardData(text: text));
                },
                child: const Text('Copy demo export'),
              ),
              TextButton(
                onPressed: () => Navigator.pop(context),
                child: const Text('Close export'),
              ),
            ],
          ),
        );
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final record = widget.record,
        spec = widget.spec,
        snapshot = widget.snapshot;
    final projection = deriveWorkspace(widget.definition, snapshot),
        property = projection.propertyOf({
          'id': record.id,
          'type': record.type,
          'data': record.data,
        });
    final related = snapshot.records
        .where(
          (r) =>
              r.id != record.id &&
              (r.data.values.contains(record.id) ||
                  (record.type == 'property' &&
                      projection.propertyOf({
                            'id': r.id,
                            'type': r.type,
                            'data': r.data,
                          }) ==
                          record.id)),
        )
        .toList();
    final blocked = [
      'restricted',
      'permission_denied',
      'offline',
      'expired_session',
      'insufficient_delegation',
    ].contains(snapshot.scenario);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Wrap(
                spacing: 8,
                runSpacing: 8,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  StatusChip(record.status),
                  Text('Version ${record.version}'),
                  if (spec['readOnly'] != true)
                    TextButton(
                      key: const Key('edit-record'),
                      onPressed: () => context.push(
                        '${recordPath(widget.definition, record.type, record.id)}/edit',
                      ),
                      child: const Text('Edit record'),
                    ),
                ],
              ),
              Text(record.id, style: Theme.of(context).textTheme.bodySmall),
              const SizedBox(height: 12),
              Text(
                record.data['title'].toString(),
                style: Theme.of(context).textTheme.titleLarge,
              ),
              const SizedBox(height: 8),
              const Text(
                'Fictional record · No external verification, legal execution or financial settlement.',
              ),
              if (spec['provider'] != null) ...[
                const SizedBox(height: 12),
                Text(spec['provider']),
                const FilledButton(
                  onPressed: null,
                  child: Text('External provider unavailable'),
                ),
              ],
              const SizedBox(height: 20),
              ...((spec['fields'] as List)
                  .cast<Json>()
                  .where((f) => f['key'] != 'title')
                  .map((f) {
                    final value = record.data[f['key']];
                    final matches = f['kind'] == 'relation'
                        ? snapshot.records.where((r) => r.id == value)
                        : <WorkspaceRecord>[];
                    final related = matches.isNotEmpty ? matches.first : null;
                    final display = f['key'] == 'lastFour'
                        ? '•••• ${value ?? "----"}'
                        : f['key'].toString().toLowerCase().contains('minor')
                        ? demoMoney((value as num? ?? 0).toInt())
                        : value is bool
                        ? value
                              ? 'Yes · demo'
                              : 'No'
                        : value?.toString() ?? 'Not recorded';
                    return Padding(
                      padding: const EdgeInsets.only(bottom: 16),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            f['label'],
                            style: Theme.of(context).textTheme.labelSmall,
                          ),
                          if (related != null)
                            TextButton(
                              onPressed: () => context.push(
                                recordPath(
                                  widget.definition,
                                  related.type,
                                  related.id,
                                ),
                              ),
                              child: Text(related.data['title']),
                            )
                          else
                            Text(display),
                        ],
                      ),
                    );
                  })),
              if (record.data['evidence'] != null &&
                  record.data['evidence'] != '')
                TextButton(
                  onPressed:
                      record.type == 'application' &&
                          record.data['evidenceAccess'] != true
                      ? null
                      : () => documentPreview(
                          'Fictional metadata: ${record.data["evidence"]}. No document upload or certification occurred.',
                        ),
                  child: const Text('Preview evidence metadata'),
                ),
              if (record.type == 'pricing')
                Text(
                  'Total example cost: ${demoMoney((record.data["amountMinor"] as num).toInt() + (record.data["feeMinor"] as num).toInt())}. No statutory rate or tax certification.',
                ),
              if (record.type == 'sale')
                const Text(
                  'Closing checklist and ownership consent are separate. Financial settlement remains unconfirmed / unavailable.',
                ),
              if (record.type == 'rent')
                Text(
                  'Example receipt ${record.id}. Not proof of real payment.',
                ),
              if (record.type == 'notification') ...[
                const Text(
                  'Delivery: local in-app example only. No push/email/SMS sent.',
                ),
                if (snapshot.records.any(
                  (r) => r.id == record.data['recordId'],
                ))
                  TextButton(
                    onPressed: () => context.push(
                      recordPath(
                        widget.definition,
                        record.data['recordType'],
                        record.data['recordId'],
                      ),
                    ),
                    child: const Text('Open related record'),
                  ),
              ],
              if (record.type == 'report' && record.status == 'GENERATED')
                FilledButton(
                  onPressed: exportReport,
                  child: const Text('Download demo report'),
                ),
              const SizedBox(height: 16),
              Wrap(
                spacing: 8,
                runSpacing: 8,
                children: (spec['actions'] as List)
                    .cast<Json>()
                    .where((a) => (a['from'] as List).contains(record.status))
                    .map(
                      (a) => FilledButton(
                        key: Key('action-${a["id"]}'),
                        onPressed: blocked || busy ? null : () => transition(a),
                        child: Text(a['label']),
                      ),
                    )
                    .toList(),
              ),
              if (blocked)
                const Text('Actions unavailable in the selected scenario.'),
              if (message.isNotEmpty)
                Semantics(
                  liveRegion: true,
                  child: Padding(
                    padding: const EdgeInsets.only(top: 16),
                    child: Text(message),
                  ),
                ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const WorkspaceHeading('Related workspace'),
              if (property.isNotEmpty && record.type != 'property')
                TextButton(
                  onPressed: () => context.push(
                    recordPath(widget.definition, 'property', property),
                  ),
                  child: const Text('Property context'),
                ),
              if (property.isNotEmpty)
                Wrap(
                  spacing: 8,
                  runSpacing: 8,
                  children: (widget.definition['types'] as List)
                      .cast<Json>()
                      .where(
                        (t) => [
                          'unit',
                          'listing',
                          'ownership',
                          'verification',
                          'maintenance',
                          'utility',
                          'parcel',
                        ].contains(t['id']),
                      )
                      .map(
                        (t) => TextButton(
                          onPressed: () => context.push(
                            '/workspace/${t["module"]}/${t["id"]}/new?property=$property',
                          ),
                          child: Text('New ${t["label"]}'),
                        ),
                      )
                      .toList(),
                ),
              ...related
                  .take(30)
                  .map(
                    (r) => ListTile(
                      contentPadding: EdgeInsets.zero,
                      title: Text(r.data['title']),
                      subtitle: Text('${r.type} · ${r.status}'),
                      trailing: const Icon(Icons.chevron_right),
                      onTap: () => context.push(
                        recordPath(widget.definition, r.type, r.id),
                      ),
                    ),
                  ),
              if (related.isEmpty) const Text('No linked records yet.'),
            ],
          ),
        ),
        const SizedBox(height: 16),
        DemoPanel(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const WorkspaceHeading('Record history'),
              ...snapshot.activity
                  .where((a) => a['recordId'] == record.id)
                  .map(
                    (a) => Padding(
                      padding: const EdgeInsets.only(bottom: 12),
                      child: Text('${a["date"]} · ${a["message"]}'),
                    ),
                  ),
              if (!snapshot.activity.any((a) => a['recordId'] == record.id))
                const Text(
                  'Seeded fictional record. Local actions appear here.',
                ),
            ],
          ),
        ),
      ],
    );
  }
}

class _SecurityDialog extends StatefulWidget {
  const _SecurityDialog(this.action);
  final Json action;
  @override
  State<_SecurityDialog> createState() => _SecurityDialogState();
}

class _SecurityDialogState extends State<_SecurityDialog> {
  final controller = TextEditingController();
  @override
  void dispose() {
    controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    title: Text(widget.action['label']),
    content: SingleChildScrollView(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Text(
            'Local demonstration only. No real transaction, legal execution or device action.',
          ),
          if (widget.action['stepUp'] == true)
            TextField(
              key: const Key('action-challenge'),
              controller: controller,
              decoration: const InputDecoration(
                labelText: 'Security confirmation',
                hintText: 'Type DEMO',
                helperText: 'Never enter real credentials or codes.',
              ),
            ),
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('Cancel'),
      ),
      FilledButton(
        onPressed: () => Navigator.pop(context, controller.text),
        child: const Text('Confirm demo action'),
      ),
    ],
  );
}
