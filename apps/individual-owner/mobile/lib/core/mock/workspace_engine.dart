import '../models/workspace.dart';

Never fault(String code, String message) => throw MockFault(code, message);
bool activeBooking(Json r) =>
    ['REQUESTED', 'APPROVED', 'CHECKED_IN'].contains(r['status']);
bool overlap(Json a, Json b) =>
    a['start'].toString().compareTo(b['end'].toString()) < 0 &&
    a['end'].toString().compareTo(b['start'].toString()) > 0;

class WorkspaceEngine {
  WorkspaceEngine(this.schema, this.seed) : _state = cloneJson(seed);
  final Json schema, seed;
  Json _state;
  List<Json> get records => (_state['records'] as List).cast<Json>();
  List<Json> get specs => (schema['types'] as List).cast<Json>();
  WorkspaceSnapshot snapshot() => WorkspaceSnapshot(_state);
  Json spec(String type) => specs.firstWhere(
    (s) => s['id'] == type,
    orElse: () => fault('NOT_FOUND', 'Record family not found.'),
  );
  void reset() => _state = cloneJson(seed);
  void renameWorkspace(String name) => _state['workspaceName'] = name;
  void setTaskDone(String id, bool done) {
    final tasks = (_state['tasks'] as List).cast<Json>();
    final task = tasks.firstWhere(
      (t) => t['id'] == id,
      orElse: () => fault('NOT_FOUND', 'Sample task not found.'),
    );
    task['done'] = done;
  }

  void scenario(String value) {
    if (!(schema['scenarios'] as List).contains(value)) {
      fault('INVALID', 'Unknown demo scenario.');
    }
    reset();
    _state['scenario'] = value;
    if (value == 'empty') {
      _state['records'] = records
          .where((r) => ['account', 'preference'].contains(r['type']))
          .toList();
      _state['tasks'] = <Json>[];
    }
    if (['pending_verification', 'correction_required'].contains(value)) {
      for (final r in records.where(
        (r) => ['identity', 'verification'].contains(r['type']),
      )) {
        r['status'] = value == 'pending_verification'
            ? 'PENDING'
            : 'CORRECTION';
      }
    }
  }

  void actor(String value) {
    if (!['owner', 'manager'].contains(value)) {
      fault('INVALID', 'Unknown demo actor.');
    }
    _state['actor'] = value;
  }

  Json record(String id, [String? type]) => records.firstWhere(
    (r) => r['id'] == id && (type == null || r['type'] == type),
    orElse: () =>
        fault('NOT_FOUND', 'The selected demo record no longer exists.'),
  );
  String propertyOf(Json r, [int depth = 0]) {
    final data = Json.from(r['data']);
    if (r['type'] == 'property') {
      return r['id'];
    }
    if (data['propertyId'] != null) {
      return data['propertyId'].toString();
    }
    if (depth > 8) {
      return '';
    }
    for (final key in [
      'listingId',
      'unitId',
      'tenancyId',
      'offerId',
      'agreementId',
      'applicationId',
      'requestId',
      'bookingId',
      'utilityId',
      'budgetId',
      'saleId',
      'deviceId',
      'claimId',
    ]) {
      for (final related in records.where((x) => x['id'] == data[key])) {
        final property = propertyOf(related, depth + 1);
        if (property.isNotEmpty) {
          return property;
        }
      }
    }
    return '';
  }

  bool canManage(String type, String propertyId) {
    if (_state['actor'] == 'owner') {
      return true;
    }
    final capability = ['maintenance', 'quote', 'preventive'].contains(type)
        ? 'maintenance'
        : type == 'viewing'
        ? 'viewings'
        : type == 'listing'
        ? 'listings'
        : '';
    return capability.isNotEmpty &&
        records.any(
          (r) =>
              r['type'] == 'assignment' &&
              r['status'] == 'ACTIVE' &&
              r['data']['propertyId'] == propertyId &&
              r['data'][capability] == true &&
              r['data']['expires'].toString().compareTo(_state['date']) >= 0,
        );
  }

