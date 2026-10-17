import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'design_tokens.dart';

final themeModeProvider = NotifierProvider<ThemeController, ThemeMode>(
  ThemeController.new,
);

class ThemeController extends Notifier<ThemeMode> {
  @override
  ThemeMode build() => ThemeMode.system;
  void toggle(Brightness current) =>
      state = current == Brightness.dark ? ThemeMode.light : ThemeMode.dark;
}

ThemeData ownerTheme(Brightness brightness) {
  final dark = brightness == Brightness.dark;
  final canvas = dark ? DesignTokens.darkCanvas : DesignTokens.lightCanvas;
  final card = dark ? DesignTokens.darkCard : DesignTokens.lightCard;
  final text = dark ? DesignTokens.darkText : DesignTokens.lightText;
  final border = dark ? DesignTokens.darkBorder : DesignTokens.lightBorder;
  final scheme =
      ColorScheme.fromSeed(
        seedColor: DesignTokens.lightAction,
        brightness: brightness,
      ).copyWith(
        primary: dark ? DesignTokens.darkAction : DesignTokens.lightAction,
        onPrimary: dark
            ? DesignTokens.darkOnAction
            : DesignTokens.lightOnAction,
        surface: card,
        onSurface: text,
        onSurfaceVariant: dark
            ? DesignTokens.darkMuted
            : DesignTokens.lightMuted,
        outlineVariant: border,
        error: dark ? DesignTokens.darkDanger : DesignTokens.lightDanger,
      );
  return ThemeData(
    fontFamily: 'Inter',
    useMaterial3: true,
    colorScheme: scheme,
    scaffoldBackgroundColor: canvas,
    appBarTheme: AppBarTheme(
      backgroundColor: card,
      foregroundColor: text,
      centerTitle: false,
      scrolledUnderElevation: 0,
    ),
    cardTheme: CardThemeData(
      color: card,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(DesignTokens.cardRadius),
        side: BorderSide(color: border),
      ),
    ),
    inputDecorationTheme: InputDecorationTheme(
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(DesignTokens.controlRadius),
      ),
    ),
    navigationBarTheme: NavigationBarThemeData(
      backgroundColor: card,
      indicatorColor: dark ? DesignTokens.darkSubtle : DesignTokens.lightSubtle,
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        minimumSize: const Size(44, 44),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(DesignTokens.controlRadius),
        ),
      ),
    ),
  );
}
