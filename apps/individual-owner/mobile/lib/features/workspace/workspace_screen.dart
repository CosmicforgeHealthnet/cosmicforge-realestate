import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/workspace.dart';
import '../../core/widgets/workspace_controls.dart';
import '../../core/widgets/async_states.dart';
import '../dashboard/dashboard_screen.dart';
import 'workspace_controller.dart';
import 'record_form.dart';
import 'record_detail.dart';
import 'workspace_experience.dart';
import 'workspace_preference.dart';

class WorkspaceScreen extends ConsumerStatefulWidget {
  const WorkspaceScreen({
    required this.slug,
    this.type,
    this.id,
    this.edit = false,
    this.initialTab,
    this.property = 'all',
    super.key,
  });
  final String slug, property;
  final String? type, id, initialTab;
  final bool edit;
  @override
  ConsumerState<WorkspaceScreen> createState() => _WorkspaceScreenState();
}

class _WorkspaceScreenState extends ConsumerState<WorkspaceScreen> {
  String? selectedType;
  String search = '', filter = 'ALL', sort = 'title';
  int page = 0;
  bool calendar = false;
  final calendarStorage = PageStorageBucket();
  DateTime selectedDate = DateTime(2026, 10, 17);
  String from = '2026-10-01', to = '2026-10-31';
  @override
  void didUpdateWidget(covariant WorkspaceScreen oldWidget) {
    super.didUpdateWidget(oldWidget);
    if (oldWidget.slug != widget.slug ||
        oldWidget.type != widget.type ||
        oldWidget.id != widget.id) {
      selectedType = null;
      search = '';
      filter = 'ALL';
      page = 0;
      calendar = false;
      sort = 'title';
      from = '2026-10-01';
      to = '2026-10-31';
      selectedDate = DateTime(2026, 10, 17);
    }
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(workspaceProvider),
        definition = ref.watch(workspaceDefinitionProvider);
    return definition.when(
      loading: () => const LoadingView(),
      error: (e, _) => ErrorView(message: e.toString()),
      data: (definition) => state.when(
        loading: () => const LoadingView(),
        error: (e, _) => ErrorView(
          message: e.toString(),
          retry: () => ref.read(workspaceProvider.notifier).refresh(),
        ),
        data: (snapshot) => content(definition, snapshot),
      ),
    );
  }

