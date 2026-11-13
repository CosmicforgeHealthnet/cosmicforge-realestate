"""Reviewed executable contracts for Sections 12-24 handlers.

Contracts are derived from the real Go handlers (request type, authorizing
capability, success status and step-up requirement) with explicit, reviewed
overrides for compatibility aliases. A handler without a derivable contract
fails generation; no route is documented without an executable handler.
"""
import re
from pathlib import Path

from business_ops_domains import contract_entries, domain_notes

FILES = ['operational_comms.go', 'operational_comms_ai.go', 'operational_reputation.go', 'operational_marketing.go', 'operational_finance.go', 'operational_domains_hooks.go', 'operational_analytics.go']

# Reviewed overrides: compatibility aliases reuse owning schemas exactly.
OVERRIDES = {
 'projectList': ('development:read', None, 'List:Node', 200, False),
 'projectCreate': ('development:write', 'InventoryCreate', 'Node', 201, False),
 'projectPhases': ('development:read', None, 'List:Node', 200, False),
 'projectPhaseCreate': ('development:write', 'InventoryCreate', 'Node', 201, False),
 'taxPolicyGet': ('tax:read', None, 'List:CommercialTaxProfile', 200, False),
 'taxPolicyPut': ('tax:write', 'CommercialTaxCreate', 'CommercialTaxProfile', 201, True),
 'listingPreview': ('content:read', None, 'CommercialContentReadiness', 200, False),
 'listingSubmitApproval': ('content:write', 'CommercialStateChange', 'CommercialListing', 200, True),
 'listingQualityAlias': ('content:read', None, 'CommercialContentReadiness', 200, False),
 'authorityVerification': ('inventory:verify', 'InventoryVerification', 'VerificationProjection', 200, True),
 'authorityCreate': ('inventory:verify', 'AuthorityCreate', 'VerificationProjection', 200, True),
 'authorityList': ('property:read', None, 'S1224List', 200, False),
 'corporateRates': ('corporate:read', None, 'CRMRawList', 200, False),
 'corporateRateSave': ('corporate:write', 'S1224Object', 'CRMCorporateContract', 201, True),
 'privacyRequestCreate': ('guest_privacy:write', 'S1224Object', 'CRMPrivacyRequestView', 201, False),
 'rmsRecommendationApprove': ('rms:write', 'S1224Object', 'RMSRecommendation', 200, True),
 'rmsRecommendationOverride': ('rms:write', 'S1224Object', 'RMSRecommendation', 200, True),
 'reservationExtend': ('pms:write', 'PMSStayExtensionCommand', 'PMSStayExtensionResult', 201, True),
 'folioEnsure': ('folios:read', 'FolioEnsureCommand', 'S1224Object', 200, False),
 'saleOfferDecision': ('sales:approve', 'SaleOfferDecision', 'OpsRecord', 200, True),
 'integrationHealth': ('integrations:read', None, 'S1224Object', 200, False),
 'internalReviewReceipt': None, 'internalFunnelObservation': None, 'internalManagerAuthorization': None, 'internalDataRoomAuthorization': None,
}

NOTES = {
 'scheduled': 'Provider acknowledgement only: unknown outcomes stay UNKNOWN_PENDING_READBACK and are read back with the same client message id before any retry.',
}


def _bodies(text):
    out = {}
    for m in re.finditer(r'^func \(h \*Handler\) (\w+)\(w http\.ResponseWriter, r \*http\.Request\) \{\n(.*?)^\}', text, re.S | re.M):
        out[m[1]] = m[2]
    for m in re.finditer(r'^func \(h \*Handler\) (\w+)\(w http\.ResponseWriter, r \*http\.Request\) \{ (.*?) \}$', text, re.M):
        out[m[1]] = m[2]
    return out


def derive(root):
    service = root / 'services/business-operations-service/internal/http'
    bodies, helpers = {}, {}
    for f in FILES:
        text = (service / f).read_text(encoding='utf-8')
        bodies.update(_bodies(text))
        for m in re.finditer(r'^func \(h \*Handler\) (\w+)\(([^)]*)\)[^{]*\{\n(.*?)^\}', text, re.S | re.M):
            helpers[m[1]] = m[3]
    data = {}
    for name, body in bodies.items():
        if name in OVERRIDES:
            if OVERRIDES[name]:
                data[name] = OVERRIDES[name]
            continue
        req = re.search(r'var in (\w+)\n', body)
        caps = re.findall(r'"([a-z_]+:[a-z_]+)"', body)
        cap = caps[0] if caps else ''
        if not cap:
            # Thin wrappers delegate to a shared command with the capability.
            hb, seen = body, set()
            for _ in range(4):
                helper = next((x for x in re.findall(r'h\.(\w+)\(', hb) if x in helpers and x not in seen), None)
                if helper is None:
                    break
                seen.add(helper)
                hb = helpers[helper]
                c2 = re.findall(r'"([a-z_]+:[a-z_]+)"', hb)
                if c2:
                    cap = c2[0]
                    break
        statuses = [int(s) for s in re.findall(r'(?:return |reply\(w, r, )(20[0-2])', body)]
        status = 201 if 201 in statuses else 202 if 202 in statuses else 200
        mfa = bool(re.search(r'(execute|financeCommand|opsRecordProperty|Command)\([^\n]*,\s*true,', body)) or 'mfa := true' in body
        typed = re.findall(r'opsGet\[(\w+)\]', body)
        response = typed[-1] if typed else 'S1224Object'
        if req is None and ('opsList' in body or 'financeList' in body):
            response = 'S1224List'
        if not cap:
            raise RuntimeError('Sections 12-24 handler lacks a derivable contract: ' + name)
        data[name] = (cap, req[1] if req else None, response, status, mfa)
    data.update(contract_entries())
    return data


def register_sections_12_24(root, s, obj, arr, ref, typ):
    s['S1224Object'] = {'type': 'object', 'additionalProperties': True}
    s['S1224List'] = obj({'items': arr({'type': 'object', 'additionalProperties': True}), 'limit': {'type': 'integer'}, 'offset': {'type': 'integer'}}, ['items', 'limit', 'offset'], True)
    for name in ('OpsRecordList',):
        s[name] = obj({'items': arr({'type': 'object', 'additionalProperties': True}), 'limit': {'type': 'integer'}, 'offset': {'type': 'integer'}}, ['items', 'limit', 'offset'], True)
    # Raw JSON fields are free-form objects/arrays in typed schemas.
    for schema in s.values():
        for k, v in list(schema.get('properties', {}).items()):
            if v.get('$ref', '').endswith('/RawMessage'):
                schema['properties'][k] = {}
    return derive(root)


def notes(handler):
    n = domain_notes().get(handler)
    if n:
        return n
    if 'Scheduled' in handler or 'reply' in handler.lower():
        return NOTES['scheduled']
    return ''
