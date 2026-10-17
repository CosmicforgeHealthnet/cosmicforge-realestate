import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:file_picker/file_picker.dart';
import '../../core/models/workspace.dart';
import '../../core/widgets/workspace_controls.dart';
import '../dashboard/dashboard_screen.dart';
import 'workspace_controller.dart';

class RecordForm extends ConsumerStatefulWidget {
  const RecordForm({
    required this.spec,
    required this.definition,
    required this.snapshot,
    this.record,
    this.propertyId = 'all',
    super.key,
  });
  final Json spec, definition;
  final WorkspaceSnapshot snapshot;
  final WorkspaceRecord? record;
  final String propertyId;
  @override
  ConsumerState<RecordForm> createState() => _RecordFormState();
}

class _RecordFormState extends ConsumerState<RecordForm> {
  final _form = GlobalKey<FormState>();
  final _challenge = TextEditingController();
  final _controllers = <String, TextEditingController>{};
  late Json _values;
  int? _version;
  int _step = 0;
  bool _saving = false;
  String _message = '';
  List<Json> get _fields => (widget.spec['fields'] as List).cast<Json>();
  @override
  void initState() {
    super.initState();
    _version = widget.record?.version;
    _values = {
      for (final f in _fields)
        f['key']:
            widget.record?.data[f['key']] ??
            (f['key'] == 'propertyId' && widget.propertyId != 'all'
                ? widget.propertyId
                : f['kind'] == 'boolean'
                ? false
                : f['kind'] == 'number'
                ? f['min'] ?? 0
                : f['kind'] == 'select'
                ? (f['options'] as List).first
                : ''),
    };
    for (final f in _fields) {
      if (!['boolean', 'relation', 'select'].contains(f['kind'])) {
        _controllers[f['key']] = TextEditingController(
          text: _values[f['key']].toString(),
        );
      }
    }
  }

  @override
  void dispose() {
    for (final controller in _controllers.values) {
      controller.dispose();
    }
    _challenge.dispose();
    super.dispose();
  }

  void dirty() => ref.read(draftGuardProvider.notifier).mark(true);
  void value(String key, dynamic value) {
    _values[key] = value;
    dirty();
  }

