import 'package:flutter/material.dart';
import '../workspace/workspace_screen.dart';

class PropertyScreen extends StatelessWidget {
  const PropertyScreen({required this.id, super.key});
  final String id;
  @override
  Widget build(BuildContext context) =>
      WorkspaceScreen(slug: 'properties', type: 'property', id: id);
}
