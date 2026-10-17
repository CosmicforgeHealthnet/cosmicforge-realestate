import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:cosmicforge_owner/features/workspace/workspace_experience.dart';

void main() {
  testWidgets('premium property flow explains steps and preserves owner context', (tester) async {
    await tester.pumpWidget(const MaterialApp(
      home: Scaffold(
        body: SingleChildScrollView(
          child: WorkspaceExperience(
            slug: 'properties',
            recordCount: 3,
            statusCount: 2,
            propertyScoped: true,
          ),
        ),
      ),
    ));
    expect(find.text('PORTFOLIO STUDIO'), findsOneWidget);
    expect(find.text('Create property'), findsOneWidget);
    expect(find.text('Add units'), findsOneWidget);
    expect(find.text('Prepare listing'), findsOneWidget);
    expect(find.text('Property scoped'), findsOneWidget);
    expect(find.text('Continue to Listing management'), findsOneWidget);
  });

  testWidgets('all 29 non-dashboard product areas expose an appropriate flow', (tester) async {
    const slugs = [
      'identity','financial','ownership','properties','verification','listings',
      'pricing','availability','inquiries','viewings','applications','tenancies',
      'rent','renewals','utilities','commercial','land','sales','shortlets',
      'managers','maintenance','professionals','notifications','reports',
      'safety','settings','financing','insurance','access',
    ];
    for (final slug in slugs) {
      await tester.pumpWidget(MaterialApp(
        home: Scaffold(
          body: SingleChildScrollView(
            child: WorkspaceExperience(
              key: ValueKey(slug),
              slug: slug,
              recordCount: 0,
              statusCount: 0,
              propertyScoped: false,
            ),
          ),
        ),
      ));
      expect(find.text('From overview to outcome'), findsOneWidget, reason: slug);
      expect(find.textContaining('Continue to '), findsOneWidget, reason: slug);
    }
  });
}
