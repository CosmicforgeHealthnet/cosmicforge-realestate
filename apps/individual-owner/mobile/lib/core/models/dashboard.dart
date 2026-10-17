// Mock presentation models. Future Gateway DTOs are mapped at repository boundaries.
class DemoProperty {
  const DemoProperty({
    required this.id,
    required this.name,
    required this.location,
    required this.kind,
    required this.units,
    required this.occupied,
    required this.verification,
  });
  factory DemoProperty.fromJson(Map<String, dynamic> json) => DemoProperty(
    id: json['id'],
    name: json['name'],
    location: json['location'],
    kind: json['kind'],
    units: json['units'],
    occupied: json['occupied'],
    verification: json['verification'],
  );
  final String id, name, location, kind, verification;
  final int units, occupied;
}

class DemoTask {
  const DemoTask({
    required this.id,
    required this.title,
    required this.category,
    this.propertyId = '',
    required this.done,
  });
  factory DemoTask.fromJson(Map<String, dynamic> json) => DemoTask(
    id: json['id'],
    title: json['title'],
    category: json['category'],
    propertyId: json['propertyId'] ?? '',
    done: json['done'],
  );
  final String id, title, category, propertyId;
  final bool done;
  DemoTask withDone(bool value) => DemoTask(
    id: id,
    title: title,
    category: category,
    propertyId: propertyId,
    done: value,
  );
}

class DemoBalance {
  const DemoBalance({
    required this.label,
    required this.amountMinor,
    required this.description,
  });
  factory DemoBalance.fromJson(Map<String, dynamic> json) => DemoBalance(
    label: json['label'],
    amountMinor: json['amountMinor'],
    description: json['description'],
  );
  final String label, description;
  final int amountMinor;
}

class Dashboard {
  Dashboard({
    required this.workspaceName,
    required List<DemoProperty> properties,
    required List<DemoTask> tasks,
    required List<DemoBalance> balances,
  }) : properties = List.unmodifiable(properties),
       tasks = List.unmodifiable(tasks),
       balances = List.unmodifiable(balances);
  factory Dashboard.fromJson(Map<String, dynamic> json) => Dashboard(
    workspaceName: json['workspaceName'],
    properties: (json['properties'] as List)
        .map((p) => DemoProperty.fromJson(p))
        .toList(),
    tasks: (json['tasks'] as List).map((p) => DemoTask.fromJson(p)).toList(),
    balances: (json['balances'] as List)
        .map((p) => DemoBalance.fromJson(p))
        .toList(),
  );
  final String workspaceName;
  final List<DemoProperty> properties;
  final List<DemoTask> tasks;
  final List<DemoBalance> balances;
  Dashboard copyWith({String? workspaceName, List<DemoTask>? tasks}) =>
      Dashboard(
        workspaceName: workspaceName ?? this.workspaceName,
        properties: properties,
        tasks: tasks ?? this.tasks,
        balances: balances,
      );
}

enum DemoScenario { populated, empty, error }