  void permission(String type, String propertyId) {
    final scenario = _state['scenario'];
    if (scenario == 'permission_denied') {
      fault(
        'DENIED',
        'This demo actor has no permission to view or change owner records.',
      );
    }
    if (scenario == 'expired_session' && type != 'account') {
      fault(
        'EXPIRED_SESSION',
        'Demo session expired. Reset QA or simulate login.',
      );
    }
    if (scenario == 'restricted') {
      fault(
        'RESTRICTED',
        'This account is restricted. Changes are unavailable.',
      );
    }
    if (scenario == 'offline') {
      fault(
        'OFFLINE',
        'Offline demonstration: cached records are read-only. No commands are queued.',
      );
    }
    if (scenario == 'provider_unavailable') {
      fault(
        'UNAVAILABLE',
        'Simulated service unavailable. No change was recorded.',
      );
    }
    if (scenario == 'failed_operation') {
      fault('FAILED', 'The simulated operation failed. No state was changed.');
    }
    if (scenario == 'insufficient_delegation' || !canManage(type, propertyId)) {
      fault(
        'DENIED',
        'Delegated permission is missing, expired, suspended or revoked. Title and payout authority cannot be delegated.',
      );
    }
  }

  void validate(Json definition, Json data, [String? id]) {
    if (_state['scenario'] == 'invalid_input') {
      fault('INVALID', 'QA scenario rejected submitted values.');
    }
    for (final f in (definition['fields'] as List).cast<Json>()) {
      final value = data[f['key']], kind = f['kind'], label = f['label'];
      if (f['required'] == true && (value == null || value == '')) {
        fault('INVALID', '$label is required.');
      }
      if (value == null || value == '') {
        continue;
      }
      if (kind == 'number' &&
          (value is! num ||
              value.toInt() != value ||
              value < (f['min'] ?? 0) ||
              value > (f['max'] ?? 9000000000000))) {
        fault('INVALID', '$label must be an integer in the displayed range.');
      }
      if (['text', 'textarea'].contains(kind) &&
          value.toString().length >
              (f['max'] ?? (kind == 'textarea' ? 1000 : 200))) {
        fault('INVALID', '$label is too long.');
      }
      if (kind == 'boolean' && value is! bool) {
        fault('INVALID', '$label must be true or false.');
      }
      if (kind == 'select' && !(f['options'] as List).contains(value)) {
        fault('INVALID', 'Choose a valid $label.');
      }
      if (kind == 'date') {
        final parsed = DateTime.tryParse(value.toString());
        if (!RegExp(r'^\d{4}-\d{2}-\d{2}$').hasMatch(value.toString()) ||
            parsed == null ||
            parsed.toIso8601String().substring(0, 10) != value) {
          fault('INVALID', 'Choose a valid $label.');
        }
      }
      if (kind == 'time' &&
          !RegExp(r'^([01]\d|2[0-3]):[0-5]\d$').hasMatch(value.toString())) {
        fault('INVALID', 'Use a valid time in UTC.');
      }
      if (f['pattern'] == 'email' &&
          !RegExp(r'^[^@\s]+@[^@\s]+\.[^@\s]+$').hasMatch(value.toString())) {
        fault('INVALID', 'Enter a fictional email address.');
      }
      if (f['pattern'] == 'lastFour' &&
          !RegExp(r'^\d{4}$').hasMatch(value.toString())) {
        fault('INVALID', 'Store only four fictional account-ending digits.');
      }
      if (kind == 'evidence' &&
          (!RegExp(
                r'\.(txt|pdf|png|jpe?g)$',
                caseSensitive: false,
              ).hasMatch(value.toString()) ||
              value.toString().contains(RegExp(r'[\\/]')))) {
        fault(
          'INVALID',
          'Select a fictional TXT, PDF, PNG or JPEG filename only.',
        );
      }
      if (kind == 'relation') {
        final related = record(value.toString(), f['target']);
        if (['ARCHIVED', 'REVOKED'].contains(related['status'])) {
          fault('RESTRICTED', '$label is archived or revoked.');
        }
        if (data['propertyId'] != null && related['type'] != 'property') {
          final property = propertyOf(related);
          if (property.isNotEmpty && property != data['propertyId']) {
            fault('INVALID', '$label belongs to another property.');
          }
        }
      }
    }
    final known = (definition['fields'] as List).map((f) => f['key']).toSet();
    for (final key in data.keys) {
      if (!known.contains(key)) {
        fault('INVALID', 'Unsupported field: $key');
      }
    }
    if (records.any(
      (r) =>
          r['type'] == definition['id'] &&
          r['id'] != id &&
          r['data']['title'].toString().trim().toLowerCase() ==
              data['title'].toString().trim().toLowerCase(),
    )) {
      fault('CONFLICT', 'A record with this title already exists.');
    }
    if (data['start'] != null &&
        data['end'] != null &&
        data['start'].toString().compareTo(data['end'].toString()) >= 0) {
      fault('INVALID', 'End date must be after start date.');
    }
    if (definition['id'] == 'party' &&
        ((data['role'] == 'Representative' && data['share'] != 0) ||
            (data['role'] == 'Co-owner' && (data['share'] as num) < 1))) {
      fault(
        'INVALID',
        'Representatives have zero title share; co-owner shares must be positive.',
      );
    }
    if (['accessgrant', 'documentgrant'].contains(definition['id']) &&
        data['expires'].toString().compareTo(_state['date']) < 0) {
      fault('INVALID', 'Choose a grant expiry on or after the demo date.');
    }
    if (definition['guard'] == 'shares') {
      final total = records
          .where(
            (r) =>
                r['type'] == 'party' &&
                r['id'] != id &&
                r['data']['propertyId'] == data['propertyId'] &&
                r['status'] != 'DECLINED',
          )
          .fold<num>(0, (n, r) => n + r['data']['share']);
      if (total + data['share'] > 100) {
        fault(
          'INVALID',
          'Combined proposed ownership shares cannot exceed 100%.',
        );
      }
    }
    if (definition['guard'] == 'meter' &&
        records.any(
          (r) =>
              r['type'] == 'meter' &&
              r['id'] != id &&
              r['data']['utilityId'] == data['utilityId'] &&
              r['data']['date'].toString().compareTo(data['date']) <= 0 &&
              r['data']['reading'] > data['reading'],
        )) {
      fault('INVALID', 'Meter reading cannot decrease.');
    }
    if (definition['id'] == 'renewal' &&
        data['newEnd'].toString().compareTo(
              record(data['tenancyId'], 'tenancy')['data']['end'],
            ) <=
            0) {
      fault('INVALID', 'Renewal end must extend the tenancy.');
    }
    if (definition['id'] == 'availability' &&
        data['reason'] != 'Available window' &&
        records.any(
          (r) =>
              r['type'] == 'booking' &&
              r['data']['unitId'] == data['unitId'] &&
              activeBooking(r) &&
              overlap(data, Json.from(r['data'])),
        )) {
      fault('CONFLICT', 'These dates overlap an active booking.');
    }
    if (definition['id'] == 'booking') {
      guard('booking', {
        'id': id ?? '',
        'type': 'booking',
        'data': data,
        'status': 'REQUESTED',
      });
    }
    if (['parcel', 'landterms'].contains(definition['id']) &&
        record(data['propertyId'], 'property')['data']['kind'] != 'Land') {
      fault('INVALID', 'Choose a land property for this record.');
    }
    if (definition['id'] == 'loi' &&
        record(data['propertyId'], 'property')['data']['kind'] !=
            'Commercial') {
      fault(
        'INVALID',
        'Choose a commercial property for this letter of intent.',
      );
    }
    if (definition['id'] == 'listing') {
      final property = record(data['propertyId'], 'property');
      if (['Rent', 'Short-let'].contains(data['transaction']) &&
          (data['unitId'] == null || data['unitId'] == '')) {
        fault('INVALID', 'Choose a unit for a rental or short-let listing.');
      }
      if (data['transaction'] == 'Land lease' &&
          property['data']['kind'] != 'Land') {
        fault('INVALID', 'Land lease listings require a land property.');
      }
      if (data['transaction'] == 'Short-let' &&
          record(data['unitId'], 'unit')['data']['kind'] != 'Short-let') {
        fault('INVALID', 'Short-let listings require a short-let unit.');
      }
    }
  }

