"""Reconcile every Section 5 table and Annex D table alias with owned migrations.
No database/network access. The integration catalogue test verifies live pg_catalog.
"""
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICE = ROOT / 'services/business-operations-service'
spec = (ROOT / 'docs/specifications/CosmicForge_Business_Property_Operations_Full_Backend_Implementation_Spec_v1.0.md').read_text(encoding='utf-8')
categories = {
    'Organisation & security': ('organisation/workforce/auth/approvals', '3'),
    'Finance & coding': ('finance', '15 / D.4 / D.37'),
    'Portfolio & authority': ('portfolio/authority', '4 / D.6'),
    'Content & listings': ('content', '6 / D.7'),
    'Inventory & rate products': ('inventory/revenue', '7'),
    'Distribution/channel manager': ('distribution', '6'),
    'CRM/marketing/support': ('crm/support', '11 / 14 / D.8 / D.22'),
    'Reservations & hospitality': ('reservations/hospitality', '9 / 10 / D.39'),
    'Residential/commercial/land': ('residential/commercial/land', 'D.9 / D.10 / D.11'),
    'Sales/development': ('sales/development', 'D.12 / D.13'),
    'Short-let/housekeeping': ('shortlet/housekeeping', 'D.14 / D.17'),
    'Maintenance/vendors': ('maintenance', 'D.20'),
    'Managers/professionals': ('delegation/professional', 'D.19 / D.21'),
    'Reviews/reputation': ('reputation', '13 / D.33'),
    'Analytics/RMS/AI': ('analytics/revenue/automation', '8 / D.23 / D.30 / D.34'),
    'Risk/privacy/access': ('risk/privacy/access/insurance/financing', 'D.24 / D.26 / D.27 / D.28'),
    'Platform': ('platform/events', '1 / 2 / 19 / 22'),
}
tables = {}
for migration in sorted((SERVICE / 'migrations').glob('*.sql')):
    source = migration.read_text(encoding='utf-8')
    for match in re.finditer(r'CREATE TABLE (\w+)\s*\((.*?)\);', source, re.S | re.I):
        name, body = match.groups()
        tables[name] = {
            'migration': migration.name,
            'primary_key': re.findall(r'PRIMARY KEY\s*(\([^)]*\))?', body, re.I),
            'foreign_key_count': len(re.findall(r'\bREFERENCES\s', body, re.I)),
            'organisation_column': bool(re.search(r'\borganisation_id\b', body)),
            'versioned': bool(re.search(r'\bversion\b', body)),
            'lifecycle': bool(re.search(r'\b(state|status|revoked_at|consumed_at|released_at)\b', body)),
            'indexes': re.findall(r'CREATE (?:UNIQUE )?INDEX (\w+) ON ' + re.escape(name) + r'\b', source, re.I),
            'immutable': bool(re.search(r'CREATE TRIGGER \w+ BEFORE UPDATE OR DELETE OR TRUNCATE ON ' + re.escape(name) + r'\b', source)),
            'unique_constraint_count': len(re.findall(r'\bUNIQUE\b', body)),
            'check_constraint_count': len(re.findall(r'\bCHECK\s*\(', body)),
        }

entries = []
section5 = spec.split('## 5. Database catalogue', 1)[1].split('## 6.', 1)[0]
seen = set()
for line in section5.splitlines():
    parts = [p.strip() for p in line.split('|')]
    if len(parts) < 4 or parts[1] not in categories:
        continue
    category = parts[1]
    owner, section = categories[category]
    for name in parts[2].split(', '):
        actual = tables.get(name)
        seen.add(name)
        entries.append({'specified_table': name, 'canonical_table': name, 'specification': 'Section 5', 'category': category, 'owner_package': owner, 'owning_section': section, 'decision': 'SCHEMA_PRESENT' if actual else 'DEFERRED_FUTURE_DOMAIN', 'schema': actual, 'repository': 'internal/' + owner.split('/')[0], 'dependency': 'Existing owned schema; transactional domain workflows are certified separately.' if actual else 'Create in the owning future-section migration with its real lifecycle/repository/tests; no empty placeholder table.', 'isolation': 'Organisation-safe local references; no cross-service SQL.' if actual and actual['organisation_column'] else 'Service-local/global registry or child identity; see parent foreign keys and domain enforcement.' if actual else 'Planned organisation-scoped references only; external services remain ID/API/event references.'})

