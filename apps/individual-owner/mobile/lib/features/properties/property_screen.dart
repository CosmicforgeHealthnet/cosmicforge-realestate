import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/widgets/async_states.dart';
import '../dashboard/dashboard_controller.dart';
import '../dashboard/dashboard_screen.dart';

class PropertyScreen extends ConsumerWidget {
  const PropertyScreen({required this.id, super.key});
  final String id;
  @override
  Widget build(BuildContext context, WidgetRef ref) => ref
      .watch(dashboardProvider)
      .when(
        loading: () => const LoadingView(),
        error: (e, _) => ErrorView(message: e.toString()),
        data: (data) {
          final matches = data.properties.where((p) => p.id == id);
          if (matches.isEmpty) {
            return const EmptyView(
              title: 'Sample property not found',
              message: 'Return to Home to select a sample property.',
            );
          }
          final property = matches.first;
          return ListView(
            padding: const EdgeInsets.all(16),
            children: [
              Text(
                property.name,
                style: Theme.of(context).textTheme.headlineMedium,
              ),
              const SizedBox(height: 8),
              Text(property.location),
              const SizedBox(height: 16),
              DemoPanel(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Icon(
                      Icons.apartment_outlined,
                      size: 64,
                      color: Theme.of(context).colorScheme.primary,
                    ),
                    const SizedBox(height: 16),
                    Text('${property.kind} · ${property.units} units'),
                    Text('Sample occupied units: ${property.occupied}'),
                    Text('Verification: ${property.verification}'),
                    const SizedBox(height: 16),
                    const Text(
                      'Fictional property · Foundation overview. No ownership or legal title has been verified. Supporting workflows follow in later sections.',
                    ),
                    const SizedBox(height: 16),
                    const FilledButton(
                      onPressed: null,
                      child: Text('Verification unavailable'),
                    ),
                  ],
                ),
              ),
            ],
          );
        },
      );
}
