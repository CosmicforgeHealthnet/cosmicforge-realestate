import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../core/router/owner_router.dart';
import '../core/theme/owner_theme.dart';
import '../features/workspace/workspace_controller.dart';
import 'dart:math' as math;

class OwnerApp extends ConsumerWidget {
  const OwnerApp({super.key});
  @override
  Widget build(BuildContext context, WidgetRef ref) => MaterialApp.router(
    title: 'CosmicForge Owner Demo',
    debugShowCheckedModeBanner: false,
    theme: ownerTheme(Brightness.light),
    darkTheme: ownerTheme(Brightness.dark),
    themeMode: ref.watch(themeModeProvider),
    routerConfig: ref.watch(routerProvider),
    builder: (context, child) {
      final records = ref
          .watch(workspaceProvider)
          .asData
          ?.value
          .records
          .where((r) => r.type == 'preference');
      final preference = records?.isNotEmpty == true
          ? records!.last.data
          : <String, dynamic>{};
      final media = MediaQuery.of(context);
      return MediaQuery(
        data: media.copyWith(
          textScaler: preference['largeText'] == true
              ? TextScaler.linear(math.max(1.3, media.textScaler.scale(1)))
              : media.textScaler,
          disableAnimations:
              media.disableAnimations || preference['reducedMotion'] == true,
        ),
        child: child!,
      );
    },
  );
}
