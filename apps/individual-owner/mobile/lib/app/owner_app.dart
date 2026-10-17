import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../core/router/owner_router.dart';
import '../core/theme/owner_theme.dart';

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
  );
}
