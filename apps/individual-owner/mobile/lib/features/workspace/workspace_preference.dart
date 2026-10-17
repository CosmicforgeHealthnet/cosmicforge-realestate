import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../dashboard/dashboard_controller.dart';
import '../dashboard/dashboard_screen.dart';

class WorkspacePreference extends ConsumerStatefulWidget {
  const WorkspacePreference({super.key});
  @override
  ConsumerState<WorkspacePreference> createState() =>
      _WorkspacePreferenceState();
}

class _WorkspacePreferenceState extends ConsumerState<WorkspacePreference> {
  final _form = GlobalKey<FormState>();
  final _name = TextEditingController();
  String _message = '';
  bool _saving = false;
  @override
  void dispose() {
    _name.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => DemoPanel(
    child: Form(
      key: _form,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            'Demo workspace preference',
            style: Theme.of(context).textTheme.titleLarge,
          ),
          const SizedBox(height: 8),
          Text(
            'Current label: ${ref.watch(dashboardProvider).asData?.value.workspaceName ?? "Loading…"}',
          ),
          const SizedBox(height: 16),
          TextFormField(
            controller: _name,
            maxLength: 60,
            decoration: const InputDecoration(
              labelText: 'Workspace label',
              helperText: '2–60 characters. Use fictional information.',
            ),
            validator: (value) {
              final length = value?.trim().length ?? 0;
              return length < 2 || length > 60 ? 'Use 2–60 characters.' : null;
            },
          ),
          const SizedBox(height: 8),
          FilledButton(
            onPressed: _saving
                ? null
                : () async {
                    if (!_form.currentState!.validate()) return;
                    setState(() => _saving = true);
                    try {
                      await ref
                          .read(dashboardProvider.notifier)
                          .rename(_name.text);
                      if (mounted) {
                        setState(
                          () => _message = 'Demo preference saved locally.',
                        );
                      }
                    } catch (error) {
                      if (mounted) {
                        setState(
                          () => _message = 'Local update failed. Try again.',
                        );
                      }
                    } finally {
                      if (mounted) setState(() => _saving = false);
                    }
                  },
            child: const Text('Save demo preference'),
          ),
          Semantics(liveRegion: true, child: Text(_message)),
        ],
      ),
    ),
  );
}
