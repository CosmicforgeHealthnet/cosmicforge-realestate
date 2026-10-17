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

/// Sapphire Atelier — native owner interface.
/// Theme values derive from the shared, generated DesignTokens; no visual
/// decoration changes the repository, consent, or financial state machines.
ThemeData ownerTheme(Brightness brightness) {
  final dark = brightness == Brightness.dark;
  final canvas = dark ? DesignTokens.darkCanvas : DesignTokens.lightCanvas;
  final card = dark ? DesignTokens.darkCard : DesignTokens.lightCard;
  final subtle = dark ? DesignTokens.darkSubtle : DesignTokens.lightSubtle;
  final text = dark ? DesignTokens.darkText : DesignTokens.lightText;
  final muted = dark ? DesignTokens.darkMuted : DesignTokens.lightMuted;
  final border = dark ? DesignTokens.darkBorder : DesignTokens.lightBorder;
  final action = dark ? DesignTokens.darkAction : DesignTokens.lightAction;
  final scheme = ColorScheme.fromSeed(
    seedColor: DesignTokens.lightAction,
    brightness: brightness,
  ).copyWith(
    primary: action,
    onPrimary: dark ? DesignTokens.darkOnAction : DesignTokens.lightOnAction,
    surface: card,
    onSurface: text,
    surfaceContainerLowest: canvas,
    surfaceContainerLow: canvas,
    surfaceContainer: subtle,
    onSurfaceVariant: muted,
    outline: border,
    outlineVariant: border,
    error: dark ? DesignTokens.darkDanger : DesignTokens.lightDanger,
  );
  const inputRadius = 13.0;
  const cardRadius = 20.0;
  const buttonRadius = 13.0;
  final cardShape = RoundedRectangleBorder(
    borderRadius: BorderRadius.circular(cardRadius),
    side: BorderSide(color: border.withValues(alpha: dark ? 0.72 : 0.8)),
  );
  final inputShape = OutlineInputBorder(
    borderRadius: BorderRadius.circular(inputRadius),
    borderSide: BorderSide(color: border),
  );
  final buttonShape = RoundedRectangleBorder(
    borderRadius: BorderRadius.circular(buttonRadius),
  );
  final focusShape = OutlineInputBorder(
    borderRadius: BorderRadius.circular(inputRadius),
    borderSide: BorderSide(color: action, width: 1.7),
  );
  return ThemeData(
    fontFamily: 'Inter',
    useMaterial3: true,
    colorScheme: scheme,
    scaffoldBackgroundColor: canvas,
    splashFactory: InkRipple.splashFactory,
    visualDensity: VisualDensity.standard,
    appBarTheme: AppBarTheme(
      backgroundColor: card,
      foregroundColor: text,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 0,
      centerTitle: false,
      toolbarHeight: 70,
      titleTextStyle: TextStyle(
        color: text,
        fontFamily: 'Inter',
        fontSize: 19,
        fontWeight: FontWeight.w750,
        letterSpacing: -0.6,
      ),
    ),
    textTheme: TextTheme(
      headlineLarge: TextStyle(
        color: text, fontSize: 36, fontWeight: FontWeight.w750, letterSpacing: -1.3,
      ),
      headlineMedium: TextStyle(
        color: text, fontSize: 29, fontWeight: FontWeight.w750, letterSpacing: -1,
      ),
      headlineSmall: TextStyle(
        color: text, fontSize: 24, fontWeight: FontWeight.w720, letterSpacing: -0.8,
      ),
      titleLarge: TextStyle(
        color: text, fontSize: 20, fontWeight: FontWeight.w720, letterSpacing: -0.5,
      ),
      titleMedium: TextStyle(
        color: text, fontSize: 16, fontWeight: FontWeight.w700, letterSpacing: -0.3,
      ),
      titleSmall: TextStyle(
        color: text, fontSize: 14, fontWeight: FontWeight.w680,
      ),
      bodyLarge: TextStyle(color: text, fontSize: 16, height: 1.55),
      bodyMedium: TextStyle(color: text, fontSize: 14, height: 1.5),
      bodySmall: TextStyle(color: muted, fontSize: 12, height: 1.45),
      labelLarge: TextStyle(color: text, fontSize: 14, fontWeight: FontWeight.w700),
      labelMedium: TextStyle(color: muted, fontSize: 12, fontWeight: FontWeight.w660),
      labelSmall: TextStyle(color: muted, fontSize: 11, fontWeight: FontWeight.w680),
    ),
    cardTheme: CardThemeData(
      color: card,
      surfaceTintColor: Colors.transparent,
      elevation: dark ? 2 : 1.5,
      shadowColor: dark ? Colors.black.withValues(alpha: 0.20) : const Color(0x183A5887),
      margin: EdgeInsets.zero,
      shape: cardShape,
      clipBehavior: Clip.antiAlias,
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: card,
      contentPadding: const EdgeInsets.symmetric(horizontal: 17, vertical: 17),
      border: inputShape,
      enabledBorder: inputShape,
      focusedBorder: focusShape,
      errorBorder: inputShape.copyWith(borderSide: BorderSide(color: scheme.error)),
      focusedErrorBorder: focusShape.copyWith(borderSide: BorderSide(color: scheme.error, width: 1.7)),
      floatingLabelStyle: TextStyle(color: action, fontWeight: FontWeight.w700),
      hintStyle: TextStyle(color: muted),
    ),
    navigationBarTheme: NavigationBarThemeData(
      height: 78,
      backgroundColor: card,
      elevation: 7,
      shadowColor: dark ? Colors.black26 : const Color(0x183A5887),
      indicatorColor: action.withValues(alpha: dark ? 0.24 : 0.11),
      iconTheme: WidgetStateProperty.resolveWith((states) => IconThemeData(
        size: 23,
        color: states.contains(WidgetState.selected) ? action : muted,
      )),
      labelTextStyle: WidgetStateProperty.resolveWith((states) => TextStyle(
        fontFamily: 'Inter',
        fontSize: 11,
        fontWeight: states.contains(WidgetState.selected) ? FontWeight.w750 : FontWeight.w600,
        color: states.contains(WidgetState.selected) ? action : muted,
      )),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        minimumSize: const Size(44, 48),
        padding: const EdgeInsets.symmetric(horizontal: 19, vertical: 13),
        shape: buttonShape,
        textStyle: const TextStyle(
          fontFamily: 'Inter', fontSize: 13, fontWeight: FontWeight.w740,
        ),
        elevation: 0,
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        minimumSize: const Size(44, 48),
        padding: const EdgeInsets.symmetric(horizontal: 17, vertical: 13),
        shape: buttonShape,
        side: BorderSide(color: border),
        textStyle: const TextStyle(fontFamily: 'Inter', fontWeight: FontWeight.w690),
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(
        minimumSize: const Size(44, 44),
        shape: buttonShape,
        textStyle: const TextStyle(fontFamily: 'Inter', fontWeight: FontWeight.w690),
      ),
    ),
    chipTheme: ChipThemeData(
      backgroundColor: subtle,
      selectedColor: action.withValues(alpha: 0.12),
      side: BorderSide(color: border),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(10)),
      labelStyle: TextStyle(color: text, fontSize: 12, fontWeight: FontWeight.w700),
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
    ),
    listTileTheme: ListTileThemeData(
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(13)),
      iconColor: action,
      contentPadding: const EdgeInsets.symmetric(horizontal: 17, vertical: 8),
    ),
    dividerTheme: DividerThemeData(color: border, thickness: 0.8, space: 20),
    dialogTheme: DialogThemeData(
      backgroundColor: card,
      surfaceTintColor: Colors.transparent,
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(23)),
    ),
    bottomSheetTheme: BottomSheetThemeData(
      backgroundColor: card,
      surfaceTintColor: Colors.transparent,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(24)),
      ),
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      backgroundColor: dark ? DesignTokens.darkSubtle : DesignTokens.lightText,
      contentTextStyle: const TextStyle(color: Colors.white, fontFamily: 'Inter'),
      shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(13)),
    ),
  );
}
