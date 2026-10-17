import 'dart:convert';

typedef Json = Map<String, dynamic>;
Json cloneJson(Json value) => jsonDecode(jsonEncode(value)) as Json;

class WorkspaceRecord {
  WorkspaceRecord(Json source)
    : id = source['id'],
      type = source['type'],
      status = source['status'],
      version = source['version'],
      createdAt = source['createdAt'],
      data = Map.unmodifiable(source['data']);
  final String id, type, status, createdAt;
  final int version;
  final Json data;
}

class WorkspaceSnapshot {
  WorkspaceSnapshot(Json source) : raw = cloneJson(source);
  final Json raw;
  List<WorkspaceRecord> get records => (raw['records'] as List)
      .map((r) => WorkspaceRecord(r))
      .toList(growable: false);
  String get scenario => raw['scenario'];
  String get actor => raw['actor'];
  String get date => raw['date'];
  int get revision => raw['revision'];
  List<Json> get activity => (raw['activity'] as List).cast<Json>();
}

class WorkspaceCommand {
  const WorkspaceCommand({
    required this.op,
    required this.type,
    this.id,
    this.version,
    this.data = const {},
    this.action,
    this.confirmed = false,
    this.challenge = '',
  });
  final String op, type;
  final String? id, action;
  final int? version;
  final Json data;
  final bool confirmed;
  final String challenge;
  factory WorkspaceCommand.fromJson(Json json) => WorkspaceCommand(
    op: json['op'],
    type: json['type'],
    id: json['id'],
    version: json['version'],
    data: Json.from(json['data'] ?? {}),
    action: json['action'],
    confirmed: json['confirmed'] ?? false,
    challenge: json['challenge'] ?? '',
  );
}

class MockFault implements Exception {
  const MockFault(this.code, this.message);
  final String code, message;
  @override
  String toString() => '$code: $message';
}