  void guard(String name, Json row) {
    final data = Json.from(row['data']), property = propertyOf(row);
    if (name == 'claim_reviewed' &&
        !records.any(
          (x) =>
              x['type'] == 'ownership' &&
              x['id'] == data['claimId'] &&
              x['status'] == 'DEMO_REVIEWED',
        )) {
      fault(
        'PENDING',
        'Record simulated ownership review before property review.',
      );
    }
    if (['publish', 'consent'].contains(name)) {
      final purpose = name == 'publish' ? 'Listing' : 'Ownership transfer';
      for (final party in records.where(
        (x) =>
            x['type'] == 'party' &&
            x['data']['propertyId'] == property &&
            x['data']['role'] == 'Co-owner' &&
            x['status'] == 'ACCEPTED',
      )) {
        if (!records.any(
          (x) =>
              x['type'] == 'consent' &&
              x['data']['partyId'] == party['id'] &&
              x['data']['purpose'] == purpose &&
              x['status'] == 'APPROVED' &&
              x['data']['expires'].toString().compareTo(_state['date']) >= 0,
        )) {
          fault(
            'CONSENT',
            'Each accepted co-owner must record current consent for this purpose.',
          );
        }
      }
    }
    if (name == 'quote_choice' &&
        records.any(
          (x) =>
              x['type'] == 'quote' &&
              x['id'] != row['id'] &&
              x['data']['requestId'] == data['requestId'] &&
              x['status'] == 'APPROVED',
        )) {
      fault('CONFLICT', 'A quote is already approved for this request.');
    }
    if (name == 'publish') {
      if (!records.any(
        (x) =>
            x['type'] == 'verification' &&
            x['data']['propertyId'] == property &&
            x['status'] == 'DEMO_REVIEWED',
      )) {
        fault('PENDING', 'A separate simulated property review is required.');
      }
      if (!records.any(
        (x) =>
            x['type'] == 'pricing' &&
            x['data']['listingId'] == row['id'] &&
            x['status'] == 'APPLIED',
      )) {
        fault('INVALID', 'Apply example pricing before publication.');
      }
      if (!records.any(
        (x) =>
            x['type'] == 'availability' &&
            x['data']['propertyId'] == property &&
            x['status'] == 'CONFIRMED' &&
            x['data']['reason'] == 'Available window',
      )) {
        fault('STALE', 'Confirm sample availability before publication.');
      }
      if (data['previewed'] != true) {
        fault('INVALID', 'Review the listing preview checkbox.');
      }
    }
    if (name == 'consent' && data['consent'] != true) {
      fault('CONSENT', 'All demo consents must be recorded separately.');
    }
    if (name == 'consent_expiry' && _state['scenario'] == 'expired_consent') {
      fault('CONSENT', 'Occupant consent has expired.');
    }
    if (name == 'delegation_consent' &&
        !records.any(
          (x) =>
              x['type'] == 'consent' &&
              x['data']['propertyId'] == property &&
              x['data']['purpose'] == 'Delegation' &&
              x['status'] == 'APPROVED' &&
              x['data']['expires'].toString().compareTo(_state['date']) >= 0,
        )) {
      fault('CONSENT', 'Record current co-owner consent for delegation first.');
    }
    if (name == 'manager_access' &&
        (row['status'] != 'ACTIVE' ||
            data['expires'].toString().compareTo(_state['date']) < 0)) {
      fault('DENIED', 'Manager assignment is revoked, suspended or expired.');
    }
    if (name == 'property_archive' &&
        records.any(
          (x) =>
              ['listing', 'tenancy', 'booking'].contains(x['type']) &&
              propertyOf(x) == row['id'] &&
              [
                'DEMO_PUBLISHED',
                'ACTIVE',
                'APPROVED',
                'CHECKED_IN',
              ].contains(x['status']),
        )) {
      fault(
        'RESTRICTED',
        'Resolve active listings, tenancies and bookings before archiving.',
      );
    }
    if (name == 'tenancy_close') {
      if (data['keysReturned'] != true || data['inspection'] != true) {
        fault('PENDING', 'Complete inspection and key return first.');
      }
      final cases = records.where(
        (x) => x['type'] == 'deposit' && x['data']['tenancyId'] == row['id'],
      );
      if (cases.isEmpty || cases.any((x) => x['status'] != 'RESOLVED')) {
        fault('PENDING', 'Resolve all demo deposit cases before closure.');
      }
    }
    if (name == 'closure' &&
        records.any(
          (x) =>
              x['type'] == 'tenancy' && x['status'] != 'CLOSED' ||
              x['type'] == 'dispute' && x['status'] != 'RESOLVED' ||
              x['type'] == 'deposit' && x['status'] != 'RESOLVED',
        )) {
      fault(
        'RESTRICTED',
        'Closure blocked by active tenancies or unresolved cases. No backend account is deleted.',
      );
    }
    if (name == 'booking') {
      if (_state['scenario'] == 'stale_availability') {
        fault('STALE', 'Availability is stale. Refresh QA before retrying.');
      }
      if (record(data['unitId'], 'unit')['data']['kind'] != 'Short-let') {
        fault('INVALID', 'Choose a short-let unit.');
      }
      if (!records.any(
        (x) => x['type'] == 'rate' && x['data']['unitId'] == data['unitId'],
      )) {
        fault('INVALID', 'Configure an example unit rate first.');
      }
      if (records.any(
        (x) =>
            x['id'] != row['id'] &&
            x['type'] == 'booking' &&
            x['data']['unitId'] == data['unitId'] &&
            activeBooking(x) &&
            overlap(data, Json.from(x['data'])),
      )) {
        fault(
          'CONFLICT',
          'The short-let unit is already reserved for these dates.',
        );
      }
      if (records.any(
        (x) =>
            x['type'] == 'availability' &&
            x['data']['unitId'] == data['unitId'] &&
            x['status'] == 'BLOCKED' &&
            x['data']['reason'] != 'Available window' &&
            overlap(data, Json.from(x['data'])),
      )) {
        fault(
          'CONFLICT',
          'An owner or maintenance block overlaps these dates.',
        );
      }
    }
  }

