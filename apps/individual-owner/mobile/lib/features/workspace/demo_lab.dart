import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/widgets/async_states.dart';
import '../../core/widgets/workspace_controls.dart';
import '../dashboard/dashboard_screen.dart';
import 'workspace_controller.dart';

class DemoLab extends ConsumerStatefulWidget {
  const DemoLab({super.key});
  @override
  ConsumerState<DemoLab> createState() => _DemoLabState();
}

class _DemoLabState extends ConsumerState<DemoLab> {
  bool enabled = false;
  @override
  Widget build(BuildContext context) => ListView(
    padding: const EdgeInsets.all(16),
    children: [
      const WorkspaceHeading(
        'Developer QA laboratory',
        subtitle: 'Local mock controls only. No external services.',
      ),
      if (!enabled)
        FilledButton(
          onPressed: () => setState(() => enabled = true),
          child: const Text('Enable developer QA'),
        )
      else
        ref
            .watch(workspaceDefinitionProvider)
            .when(
              loading: () => const LoadingView(),
              error: (e, _) => ErrorView(message: e.toString()),
              data: (definition) => ref
                  .watch(workspaceProvider)
                  .when(
                    loading: () => const LoadingView(),
                    error: (e, _) => ErrorView(message: e.toString()),
                    data: (state) => DemoPanel(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.stretch,
                        children: [
                          DropdownButtonFormField<String>(
                            key: ValueKey(state.scenario),
                            initialValue: state.scenario,
                            isExpanded: true,
                            decoration: const InputDecoration(
                              labelText: 'QA scenario',
                            ),
                            items: (definition['scenarios'] as List)
                                .map(
                                  (s) => DropdownMenuItem<String>(
                                    value: s,
                                    child: SizedBox(
                                      width: 200,
                                      child: Text(
                                        s.toString().replaceAll('_', ' '),
                                        maxLines: 2,
                                        overflow: TextOverflow.ellipsis,
                                      ),
                                    ),
                                  ),
                                )
                                .toList(),
                            onChanged: (value) {
                              if (value != null) {
                                ref
                                    .read(workspaceProvider.notifier)
                                    .scenario(value);
                              }
                            },
                          ),
                          const SizedBox(height: 16),
                          DropdownButtonFormField<String>(
                            key: ValueKey(state.actor),
                            initialValue: state.actor,
                            isExpanded: true,
                            decoration: const InputDecoration(
                              labelText: 'Simulated actor',
                            ),
                            items: const [
                              DropdownMenuItem(
                                value: 'owner',
                                child: Text('Property owner'),
                              ),
                              DropdownMenuItem(
                                value: 'manager',
                                child: Text('Delegated manager'),
                              ),
                            ],
                            onChanged: (value) {
                              if (value != null) {
                                ref
                                    .read(workspaceProvider.notifier)
                                    .actor(value);
                              }
                            },
                          ),
                          const SizedBox(height: 16),
                          const Text(
                            'Changing QA scenario resets the fixtures deterministically. It changes actual state guards, not just labels.',
                          ),
                          FilledButton(
                            onPressed: () async {
                              if (await confirmDemoAction(
                                context,
                                'Reset Demo Data',
                                message:
                                    'Discard local changes and restore fictional seed records?',
                              )) {
                                ref.read(workspaceProvider.notifier).reset();
                              }
                            },
                            child: const Text('Reset Demo Data'),
                          ),
                          Text(
                            'Revision ${state.revision} · Demo clock ${state.date} UTC',
                          ),
                        ],
                      ),
                    ),
                  ),
            ),
    ],
  );
}
