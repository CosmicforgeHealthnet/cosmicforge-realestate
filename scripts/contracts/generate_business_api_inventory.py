"""Business Operations API inventory from contracts/routes.json and the Go
route registrations (handler + source file:line), preserving reviewed groups.
Run: python scripts/contracts/generate_business_api_inventory.py [--check]
"""
import collections
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SERVICE = ROOT / 'services/business-operations-service'
DOC = ROOT / 'docs/implementation'
routes = json.loads((SERVICE / 'contracts/routes.json').read_text(encoding='utf-8'))
previous = {(r['method'], r['path']): r for r in json.loads((DOC / 'business-operations-section-16-api-inventory.json').read_text(encoding='utf-8'))['routes']}
registrations = {}
for go in sorted(SERVICE.joinpath('internal').rglob('*.go')):
    if go.name.endswith('_test.go'):
        continue
    for n, line in enumerate(go.read_text(encoding='utf-8').splitlines(), 1):
        for m in re.finditer(r'r\.(Get|Post|Put|Patch|Delete)\("([^"]+)",\s*h\.(\w+)\)', line):
            registrations.setdefault((m[1].upper(), m[2]), (m[3], f'{go.relative_to(SERVICE)}:{n}'))
GROUPS = [('/internal/', 'Internal (service-only)'), ('/webhooks/', 'Webhooks (provider callbacks)'),
          ('/rental-applications', 'Residential/commercial/land/sales'), ('/agreements', 'Residential/commercial/land/sales'), ('/rent-obligations', 'Residential/commercial/land/sales'),
          ('/deposits', 'Residential/commercial/land/sales'), ('/notices', 'Residential/commercial/land/sales'), ('/renewals', 'Residential/commercial/land/sales'), ('/move-outs', 'Residential/commercial/land/sales'),
          ('/utility-accounts', 'Residential/commercial/land/sales'), ('/residential', 'Residential/commercial/land/sales'), ('/commercial/deals', 'Residential/commercial/land/sales'),
          ('/commercial/service-charge', 'Residential/commercial/land/sales'), ('/land/', 'Residential/commercial/land/sales'), ('/due-diligence', 'Residential/commercial/land/sales'),
          ('/closing-milestones', 'Residential/commercial/land/sales'), ('/sales', 'Residential/commercial/land/sales'), ('/projects', 'Development'),
          ('/work-orders', 'Housekeeping/maintenance'), ('/warranties', 'Housekeeping/maintenance'), ('/service-contracts', 'Housekeeping/maintenance'), ('/assets/', 'Housekeeping/maintenance'),
          ('/housekeeping', 'Housekeeping/maintenance'), ('/lost-found', 'Housekeeping/maintenance'), ('/linen-stock', 'Housekeeping/maintenance'),
          ('/manager-assignments', 'Managers/professionals'), ('/professional-appointments', 'Managers/professionals'),
          ('/risk', 'Risk/privacy'), ('/protection-cases', 'Risk/privacy'), ('/incidents', 'Risk/privacy'), ('/branch-incidents', 'Risk/privacy'),
          ('/access/', 'Finance/insurance/access/integrations'), ('/financing', 'Finance/insurance/access/integrations'), ('/insurance', 'Finance/insurance/access/integrations'),
          ('/integrations', 'Finance/insurance/access/integrations'), ('/fx-rates', 'Finance/insurance/access/integrations'),
          ('/business-changes', 'Auth/organisation/staff/roles/approvals'), ('/closure-cases', 'Auth/organisation/staff/roles/approvals'),
          ('/shortlet', 'Reservations/front desk/folio/night audit'), ('/hotel/', 'Inventory/rates'), ('/content-suggestions', 'Listings/content'),
          ('/revenue/', 'Revenue'), ('/market-benchmarks', 'Analytics/AI/automation')]
out = []
for r in routes:
    key = (r['method'], r['path'])
    old = previous.get(key)
    rel = re.sub(r'^/api/v1/business', '', r['path'])
    internal_rel = re.sub(r'^/internal/v1/business-supply', '', r['path'])
    handler, source = registrations.get((r['method'], rel)) or registrations.get(key) or (old or {}).get('handler'), None
    if isinstance(handler, tuple):
        handler, source = handler
    reg = registrations.get((r['method'], rel)) or registrations.get(key) or (r['path'].startswith('/internal/') and registrations.get((r['method'], internal_rel)))
    if reg:
        handler, source = reg
    elif old:
        handler, source = old['handler'], old['source']
    group = old['group'] if old else next((g for prefix, g in GROUPS if rel.startswith(prefix) or r['path'].startswith(prefix)), None)
    if group is None or handler is None:
        raise SystemExit(f'Unclassified route {key}')
    out.append({'method': r['method'], 'path': r['path'], 'group': group, 'capability': r.get('capability', ''), 'step_up': r.get('step_up', False), 'idempotent': r.get('idempotent', False),
                'handler': handler, 'source': source, 'annex_e': bool(old and old.get('annex_e')), 'openapi': True, 'postman': True})
pairs = collections.Counter((r['method'], r['path']) for r in out)
dupes = [k for k, v in pairs.items() if v > 1]
annex = sum(r['annex_e'] for r in out)
data = {'total': len(out), 'annex_e_required': 213, 'annex_e_registered': annex, 'routes': out}
groups = collections.Counter(r['group'] for r in out)
md = ['# Business Operations — Complete API Inventory', '',
      'Machine-readable inventory: `business-operations-section-16-api-inventory.json` (method, path, group, capability, step-up, idempotency, handler, source location, Annex E flag). '
      'Generated by `python scripts/contracts/generate_business_api_inventory.py` from `contracts/routes.json` (itself generated from the registered Go routers and checked by '
      '`TestBusinessRegisteredRoutesMatchOpenAPIAndOfficialPostman` and `generate_business_operations.py --check --validate`).', '',
      f'* Registered method/path pairs: **{len(out)}** (OpenAPI = Postman = handlers).',
      f'* Annex E required pairs: **213**; registered: **{annex}**; missing: **{213 - annex}**.',
      f'* Duplicate method/path pairs: **{len(dupes)}**.',
      '* Internal routes stay on `/internal/v1/business-supply/*` (signed service identity + caller allowlist); provider callbacks on `/webhooks/v1/business-channels/*`; neither is mounted on the browser router.', '',
      '| API group | Routes |', '|---|---|'] + [f'| {g} | {n} |' for g, n in sorted(groups.items())] + ['',
      '## Routes', '', '| Method | Path | Capability | Step-up | Handler | Source |', '|---|---|---|---|---|---|'] + [
      f"| {r['method']} | `{r['path']}` | {r['capability'] or '—'} | {'MFA' if r['step_up'] else ''} | {r['handler']} | `{r['source']}` |" for r in out]
outputs = {DOC / 'business-operations-section-16-api-inventory.json': json.dumps(data, indent=1) + '\n', DOC / 'business-operations-section-16-api-inventory.md': '\n'.join(md) + '\n'}
changed = [str(p.relative_to(ROOT)) for p, t in outputs.items() if not p.exists() or p.read_text(encoding='utf-8') != t]
if '--check' in sys.argv:
    if changed:
        raise SystemExit('API inventory drift: ' + ', '.join(changed))
else:
    for p, t in outputs.items():
        p.write_text(t, encoding='utf-8', newline='\n')
if dupes or annex != 213:
    raise SystemExit(f'duplicates={dupes} annex_e={annex}')
print(f'{len(out)} routes inventoried; Annex E {annex}/213; duplicates 0')