  String nextId(String type) {
    var n = 1;
    while (records.any(
      (r) => r['id'] == 'demo-$type-${n.toString().padLeft(2, '0')}',
    )) {
      n++;
    }
    return 'demo-$type-${n.toString().padLeft(2, '0')}';
  }

  Json template(Json values, Json row) => values.map(
    (key, value) => MapEntry(
      key,
      value is String && value.startsWith(r'$')
          ? (value == r'$id'
                ? row['id']
                : row['data'][value.substring(1)] ?? '')
          : value,
    ),
  );
  Json createEffect(String type, String status, Json data) {
    final row = <String, dynamic>{
      'id': nextId(type),
      'type': type,
      'status': status,
      'version': 1,
      'createdAt': _state['date'],
      'data': data,
    };
    (_state['records'] as List).add(row);
    return row;
  }

  WorkspaceRecord execute(WorkspaceCommand command) {
    final before = cloneJson(_state);
    try {
      return WorkspaceRecord(cloneJson(apply(command)));
    } catch (_) {
      _state = before;
      rethrow;
    }
  }

  Json apply(WorkspaceCommand command) {
    final definition = spec(command.type);
    if (definition['readOnly'] == true) {
      fault('UNAVAILABLE', definition['provider'] ?? 'Provider unavailable.');
    }
    final existing = command.id != null
        ? record(command.id!, command.type)
        : null;
    final data = <String, dynamic>{...?existing?['data'], ...command.data};
    permission(
      command.type,
      existing != null
          ? propertyOf(existing)
          : (data['propertyId'] ??
                (data['listingId'] != null
                    ? propertyOf(record(data['listingId']))
                    : '')),
    );
    if (existing != null &&
        (command.version != existing['version'] ||
            _state['scenario'] == 'conflict')) {
      fault(
        'CONFLICT',
        'This record changed. Reload the latest version before retrying.',
      );
    }
    late Json result;
    if (['create', 'update'].contains(command.op)) {
      if (definition['sensitive'] == true && command.challenge != 'DEMO') {
        fault('STEP_UP', 'Type DEMO to confirm this local security step.');
      }
      validate(definition, data, existing?['id']);
      if (command.op == 'update' && existing == null) {
        fault('NOT_FOUND', 'Select a record to edit.');
      }
      if (existing != null) {
        existing['data'] = data;
        existing['version']++;
        result = existing;
      } else {
        result = createEffect(command.type, definition['initial'], data);
      }
    } else {
      result = existing ?? fault('NOT_FOUND', 'Select a record first.');
      final transition = (definition['actions'] as List)
          .cast<Json>()
          .firstWhere(
            (a) => a['id'] == command.action,
            orElse: () => fault('INVALID', 'Unknown demo action.'),
          );
      if (!(transition['from'] as List).contains(result['status'])) {
        fault('CONFLICT', 'Action is no longer allowed in this state.');
      }
      if (transition['confirm'] == true && !command.confirmed) {
        fault('CONFIRMATION', 'Review and confirm this demo action.');
      }
      if (transition['stepUp'] == true && command.challenge != 'DEMO') {
        fault(
          'STEP_UP',
          'Type DEMO to confirm the simulated security step. Never enter a real code or password.',
        );
      }
      if (transition['financial'] == true &&
          _state['scenario'] == 'unknown_financial_outcome') {
        fault(
          'UNKNOWN_OUTCOME',
          'Financial outcome unknown. No optimistic balance change or retry payment allowed.',
        );
      }
      if (_state['scenario'] == 'stale_availability' &&
          command.type == 'availability') {
        fault('STALE', 'Availability stale; reset QA to refresh.');
      }
      for (final requirement in (transition['requires'] ?? []) as List) {
        final value = data[requirement['key']];
        if (value == null || value == false || value == '') {
          fault('INVALID', requirement['message']);
        }
      }
      if (transition['guard'] != null) {
        guard(transition['guard'], result);
      }
      if (transition['id'] == 'movein') {
        final listing = record(data['listingId'], 'listing');
        if (records.any(
          (t) =>
              t['type'] == 'tenancy' &&
              t['status'] == 'ACTIVE' &&
              record(t['data']['listingId'])['data']['unitId'] ==
                  listing['data']['unitId'],
        )) {
          fault('CONFLICT', 'This unit already has an active tenancy.');
        }
      }
      if (transition['copy'] == true) {
        result = createEffect(command.type, 'DRAFT', {
          ...Json.from(result['data']),
          'title': '${result["data"]["title"]} · Copy ${_state["revision"]}',
        });
      } else {
        if (transition['to'] != 'SAME') {
          result['status'] = transition['to'];
        }
        result['version']++;
      }
      for (final e in (transition['effects'] ?? []) as List) {
        createEffect(
          e['type'],
          e['status'],
          template(Json.from(e['data']), result),
        );
      }
      final update = transition['updateRelated'];
      if (update != null) {
        final related = record(result['data'][update['via']], update['type']);
        (related['data'] as Json).addAll(
          template(Json.from(update['sets']), result),
        );
        if (update['status'] != null) {
          related['status'] = update['status'];
        }
        related['version']++;
      }
      if (transition['id'] == 'mfa') {
        _state['scenario'] = 'normal';
      }
      if ((command.type == 'tenancy' && transition['id'] == 'close') ||
          (command.type == 'agreement' && transition['id'] == 'movein')) {
        final listing = record(result['data']['listingId']);
        for (final unit in records.where(
          (x) => x['id'] == listing['data']['unitId'],
        )) {
          unit['data']['occupied'] = transition['id'] == 'movein';
          unit['version']++;
        }
      }
    }
    _state['revision']++;
    final label = command.op == 'transition'
        ? (definition['actions'] as List).firstWhere(
            (a) => a['id'] == command.action,
          )['label']
        : command.op == 'create'
        ? 'Created'
        : 'Updated';
    final message =
        '$label · ${result["data"]["title"]}. Recorded in demo — no real transaction occurred.';
    (_state['activity'] as List).insert(0, {
      'id': 'demo-event-${_state["revision"]}',
      'type': result['type'],
      'recordId': result['id'],
      'propertyId': propertyOf(result),
      'date': _state['date'],
      'message': message,
      'revision': _state['revision'],
    });
    if (result['type'] != 'notification') {
      createEffect('notification', 'UNREAD', {
        'title': '${definition["label"]} updated',
        'recordType': result['type'],
        'recordId': result['id'],
        'channel': 'In-app only',
        'message': message,
      });
    }
    return result;
  }