  Widget content(Json definition, WorkspaceSnapshot snapshot) {
    final modules = (definition['modules'] as List).cast<Json>().where(
      (m) => m['slug'] == widget.slug,
    );
    if (modules.isEmpty) {
      return const EmptyView(
        title: 'Workspace not found',
        message: 'Choose a product area from More.',
      );
    }
    final area = modules.first, types = List<String>.from(area['types']);
    if (types.isEmpty) {
      return const DashboardScreen();
    }
    final type =
        widget.type ?? selectedType ?? widget.initialTab ?? types.first;
    final specs = (definition['types'] as List).cast<Json>().where(
      (t) => t['id'] == type,
    );
    if (specs.isEmpty || !types.contains(type)) {
      return const EmptyView(
        title: 'Record family not found',
        message: 'Return to the workspace directory.',
      );
    }
    final spec = specs.first,
        scope = ref.watch(propertyScopeProvider),
        projection = deriveWorkspace(definition, snapshot);
    if (['permission_denied', 'expired_session'].contains(snapshot.scenario) &&
        spec['id'] != 'account') {
      return ListView(
        padding: const EdgeInsets.all(24),
        children: [
          WorkspaceHeading(
            snapshot.scenario == 'permission_denied'
                ? 'Permission denied'
                : 'Demo session expired',
          ),
          const Text('No protected record details are displayed.'),
          TextButton(
            onPressed: () => context.go('/demo-lab'),
            child: const Text('Open developer QA'),
          ),
        ],
      );
    }
    final rows = snapshot.records.where((r) {
      final property = projection.propertyOf({
        'id': r.id,
        'type': r.type,
        'data': r.data,
      });
      return r.type == type &&
          (scope == 'all' || property.isEmpty || property == scope);
    }).toList();
    final filteredRows = rows
        .where(
          (r) =>
              (filter == 'ALL' || r.status == filter) &&
              jsonSearch(r).contains(search.toLowerCase()),
        )
        .toList();
    final maxPage = (filteredRows.length / 12).ceil() - 1;
    final currentPage = page > maxPage ? (maxPage < 0 ? 0 : maxPage) : page;
    final matches = snapshot.records.where(
          (r) => r.id == widget.id && r.type == type,
        ),
        record = matches.isEmpty ? null : matches.first;
    final summary = projection.summary(propertyId: scope, start: from, end: to);
    return ListView(
      key: PageStorageKey(
        'workspace-${widget.slug}-$type-${widget.id ?? "list"}',
      ),
      padding: EdgeInsets.fromLTRB(16, 16, 16, 24),
      children: [
        if (widget.id != null)
          TextButton(
            onPressed: () => context.go('/workspace/${widget.slug}?tab=$type'),
            child: const Text('Back to workspace list'),
          ),
        WorkspaceHeading(
          area['title'],
          subtitle: widget.id != null
              ? spec['label']
              : 'Connected local workflows · Demo data',
        ),
        if (snapshot.scenario != 'normal')
          DemoPanel(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text('QA scenario: ${snapshot.scenario}'),
                const Text('Relevant states and operation guards are active.'),
                TextButton(
                  onPressed: () => context.go('/demo-lab'),
                  child: const Text('Review scenario'),
                ),
              ],
            ),
          ),
        if (widget.id == null) ...[
          WorkspaceExperience(
            slug: widget.slug,
            recordCount: rows.length,
            statusCount: rows.map((r) => r.status).toSet().length,
            propertyScoped: scope != 'all',
          ),
          const SizedBox(height: 15),
          SizedBox(
            height: 56,
            child: ListView(
              scrollDirection: Axis.horizontal,
              children: types.map((id) {
                final model = (definition['types'] as List).firstWhere(
                  (t) => t['id'] == id,
                );
                return Padding(
                  padding: const EdgeInsets.only(right: 8),
                  child: ChoiceChip(
                    label: Text(model['label']),
                    selected: id == type,
                    onSelected: (_) => setState(() {
                      selectedType = id;
                      filter = 'ALL';
                      search = '';
                      page = 0;
                    }),
                  ),
                );
              }).toList(),
            ),
          ),
          const SizedBox(height: 12),
          if (spec['readOnly'] != true)
            FilledButton(
              key: const Key('new-record'),
              onPressed: () => context.push(
                '/workspace/${spec["module"]}/$type/new${scope != "all" ? "?property=$scope" : ""}',
              ),
              child: Text('New ${spec["label"]}'),
            ),
          if (['reports', 'rent', 'financial'].contains(widget.slug)) ...[
            const SizedBox(height: 16),
            DemoPanel(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  const WorkspaceHeading('Demonstration overview'),
                  if (widget.slug == 'reports')
                    Wrap(
                      spacing: 8,
                      runSpacing: 8,
                      children: [
                        TextButton(
                          onPressed: () async {
                            final date = await showDatePicker(
                              context: context,
                              initialDate: DateTime.parse(from),
                              firstDate: DateTime(2020),
                              lastDate: DateTime(2040),
                            );
                            if (date != null) {
                              setState(
                                () => from = date.toIso8601String().substring(
                                  0,
                                  10,
                                ),
                              );
                            }
                          },
                          child: Text('From $from'),
                        ),
                        TextButton(
                          onPressed: () async {
                            final date = await showDatePicker(
                              context: context,
                              initialDate: DateTime.parse(to),
                              firstDate: DateTime(2020),
                              lastDate: DateTime(2040),
                            );
                            if (date != null) {
                              setState(
                                () => to = date.toIso8601String().substring(
                                  0,
                                  10,
                                ),
                              );
                            }
                          },
                          child: Text('To $to'),
                        ),
                      ],
                    ),
                  Text(
                    '${summary["properties"]} properties · ${summary["occupied"]} / ${summary["units"]} occupied units',
                  ),
                  Text(
                    '${summary["listings"]} demo listings · ${summary["maintenanceCompleted"]} completed maintenance requests',
                  ),
                  const SizedBox(height: 12),
                  LinearProgressIndicator(
                    value: summary['units'] > 0
                        ? summary['occupied'] / summary['units']
                        : 0,
                  ),
                  const SizedBox(height: 16),
                  ...['PENDING', 'HELD', 'AVAILABLE', 'RESTRICTED'].map(
                    (bucket) => Padding(
                      padding: const EdgeInsets.only(bottom: 12),
                      child: Text(
                        '$bucket · ${demoMoney((summary["buckets"][bucket] as num).toInt())} · EXAMPLE',
                      ),
                    ),
                  ),
                  const Text(
                    'No real funds moved or protected custody. Figures derive from current mock records.',
                  ),
                ],
              ),
            ),
          ],
          if ([
            'inquiries',
            'viewings',
            'notifications',
            'professionals',
          ].contains(widget.slug))
            const DemoPanel(
              child: Column(
                children: [
                  Text(
                    'Human chat, calls and video require the future CF Meet integration. No messages or invitations are sent.',
                  ),
                  FilledButton(
                    onPressed: null,
                    child: Text('CF Meet unavailable'),
                  ),
                ],
              ),
            ),
          if (spec['provider'] != null) ...[
            const SizedBox(height: 16),
            DemoPanel(
              child: Column(
                children: [
                  Text(spec['provider']),
                  const FilledButton(
                    onPressed: null,
                    child: Text('External service unavailable'),
                  ),
                ],
              ),
            ),
          ],
          const SizedBox(height: 16),
          TextField(
            key: const Key('record-search'),
            decoration: InputDecoration(
              labelText: 'Search ${spec["label"]}',
              prefixIcon: const Icon(Icons.search),
            ),
            onChanged: (v) => setState(() {
              search = v;
              page = 0;
            }),
          ),
          const SizedBox(height: 12),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              DropdownButton<String>(
                isExpanded: true,
                value: filter,
                items:
                    {
                          'ALL',
                          ...rows.map((r) => r.status),
                          if (filter != 'ALL') filter,
                        }
                        .map(
                          (v) => DropdownMenuItem(
                            value: v,
                            child: Text(v == 'ALL' ? 'All statuses' : v),
                          ),
                        )
                        .toList(),
                onChanged: (v) => setState(() {
                  filter = v ?? 'ALL';
                  page = 0;
                }),
              ),
              DropdownButton<String>(
                isExpanded: true,
                value: sort,
                items: const [
                  DropdownMenuItem(value: 'title', child: Text('Sort: title')),
                  DropdownMenuItem(value: 'date', child: Text('Sort: newest')),
                  DropdownMenuItem(
                    value: 'status',
                    child: Text('Sort: status'),
                  ),
                ],
                onChanged: (v) => setState(() => sort = v ?? 'title'),
              ),
              if (spec['calendar'] == true)
                TextButton(
                  onPressed: () => setState(() => calendar = !calendar),
                  child: Text(calendar ? 'List view' : 'Calendar view'),
                ),
            ],
          ),
          if (calendar && spec['calendar'] == true)
            DemoPanel(
              child: Column(
                children: [
                  PageStorage(
                    bucket: calendarStorage,
                    child: CalendarDatePicker(
                      initialDate: selectedDate,
                      firstDate: DateTime(2020),
                      lastDate: DateTime(2040),
                      onDateChanged: (date) =>
                          setState(() => selectedDate = date),
                    ),
                  ),
                  ...filteredRows
                      .where((r) => isDemoCalendarDate(r, selectedDate))
                      .map(
                        (r) => ListTile(
                          title: Text(r.data['title']),
                          subtitle: Text(
                            '${r.status}${r.data["unitId"] == null ? "" : " · ${r.data["unitId"]}"}',
                          ),
                          onTap: () => context.push(
                            recordPath(definition, r.type, r.id),
                          ),
                        ),
                      ),
                ],
              ),
            ),
          ...listRows(filteredRows, definition, currentPage: currentPage),
          if (rows.isEmpty)
            EmptyView(
              title: 'No ${spec["label"].toString().toLowerCase()} yet',
              message:
                  'Create fictional records to begin. Related records must exist before selection.',
            ),
          if (rows.isNotEmpty && filteredRows.isEmpty)
            const Text(
              'No matching records. Change the search or status filter.',
            ),
          if (filteredRows.length > 12)
            Wrap(
              spacing: 12,
              runSpacing: 8,
              alignment: WrapAlignment.spaceBetween,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                TextButton(
                  onPressed: currentPage == 0
                      ? null
                      : () => setState(() => page = currentPage - 1),
                  child: const Text('Previous page'),
                ),
                Text('Page ${currentPage + 1}'),
                TextButton(
                  onPressed: (currentPage + 1) * 12 >= filteredRows.length
                      ? null
                      : () => setState(() => page = currentPage + 1),
                  child: const Text('Next page'),
                ),
              ],
            ),
          if (widget.slug == 'settings')
            const Padding(
              padding: EdgeInsets.only(top: 16),
              child: WorkspacePreference(),
            ),
          const SizedBox(height: 16),
          DemoPanel(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                const WorkspaceHeading('Recent workspace activity'),
                ...snapshot.activity
                    .where((a) => scope == 'all' || a['propertyId'] == scope)
                    .take(8)
                    .map(
                      (a) => TextButton(
                        onPressed: () => context.push(
                          recordPath(definition, a['type'], a['recordId']),
                        ),
                        child: Text('${a["date"]} · ${a["message"]}'),
                      ),
                    ),
                if (snapshot.activity.isEmpty)
                  const Text(
                    'No local changes yet. Seed records are fictional examples.',
                  ),
              ],
            ),
          ),
        ] else if (widget.id == 'new')
          RecordForm(
            key: ValueKey('new-$type'),
            spec: spec,
            definition: definition,
            snapshot: snapshot,
            propertyId: widget.property == 'all' ? scope : widget.property,
          )
        else if (record == null)
          const EmptyView(
            title: 'Record not found',
            message: 'This record is unavailable in the selected scenario.',
          )
        else if (widget.edit)
          RecordForm(
            key: ValueKey(record.id),
            spec: spec,
            definition: definition,
            snapshot: snapshot,
            record: record,
            propertyId: scope,
          )
        else
          RecordDetail(
            key: ValueKey(record.id),
            spec: spec,
            definition: definition,
            snapshot: snapshot,
            record: record,
          ),
      ],
    );
  }

  List<Widget> listRows(
    List<WorkspaceRecord> rows,
    Json definition, {
    required int currentPage,
  }) {
    final filtered =
        rows
            .where(
              (r) =>
                  (filter == 'ALL' || r.status == filter) &&
                  jsonSearch(r).contains(search.toLowerCase()),
            )
            .toList()
          ..sort(
            (a, b) => sort == 'date'
                ? b.createdAt.compareTo(a.createdAt)
                : sort == 'status'
                ? a.status.compareTo(b.status)
                : a.data['title'].toString().compareTo(
                    b.data['title'].toString(),
                  ),
          );
    return filtered
        .skip(currentPage * 12)
        .take(12)
        .map(
          (r) => Padding(
            padding: const EdgeInsets.only(top: 12),
            child: Card(
              child: ListTile(
                key: Key('record-${r.id}'),
                contentPadding: const EdgeInsets.all(16),
                leading: r.type == 'property'
                    ? Icon(
                        Icons.apartment_outlined,
                        color: Theme.of(context).colorScheme.primary,
                      )
                    : null,
                title: Text(r.data['title']),
                subtitle: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(r.id, style: Theme.of(context).textTheme.bodySmall),
                    Text(r.status.replaceAll('_', ' ')),
                    if (r.data['date'] != null || r.data['start'] != null)
                      Text(
                        '${r.data["date"] ?? r.data["start"]} ${r.data["end"] != null ? "→ ${r.data["end"]}" : ""}',
                      ),
                    if (r.type == 'bank') Text('•••• ${r.data["lastFour"]}'),
                    if (['quote', 'rent'].contains(r.type))
                      Text(demoMoney((r.data['amountMinor'] as num).toInt())),
                  ],
                ),
                trailing: const Icon(Icons.chevron_right),
                onTap: () => context.push(recordPath(definition, r.type, r.id)),
              ),
            ),
          ),
        )
        .toList();
  }

  String jsonSearch(WorkspaceRecord record) =>
      record.data.values.join(' ').toLowerCase();
}