# Annex shorthand is explicitly mapped rather than creating competing tables.
aliases = {'credentials': 'business_credentials', 'sessions': 'business_sessions', 'devices': 'business_devices', 'mfa_methods': 'business_mfa_methods', 'phases': 'development_phases', 'assignments': 'lead_assignments', 'activities': 'lead_activities', 'modifications': 'reservation_modifications', 'holds': 'inventory_holds', 'restrictions': 'rate_restrictions', 'checklists': 'housekeeping_checklists', 'capabilities': 'channel_capabilities', 'field_authority': 'channel_field_authority', 'sync_jobs': 'channel_sync_jobs', 'cursors': 'channel_sync_cursors', 'failures': 'channel_sync_failures', 'conflicts': 'channel_conflicts', 'waitlist': 'waitlist_entries', 'source_refs': 'channel_reservation_refs'}
annex = spec.split('## Implementation Annex D.', 1)[1].split('## Implementation Annex E.', 1)[0]
current = ''
for line in annex.splitlines():
    if line.startswith('### D.'):
        current = line[4:]
    if not line.startswith('| Primary tables |'):
        continue
    for name in line.split('|')[2].strip().split(', '):
        if name == 'property/product/rate mappings':
            names = ['channel_property_mappings', 'channel_product_mappings', 'channel_rate_mappings']
        else:
            names = [aliases.get(name, name)]
        for canonical in names:
            if canonical in seen:
                continue
            if not re.fullmatch('[a-z_]+', canonical):
                raise RuntimeError('Unmapped annex table: ' + canonical)
            seen.add(canonical)
            actual = tables.get(canonical)
            entries.append({'specified_table': name, 'canonical_table': canonical, 'specification': current, 'category': 'Annex supplement', 'owner_package': 'See ' + current, 'owning_section': current, 'decision': 'SCHEMA_PRESENT' if actual else 'DEFERRED_FUTURE_DOMAIN', 'schema': actual, 'repository': 'Domain package from authoritative annex', 'dependency': 'Mapped to existing canonical table; no duplicate identity.' if actual else 'Deferred with owning annex workflow; not required for executable Sections 6–7.', 'isolation': 'Local tenant-safe FK/API/event references only.'})

mapped = {e['canonical_table'] for e in entries}
for name, schema in sorted(tables.items()):
    if name not in mapped:
        entries.append({'specified_table': '(supporting table)', 'canonical_table': name, 'specification': 'Supporting implementation', 'category': 'Supporting persistence', 'owner_package': 'Existing owning migration/domain', 'owning_section': '9' if int(schema['migration'][:5]) == 8 else '3–7' if int(schema['migration'][:5]) >= 6 else '1–4', 'decision': 'SCHEMA_PRESENT', 'schema': schema, 'repository': 'Owning bounded context; no cross-service implementation imports.', 'dependency': 'Supporting secure/transactional operation, immutable history, normalized relationships or recovery.', 'isolation': 'Service-local; parent tenant keys and command authorization.'})