  Json summary({
    String propertyId = 'all',
    String start = '0000-00-00',
    String end = '9999-99-99',
  }) {
    final rows = records
        .where(
          (r) =>
              (propertyId == 'all' || propertyOf(r) == propertyId) &&
              r['createdAt'].toString().compareTo(start) >= 0 &&
              r['createdAt'].toString().compareTo(end) <= 0,
        )
        .toList();
    int count(String type, [String? status]) => rows
        .where(
          (r) => r['type'] == type && (status == null || r['status'] == status),
        )
        .length;
    final buckets = <String, dynamic>{
      for (final status in ['PENDING', 'HELD', 'AVAILABLE', 'RESTRICTED'])
        status: rows
            .where((r) => r['type'] == 'rent' && r['status'] == status)
            .fold<num>(0, (n, r) => n + r['data']['amountMinor']),
    };
    if (_state['scenario'] == 'restricted') {
      buckets['RESTRICTED'] = buckets.values.fold<num>(0, (n, v) => n + v);
      buckets['PENDING'] = buckets['HELD'] = buckets['AVAILABLE'] = 0;
    }
    return {
      'properties': rows
          .where((r) => r['type'] == 'property' && r['status'] != 'ARCHIVED')
          .length,
      'units': count('unit'),
      'occupied': rows
          .where((r) => r['type'] == 'unit' && r['data']['occupied'] == true)
          .length,
      'listings': count('listing', 'DEMO_PUBLISHED'),
      'maintenanceOpen': rows
          .where(
            (r) => r['type'] == 'maintenance' && r['status'] != 'COMPLETED',
          )
          .length,
      'maintenanceCompleted': count('maintenance', 'COMPLETED'),
      'bookings': rows
          .where((r) => r['type'] == 'booking' && activeBooking(r))
          .length,
      'buckets': buckets,
    };
  }
}