  Future<void> preview(String message) async => showDialog<void>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Fictional document preview'),
      content: SelectableText(message),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('Close preview'),
        ),
      ],
    ),
  );
  Future<void> save() async {
    if (!_form.currentState!.validate()) {
      return;
    }
    if (widget.spec['wizard'] == true && _step < 2) {
      setState(() => _step++);
      return;
    }
    setState(() {
      _saving = true;
      _message = '';
    });
    try {
      final result = await ref
          .read(workspaceProvider.notifier)
          .execute(
            WorkspaceCommand(
              op: widget.record == null ? 'create' : 'update',
              type: widget.spec['id'],
              id: widget.record?.id,
              version: _version,
              data: _values,
              challenge: _challenge.text,
            ),
          );
      ref.read(draftGuardProvider.notifier).mark(false);
      if (mounted) {
        context.go(recordPath(widget.definition, result.type, result.id));
      }
    } catch (error) {
      if (mounted) {
        setState(() => _message = error.toString());
      }
    } finally {
      if (mounted) {
        setState(() => _saving = false);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final groups = widget.spec['wizard'] == true ? 3 : 1,
        chunk = (_fields.length / groups).ceil(),
        fields = _fields.skip(_step * chunk).take(chunk);
    return DemoPanel(
      child: Form(
        key: _form,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            WorkspaceHeading(
              widget.record == null
                  ? 'New ${widget.spec["label"]}'
                  : 'Edit ${widget.spec["label"]}',
              subtitle: 'Step ${_step + 1} / $groups · Fictional data only',
            ),
            const Text(
              'Records stay local. Evidence selection stores only filename metadata; no document is uploaded.',
            ),
            const SizedBox(height: 20),
            ...fields.map(field),
            if (widget.spec['sensitive'] == true)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 12),
                child: TextFormField(
                  key: const Key('security-confirmation'),
                  controller: _challenge,
                  decoration: const InputDecoration(
                    labelText: 'Security confirmation',
                    hintText: 'Type DEMO',
                    helperText: 'Never enter real credentials or codes.',
                  ),
                ),
              ),
            if (_message.isNotEmpty)
              Semantics(
                liveRegion: true,
                child: Padding(
                  padding: const EdgeInsets.all(12),
                  child: Text(
                    _message,
                    style: TextStyle(
                      color: Theme.of(context).colorScheme.error,
                    ),
                  ),
                ),
              ),
            const SizedBox(height: 16),
            Wrap(
              spacing: 12,
              runSpacing: 12,
              children: [
                if (_step > 0)
                  TextButton(
                    onPressed: () => setState(() => _step--),
                    child: const Text('Previous'),
                  ),
                FilledButton(
                  key: const Key('save-record'),
                  onPressed: _saving ? null : save,
                  child: Text(
                    _step < groups - 1
                        ? 'Next step'
                        : _saving
                        ? 'Saving local record…'
                        : 'Save demo record',
                  ),
                ),
                TextButton(
                  onPressed: () async {
                    if (!ref.read(draftGuardProvider) ||
                        await confirmDemoAction(
                          context,
                          'Discard draft?',
                          message: 'Discard unsaved local form changes?',
                        )) {
                      ref.read(draftGuardProvider.notifier).mark(false);
                      if (context.mounted) {
                        context.go('/workspace/${widget.spec["module"]}');
                      }
                    }
                  },
                  child: const Text('Cancel'),
                ),
                if (widget.record != null)
                  TextButton(
                    onPressed: () {
                      setState(() {
                        _values = Json.from(widget.record!.data);
                        _version = widget.record!.version;
                        for (final entry in _controllers.entries) {
                          entry.value.text = (_values[entry.key] ?? '')
                              .toString();
                        }
                        _message = 'Latest version reloaded.';
                      });
                      ref.read(draftGuardProvider.notifier).mark(false);
                    },
                    child: const Text('Reload latest version'),
                  ),
              ],
            ),
          ],
        ),
      ),
    );
  }

  Widget field(Json f) {
    final key = f['key'] as String, kind = f['kind'];
    final label = f['label'].toString();
    final required = f['required'] == true;
    String? requiredValue(dynamic value) =>
        required && (value == null || value == '')
        ? '$label is required.'
        : null;
    if (kind == 'boolean') {
      return SwitchListTile(
        key: Key('field-$key'),
        contentPadding: EdgeInsets.zero,
        title: Text(label),
        value: _values[key] == true,
        onChanged: (v) => setState(() => value(key, v)),
      );
    }
    if (kind == 'select' || kind == 'relation') {
      final options = kind == 'select'
          ? (f['options'] as List)
                .map((v) => DropdownMenuItem<String>(value: v, child: Text(v)))
                .toList()
          : widget.snapshot.records
                .where(
                  (r) =>
                      r.type == f['target'] &&
                      !['ARCHIVED', 'REVOKED'].contains(r.status),
                )
                .map(
                  (r) => DropdownMenuItem<String>(
                    value: r.id,
                    child: Text(
                      '${r.data["title"]} · ${r.id}',
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                )
                .toList();
      final current = _values[key]?.toString();
      return Padding(
        padding: const EdgeInsets.only(bottom: 16),
        child: DropdownButtonFormField<String>(
          key: Key('field-$key'),
          initialValue: options.any((o) => o.value == current) ? current : null,
          isExpanded: true,
          decoration: InputDecoration(
            labelText: '$label${required ? " *" : ""}',
          ),
          items: options,
          validator: requiredValue,
          onChanged: (v) => value(key, v ?? ''),
        ),
      );
    }
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          TextFormField(
            key: Key('field-$key'),
            controller: _controllers[key],
            maxLines: kind == 'textarea' ? 3 : 1,
            keyboardType: kind == 'number'
                ? TextInputType.number
                : kind == 'date'
                ? TextInputType.datetime
                : TextInputType.text,
            decoration: InputDecoration(
              labelText: '$label${required ? " *" : ""}',
              helperText: key.toLowerCase().contains('minor')
                  ? '100 minor units = NGN 1. Example only.'
                  : kind == 'date'
                  ? 'YYYY-MM-DD · demonstration dates'
                  : null,
              suffixIcon: kind == 'date'
                  ? IconButton(
                      tooltip: 'Choose $label',
                      icon: const Icon(Icons.calendar_month_outlined),
                      onPressed: () async {
                        final selected = await showDatePicker(
                          context: context,
                          initialDate:
                              DateTime.tryParse(_controllers[key]!.text) ??
                              DateTime(2026, 10, 17),
                          firstDate: DateTime(2020),
                          lastDate: DateTime(2040),
                        );
                        if (selected != null) {
                          _controllers[key]!.text = selected
                              .toIso8601String()
                              .substring(0, 10);
                          value(key, _controllers[key]!.text);
                        }
                      },
                    )
                  : kind == 'time'
                  ? IconButton(
                      tooltip: 'Choose $label',
                      icon: const Icon(Icons.schedule),
                      onPressed: () async {
                        final selected = await showTimePicker(
                          context: context,
                          initialTime: const TimeOfDay(hour: 10, minute: 0),
                        );
                        if (selected != null) {
                          final time =
                              '${selected.hour.toString().padLeft(2, "0")}:${selected.minute.toString().padLeft(2, "0")}';
                          _controllers[key]!.text = time;
                          value(key, time);
                        }
                      },
                    )
                  : null,
            ),
            validator: (v) {
              final missing = requiredValue(v);
              if (missing != null) {
                return missing;
              }
              if (kind == 'number' && int.tryParse(v ?? '') == null) {
                return 'Enter an integer.';
              }
              return null;
            },
            onChanged: (v) =>
                value(key, kind == 'number' ? int.tryParse(v) ?? v : v),
          ),
          if (kind == 'evidence')
            Wrap(
              spacing: 8,
              runSpacing: 8,
              children: [
                TextButton(
                  onPressed: () async {
                    try {
                      final result = await FilePicker.pickFile(
                        type: FileType.custom,
                        allowedExtensions: ['txt', 'pdf', 'png', 'jpg', 'jpeg'],
                      );
                      if (result != null &&
                          (result.lengthSync() ?? 5242881) <= 5 * 1024 * 1024) {
                        final name = result.name;
                        _controllers[key]!.text = name;
                        value(key, name);
                      } else if (result != null && mounted) {
                        setState(
                          () => _message =
                              'Choose a fictional file smaller than 5 MB.',
                        );
                      }
                    } catch (_) {
                      if (mounted) {
                        setState(
                          () => _message =
                              'Local file picker unavailable. Choose a sample evidence item instead.',
                        );
                      }
                    }
                  },
                  child: const Text('Select fictional file'),
                ),
                TextButton(
                  key: Key('sample-$key'),
                  onPressed: () {
                    _controllers[key]!.text = 'sample-evidence.txt';
                    value(key, 'sample-evidence.txt');
                  },
                  child: const Text('Choose sample evidence'),
                ),
                TextButton(
                  onPressed: () => preview(
                    'Fictional evidence metadata: ${_controllers[key]!.text}. No upload, legal verification or provider action occurred.',
                  ),
                  child: const Text('Preview sample document'),
                ),
              ],
            ),
        ],
      ),
    );
  }
}