# Resolve each table to one owning context, including supporting persistence.
annex_owners={1:'organisation',2:'auth',3:'workforce',4:'finance',5:'portfolio',6:'portfolio',7:'content',8:'crm',9:'residential',10:'commercial',11:'land',12:'sales',13:'development',14:'shortlet',15:'portfolio',16:'reservations',17:'housekeeping',18:'revenue',19:'delegation',20:'maintenance',21:'professional',22:'support',23:'analytics',24:'risk',25:'organisation',26:'financing',27:'insurance',28:'access',29:'distribution',30:'revenue',31:'reservations',32:'crm',33:'reputation',34:'automation',35:'commerce',36:'integrations',37:'finance',38:'hospitality',39:'sales',40:'content',41:'workers',42:'risk'}
for entry in entries:
    name=entry['canonical_table']
    owner=None
    groups=[('auth',('business_auth_','business_external_','business_refresh_','business_registration_','business_accounts','business_credentials','business_sessions','business_devices','business_mfa','security_events','sso_','scim_','mfa_','refresh_','password_','authentication_','external_identity_')),
            ('organisation',('organisations','organisation_','capability_packs','verification_links','verification_link_history','business_account_profiles')),
            ('workforce',('staff_','branches','departments','teams','regions','role_','permissions')),
            ('approvals',('approval_','delegation_grants','break_glass_')),
            ('portfolio',('inventory_graph_versions','inventory_nodes','inventory_edges','inventory_edge_','inventory_history','inventory_external_','inventory_import_','inventory_verification_','inventory_media_','space_graphs','digital_twin_refs','portfolios','developments','development_phases','parcels','properties','buildings','blocks','floors','units','room_types','rooms','facilities','common_areas','assets')),
            ('inventory',('inventory_pools','inventory_pool_members','inventory_calendar','inventory_holds','inventory_hold_days','inventory_allotments','inventory_allotment_days','sellable_products')),
            ('reservations',('reservations','reservation_','waitlist_')),
            ('revenue',('rate_','promotions','add_ons','packages','package_components','price_quotes','bulk_rate_','tax_profiles','currency_policies')),
            ('content',('listing_templates','listings','listing_versions','listing_media_links','content_quality_scores','listing_approvals')),
            ('distribution',('channel_','listing_publication')),
            ('events',('event_failures','outbox_','inbox_','audit_events','commercial_history')),
            ('workers',('notification_jobs','notification_acceptances','worker_')),
            ('finance',('compliance_holds',))]
    for candidate,prefixes in groups:
        if any(name==prefix or prefix.endswith('_') and name.startswith(prefix) or prefix.startswith('business_mfa') and name.startswith(prefix) for prefix in prefixes):owner=candidate;break
    if owner is None:
        annex=re.match(r'D\.(\d+)',entry['owning_section'])
        owner=annex_owners[int(annex[1])] if annex else entry['owner_package'].split('/')[0]
    if owner in ('Existing owning migration','See ') or owner.startswith('See '):raise RuntimeError('Unresolved table owner '+name)
    entry['owner_package']='internal/'+owner
    entry['repository']='internal/repositories/db (SQLC); internal/'+owner+' owning command/query interfaces' if entry['schema'] else 'Planned internal/'+owner+' repository with owning-section migration'
    if entry['schema']:
        all_source='\n'.join(p.read_text(encoding='utf-8') for p in (SERVICE/'migrations').glob('*.sql'))
        entry['schema']['altered_in']=[p.name for p in sorted((SERVICE/'migrations').glob('*.sql')) if re.search(r'ALTER TABLE '+re.escape(name)+r'\b',p.read_text(encoding='utf-8'),re.I)]
        entry['schema']['indexes']=sorted(set(re.findall(r'CREATE (?:UNIQUE )?INDEX (\w+) ON '+re.escape(name)+r'\b',all_source,re.I)))
entries.sort(key=lambda e: (e['category'], e['canonical_table']))
data = {'latest_migration':sorted((SERVICE/'migrations').glob('*.sql'))[-1].name,'specification': 'CosmicForge_Business_Property_Operations_Full_Backend_Implementation_Spec_v1.0', 'aliases': aliases, 'entries': entries}
md = ['# Section 5: reconciled database catalogue', '', 'Generated from the complete Section 5 catalogue, Annex D table supplements and actual owned migrations. SCHEMA_PRESENT certifies presence, not unexecuted workflows. Future-domain tables are deliberately deferred, with no placeholder DDL. Run `python scripts/contracts/generate_business_database_catalogue.py --check` to detect drift. Integration tests verify implemented tables against real pg_catalog. SQLC generates typed query models; domain commands retain owning transactions and explicit scope checks.', '', '| Specified table | Canonical table | Owner / section | Decision | Migration | Tenant / FK / unique / check / indexes / immutability |', '|---|---|---|---|---|---|']
for e in entries:
    a = e['schema']
    detail = ('altered_in='+','.join(a['altered_in'])+'; tenant=' + str(a['organisation_column']) + '; FK=' + str(a['foreign_key_count']) + '; unique=' + str(a['unique_constraint_count']) + '; checks=' + str(a['check_constraint_count']) + '; indexes=' + ', '.join(a['indexes']) + '; immutable=' + str(a['immutable'])) if a else e['dependency']
    md.append('| ' + ' | '.join([e['specified_table'], e['canonical_table'], e['owner_package'] + ' / ' + e['owning_section'], e['decision'], a['migration'] if a else 'Future owning-section migration', detail]) + ' |')
outputs = {ROOT / 'docs/implementation/business-operations-database-catalogue.json': json.dumps(data, indent=2) + '\n', ROOT / 'docs/implementation/business-operations-database-catalogue.md': '\n'.join(md) + '\n'}
changed = []
for path, text in outputs.items():
    if not path.exists() or path.read_text(encoding='utf-8') != text:
        changed.append(str(path.relative_to(ROOT)))
        if '--check' not in sys.argv:
            path.write_text(text, encoding='utf-8', newline='\n')
if '--check' in sys.argv and changed:
    raise SystemExit('Catalogue drift: ' + ', '.join(changed))
print(f'{len(entries)} table decisions; {len(tables)} implemented tables; {sum(e["decision"] == "DEFERRED_FUTURE_DOMAIN" for e in entries)} future tables explicitly deferred')
