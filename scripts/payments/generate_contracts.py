"""Generate the controlled Sections 1-10 OpenAPI, Postman and event contracts.

Only implemented HTTP surfaces are listed. Run from the repository root.
Contracts use JSON syntax (valid YAML) for reproducible native serialization.
"""
import json
import sys
sys.dont_write_bytecode = True
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
UUID = {"type": "string", "format": "uuid"}
TEXT = {"type": "string", "minLength": 1}
DATE = {"type": "string", "format": "date-time"}
MONEY = {"type": "integer", "format": "int64", "minimum": 1}
BOOL = {"type": "boolean"}
CURRENCY = {"type": "string", "pattern": "^[A-Z]{3}$", "description": "Validated ISO 4217 currency; integer minor units."}


def obj(props, required=None):
    return {"type": "object", "properties": props, "required": list(props) if required is None else required, "additionalProperties": False}


def ref(name):
    return {"$ref": "#/components/schemas/" + name}


def array(item):
    return {"type": "array", "items": item}


SCHEMAS = {
    "Provision": obj(dict(domain=TEXT, account_id=UUID, role=TEXT, party_kind={"type": "string", "enum": ["PERSON", "ORGANIZATION"]}, legal_entity=TEXT, currency=CURRENCY, registration_ref=TEXT)),
    "Wallet": obj(dict(wallet_id=UUID, identity_id=UUID, domain=TEXT, role=TEXT, legal_entity=TEXT, currency=CURRENCY, state={"type": "string", "enum": ["ACTIVE", "RESTRICTED", "CLOSED"]})),
    "Balance": obj(dict(kind={"type": "string", "enum": ["PAYMENT_PENDING", "PENDING_EARNINGS", "PROTECTED", "AVAILABLE", "FROZEN", "WITHDRAWAL_PENDING", "REFUND_PENDING"]}, pool_id=UUID, amount_minor={"type": "string", "pattern": "^-?[0-9]+$", "description": "Exact aggregate minor units without JSON precision loss."}, withdrawable=BOOL)),
    "History": obj(dict(entry_id=UUID, journal_id=UUID, operation_ref=TEXT, kind=TEXT, side={"type": "string", "enum": ["DEBIT", "CREDIT"]}, amount_minor=MONEY, currency=CURRENCY, created_at=DATE)),
    "Link": obj(dict(identity_id=UUID, domain=TEXT, account_id=UUID, evidence_ref=TEXT)),
    "Verification": obj(dict(verification_ref=TEXT, state={"type": "string", "enum": ["VERIFIED", "REVOKED", "EXPIRED"]}, expires_at=DATE)),
    "IntentInput": obj(dict(wallet_id=UUID, amount_minor=MONEY, currency=CURRENCY, country=TEXT, category=TEXT, evidence_ref=TEXT, policy_version=TEXT, email={"type": "string", "format": "email"}, requires_custody=BOOL, provider_constraint=UUID), ["wallet_id", "amount_minor", "currency", "country", "category", "evidence_ref", "policy_version", "email"]),
    "Intent": obj(dict(intent_id=UUID, wallet_id=UUID, amount_minor=MONEY, currency=CURRENCY, legal_entity=TEXT, provider_account_id=UUID, pool_id=UUID, reference=TEXT, state={"type": "string", "enum": ["REQUESTED", "SUBMITTING", "INITIALIZED", "PENDING", "PROCESSING", "VERIFIED_SUCCESS", "VERIFIED_FAILURE", "UNKNOWN_OUTCOME", "RECONCILIATION_EXCEPTION","CANCELLED"]}, settlement_state={"type": "string", "enum": ["PENDING", "SETTLED", "EXCEPTION"]}, checkout_url={"type": "string", "format": "uri"}), ["intent_id", "wallet_id", "amount_minor", "currency", "legal_entity", "provider_account_id", "pool_id", "reference", "state", "settlement_state"]),
    "Customer": obj(dict(email=TEXT, first_name=TEXT, last_name=TEXT, phone={"type": "string"}), ["email", "first_name", "last_name"]),
    "Validation": obj(dict(country={"const": "NG"}, type={"const": "bank_account"}, account_number=TEXT, bvn=TEXT, bank_code=TEXT, first_name={"type": "string"}, last_name={"type": "string"}), ["country", "type", "account_number", "bvn", "bank_code"]),
    "DVAInput": obj(dict(wallet_id=UUID, provider_account_id=UUID, consent_ref=TEXT, consent_version=TEXT, consent_at=DATE, consent_expires_at=DATE, verification_ref=TEXT, customer=ref("Customer"), validation=ref("Validation"))),
    "DVA": obj(dict(virtual_account_id=UUID, wallet_id=UUID, state={"type": "string", "enum": ["REQUESTED", "CUSTOMER_UNKNOWN", "VALIDATION_REQUIRED", "ASSIGNING", "ASSIGNMENT_UNKNOWN", "ACTIVE", "RESTRICTED", "REJECTED", "INACTIVE"]}, account_mask=TEXT, bank_code=TEXT), ["virtual_account_id", "wallet_id", "state"]),
    "Evidence": obj(dict(evidence_ref=TEXT)),
    "SettlementRequest": obj(dict(reference=TEXT)),
    "Coverage": obj(dict(pool_id=UUID, settled_cash_minor={"type": "string"}, receivable_minor={"type": "string"}, liability_minor={"type": "string"}, reserve_minor={"type": "integer", "format": "int64"}, restricted=BOOL, custody_approved=BOOL)),
    "Error": {"type": "object", "required": ["error"], "properties": {"error": {"type": "object", "required": ["code", "message"], "properties": {"code": TEXT, "message": TEXT, "request_id": {"type": "string"}, "details": {"type": "array"}, "outcome": {"type": "string"}}}}},
}

INTERNAL = [
    ("POST", "/wallets/ensure", "Provision", ref("Wallet"), "Account authority for the requested domain; registration evidence required. Idempotent by domain/account/role/entity/currency."),
    ("GET", "/wallets/{id}", None, ref("Wallet"), "Source-bound delegated wallet owner."),
    ("GET", "/wallets/{id}/balances", None, array(ref("Balance")), "Source-bound delegated wallet owner; provider pools remain distinct; deposits included in PROTECTED once."),
    ("GET", "/wallets/{id}/transactions", None, array(ref("History")), "Source-bound delegated wallet owner; use last entry_id as after cursor, limit 1-100."),
    ("POST", "/financial-identities/link", "Link", obj(dict(linked=BOOL)), "Verification service only; verified cross-domain legal-party evidence. Existing identities are never merged."),
    ("PUT", "/financial-identities/{id}/verification", "Verification", obj(dict(recorded=BOOL)), "Verification service only; authoritative status with expiry/revocation; idempotent update."),
    ("POST", "/payment-intents", "IntentInput", ref("Intent"), "Domain authority with delegated wallet owner, accepted evidence and policy. Durable Idempotency-Key; conflicting fingerprints rejected. Test mode only."),
    ("GET", "/payment-intents/{id}", None, ref("Intent"), "Original source service and delegated wallet owner."),
    ("POST", "/payment-intents/{id}/verify", None, ref("Intent"), "Original source service and delegated wallet owner. Idempotent economic effect; read-back validates reference/amount/currency/merchant/intent/mode. Does not settle or release funds."),
    ("POST", "/virtual-accounts", "DVAInput", ref("DVA"), "Domain authority with delegated wallet owner; consent and authoritative KYC, approved test merchant. Idempotent by wallet/provider with fingerprint; repeat unknown customer request performs read-back."),
    ("GET", "/virtual-accounts/{id}", None, ref("DVA"), "Source-bound delegated wallet owner; masked account only."),
    ("POST", "/virtual-accounts/{id}/sync", None, ref("DVA"), "Domain authority with delegated owner. Provider identification before assignment; requery/fetch for existing account. Unknown assignment uses read-back."),
    ("POST", "/virtual-accounts/{id}/deactivate", None, ref("DVA"), "Domain authority with delegated owner; provider deactivation followed by status read-back."),
    ("POST", "/provider-inbox/{id}/replay", "Evidence", obj(dict(requeued=BOOL)), "Admin service only; audit evidence required; active worker lease cannot be overridden; original references retained."),
    ("GET", "/custody-pools/{id}/coverage", None, ref("Coverage"), "Admin service only; raw merchant observations never represented as individual wallet money or licensed custody."),
    ("POST", "/provider-accounts/{id}/reconcile", None, obj(dict(reconciled=BOOL)), "Admin service only; external merchant balance read-back; mismatches restrict the pool and publish an exception."),
    ("POST", "/provider-accounts/{id}/settlements", "SettlementRequest", obj(dict(observed=BOOL)), "Admin service only; immutable external settlement batch observation. Does not credit settled cash or change an intent to SETTLED."),
]
SCHEMAS.update({
    "IdentityControl": obj(dict(state={"enum": ["ACTIVE", "RESTRICTED", "CLOSED"]}, evidence_ref=TEXT, policy_version=TEXT, approvers=array(TEXT)), ["state", "evidence_ref", "policy_version"]),
    "PayoutReferenceInput": obj(dict(wallet_id=UUID, beneficiary_ref=TEXT, verification_ref=TEXT, jurisdiction={"type": "string", "minLength": 2, "maxLength": 2}, expires_at=DATE)),
    "PayoutReference": obj(dict(payout_account_id=UUID, wallet_id=UUID, beneficiary_ref=TEXT, verification_ref=TEXT, jurisdiction=TEXT, expires_at=DATE, eligible={"const": False})),
})
SCHEMAS["IntentInput"]["properties"]["required_liquidity_minor"] = {"type": "integer", "format": "int64", "minimum": 0, "description": "Required reconciled provider liquidity for routing; zero for collection-only requests. Configuration is policy input, not custody proof."}
INTERNAL.extend([
    ("PUT", "/financial-identities/{id}/state", "IdentityControl", obj(dict(recorded=BOOL)), "Admin service only; evidence and policy required. Restoration requires two distinct recorded approvers. Closure rejects outstanding liabilities/intents and retains history."),
    ("POST", "/payout-account-references", "PayoutReferenceInput", ref("PayoutReference"), "Verification service only; current authoritative verification, exact beneficiary reference, jurisdiction and expiry. Records bank verification only; no financial entitlement or payout execution. Eligible remains false."),
    ("GET", "/wallets/{id}/payout-account-references", None, array(ref("PayoutReference")), "Source-bound delegated wallet owner; bank reference only, no raw bank details. Recording a reference does not authorize withdrawals."),
])
from lifecycle_contracts import extend
LIFECYCLE_PUBLIC = extend(ROOT, SCHEMAS, INTERNAL, obj, ref, array, TEXT, UUID)
from remedy_contracts import extend as extend_remedies
REMEDY_PUBLIC=extend_remedies(SCHEMAS,INTERNAL,obj,ref,array,TEXT)
PUBLIC = [(m, p, b, out, desc) for m, p, b, out, desc in INTERNAL if m == "GET" and (p.startswith("/wallets/") or p.startswith("/payment-intents/") or p.startswith("/virtual-accounts/"))]
PUBLIC.extend(LIFECYCLE_PUBLIC)
PUBLIC.extend(REMEDY_PUBLIC)
PUBLIC=list({(r[0],r[1]):r for r in PUBLIC}.values())
PUBLIC = [route for route in PUBLIC if not route[1].endswith("/payout-account-references")]
PUBLIC.insert(0, ("GET", "/wallets/me", None, array(ref("Wallet")), "Authenticated user through gateway; signed actor domain/account only. No actor supplied through client headers."))
ERROR_CODES = sorted(set(__import__("re").findall(r'(?:Fail\(\s*\d+,\s*|fail\(\s*)"([A-Z_]+)"', "\n".join(p.read_text(encoding='utf-8') for p in (ROOT / "services/payment-service/internal").rglob("*.go") if not p.name.endswith("_test.go")))))


def spec(title, prefix, routes, internal):
    paths = {}
    for method, path, body, output, description in routes:
        op = {"summary": method + " " + path, "operationId": (method + prefix + path).replace("/", "_").replace("{", "").replace("}", "").replace("-", "_"), "description": description, "security": [{"ServiceJWT" if internal else "UserJWT": []}], "responses": {"200": {"description": "Authoritative result (test mode; capability status is explicit)", "content": {"application/json": {"schema": obj({"data": output})}}}}, "parameters": []}
        for parameter in __import__("re").findall(r"\{([^}]+)\}",path):
            op["parameters"].append({"name":parameter,"in":"path","required":True,"schema":UUID})
        if path.endswith("/transactions"):
            op["parameters"].extend([{"name": "after", "in": "query", "schema": UUID}, {"name": "limit", "in": "query", "schema": {"type": "integer", "minimum": 1, "maximum": 100, "default": 50}}])
        if method == "POST" and path in ["/payment-intents", "/locked-quotes", "/allocation-lines/{id}/hold", "/allocation-releases", "/withdrawals", "/financial-cases", "/financial-case-facts", "/financial-case-decisions", "/refunds", "/customer-topups", "/wallet-spends","/recoverable-actions","/intent-cancellations"]:
            op["parameters"].append({"name": "Idempotency-Key", "in": "header", "required": True, "schema": {"type": "string", "minLength": 1, "maxLength": 128}})
        if body:
            op["requestBody"] = {"required": True, "content": {"application/json": {"schema": ref(body)}}}
        for code in [400, 401, 403, 404, 409, 413, 415, 422, 500, 503]:
            op["responses"][str(code)] = {"description": "Platform error envelope; financial safety/error codes in x-error-codes.", "content": {"application/json": {"schema": ref("Error")}}}
        paths.setdefault(prefix + path, {})[method.lower()] = op
    return {"openapi": "3.1.0", "info": {"title": title, "version": "1.0.0", "description": "Blueprint v1.0 Sections 1-10. Live custody, collections and payouts disabled; explicitly approved test-mode payout workflow available. Internal service JWT audience payment-service. Public surface requires gateway user JWT; backend validates signed gateway delegation in X-Service-Authorization. There is no arbitrary balance or journal mutation endpoint."}, "paths": paths, "components": {"schemas": SCHEMAS, "securitySchemes": {"ServiceJWT": {"type": "http", "scheme": "bearer", "bearerFormat": "Ed25519 service JWT"}, "UserJWT": {"type": "http", "scheme": "bearer", "bearerFormat": "domain access JWT"}}}, "x-error-codes": ERROR_CODES}


def write(path, data):
    p = ROOT / path
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")


write("contracts/internal/openapi/payment-service.yaml", spec("Payment Service internal foundation", "/internal/v1", INTERNAL, True))
write("contracts/public/openapi/payments.yaml", spec("Payment wallet public API", "/api/v1/payments", PUBLIC, False))
webhook = spec("Paystack durable webhook", "", [("POST", "/webhooks/v1/paystack/{id}/callbacks", None, obj(dict(received=BOOL)), "Gateway service JWT plus Paystack HMAC-SHA512 over raw bytes using the selected merchant secret. Acknowledge only after durable insertion; duplicates retain retry state. Maximum 64 KiB. No raw card/BVN payload persisted.")], True)
operation = webhook["paths"]["/webhooks/v1/paystack/{id}/callbacks"]["post"]
operation["parameters"].append({"name": "X-Paystack-Signature", "in": "header", "required": True, "schema": TEXT})
operation["requestBody"] = {"required": True, "content": {"application/json": {"schema": {"type": "object", "required": ["event", "data"], "properties": {"event": TEXT, "data": {"type": "object"}}}}}}
webhook["components"]["securitySchemes"]["ServiceJWT"] = {"type": "apiKey", "in": "header", "name": "X-Service-Authorization", "description": "Bearer gateway service JWT; audience payment-service."}
write("contracts/webhooks/paystack.yaml", webhook)

items = []
def example(schema, name="value"):
    schema = dict(schema)
    if isinstance(schema.get("type"), list): schema["type"] = schema["type"][0]
    if "$ref" in schema:
        return example(SCHEMAS[schema["$ref"].split("/")[-1]], name)
    if "const" in schema: return schema["const"]
    if "enum" in schema: return schema["enum"][0]
    if schema.get("type") == "object":
        return {k: example(v, k) for k, v in schema.get("properties", {}).items() if k in schema.get("required", [])}
    if schema.get("type") == "array": return []
    if schema.get("type") == "integer": return 10000
    if schema.get("type") == "boolean": return False
    fixed = {"domain": "individual-owner", "role": "OWNER", "party_kind": "PERSON", "legal_entity": "cosmicforge-ng", "currency": "NGN", "country": "NG", "jurisdiction": "NG", "category": "RENT", "policy_version": "v1"}
    return fixed.get(name, "{{" + name + "}}")
for prefix, routes, token in [("/internal/v1", INTERNAL, "service_token"), ("/api/v1/payments", PUBLIC, "user_token")]:
    for method, path, body, output, description in routes:
        url = ("{{gateway_base_url}}" if prefix.startswith("/api/") else "{{payment_base_url}}") + prefix + path.replace("{id}", "{{entity_id}}").replace("{pool}","{{pool_id}}")
        headers = [{"key": "Authorization", "value": "Bearer {{" + token + "}}"}]
        request = {"method": method, "header": headers, "url": url, "description": description}
        if body:
            request["header"].append({"key": "Content-Type", "value": "application/json"})
            request["body"] = {"mode": "raw", "raw": json.dumps(example(SCHEMAS[body]), indent=2), "options": {"raw": {"language": "json"}}}
        if method == "POST" and path in ["/payment-intents", "/locked-quotes", "/allocation-lines/{id}/hold", "/allocation-releases", "/withdrawals", "/financial-cases", "/financial-case-facts", "/financial-case-decisions", "/refunds", "/customer-topups", "/wallet-spends","/recoverable-actions","/intent-cancellations"]:
            request["header"].append({"key": "Idempotency-Key", "value": "{{idempotency_key}}"})
        items.append({"name": method + " " + prefix + path, "request": request})
items.append({"name": "POST Paystack callbacks", "request": {"method": "POST", "url": "{{gateway_base_url}}/webhooks/v1/paystack/{{entity_id}}/callbacks", "header": [{"key": "X-Paystack-Signature", "value": "{{raw_body_hmac_sha512}}"}], "description": "Use exact signed provider body; gateway injects its service JWT. No fake signature fallback."}})
write("postman/payment-service.postman_collection.json", {"info": {"name": "Payment Service Sections 1-10", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"}, "item": items, "variable": [{"key": "payment_base_url", "value": "http://localhost:8094"}, {"key": "gateway_base_url", "value": "http://localhost:8080"}, {"key": "entity_id", "value": ""},{"key":"pool_id","value":""}, {"key": "idempotency_key", "value": ""}]})

payloads = {
    "finance.wallet.opened.v1": ref("Wallet"),
    "finance.payment.intent_created.v1": ref("Intent"),
    "finance.payment.collected.v1": obj(dict(intent_id=UUID, amount_minor=MONEY, currency=CURRENCY, settlement_state={"const": "PENDING"})),
    "finance.journal.posted.v1": obj(dict(journal_id=UUID, operation_ref=TEXT, currency=CURRENCY, legal_entity=TEXT)),
    "finance.virtual_account.assigned.v1": obj(dict(virtual_account_id=UUID, wallet_id=UUID, bank_code=TEXT, account_mask=TEXT)),
    "finance.wallet.funding_pending.v1": obj(dict(receipt_id=UUID, wallet_id=UUID, amount_minor=MONEY, currency=CURRENCY, settlement_state={"const": "PENDING"})),
    "finance.reconciliation.exception_detected.v1": {"type": "object", "required": ["reason"], "properties": {"exception_id": UUID, "pool_id": UUID, "intent_id": UUID, "reason": TEXT}},
}
payloads["finance.identity.state_changed.v1"] = obj(dict(identity_id=UUID, state={"enum": ["ACTIVE", "RESTRICTED", "CLOSED"]}, evidence_ref=TEXT, policy_version=TEXT))
payloads["finance.allocation_created.v1"]=ref("Allocation")
payloads["finance.allocation_released.v1"]=obj(dict(line_id=UUID,amount_minor=MONEY,currency=CURRENCY,journal_id=UUID))
payloads["finance.platform_commission_allocated.v1"]=payloads["finance.allocation_released.v1"]
payloads["finance.rent_obligation_created.v1"]=ref("Obligation")
payloads["finance.rent_payment_verified.v1"]=obj(dict(obligation_id=UUID,intent_id=UUID,amount_minor=MONEY,currency=CURRENCY,settlement_state={"const":"PENDING"}))
payloads["finance.rent_split_pending.v1"]=obj(dict(obligation_id=UUID,intent_id=UUID,allocation_id=UUID,amount_minor=MONEY,currency=CURRENCY))
for event in ["withdrawal_reserved","payout_initiated","payout_confirmed","payout_failed","payout_resolution_required"]:
    payloads["finance."+event+".v1"]=ref("Payout")
payloads.update({
 "finance.provider_state_changed.v1":ref("StateChange"),
 "finance.recoverable_action.v1":ref("RecoverableAction"),
 "finance.deposit_registered.v1":ref("Deposit"),
 "finance.case_opened.v1":ref("FinancialCase"),
 "finance.case_decided.v1":ref("CaseDecision"),
 "finance.refund_reserved.v1":ref("RefundAttempt"),
 "finance.refund_state_changed.v1":ref("RefundAttempt"),
 "finance.financial_reversed.v1":ref("ReversalRecord"),
 "finance.destination_changed.v1":ref("DestinationChange"),
 "finance.customer_funded.v1":obj(dict(topup_id=UUID,wallet_id=UUID,amount_minor=MONEY,currency=CURRENCY,journal_id=UUID)),
 "finance.wallet_spent.v1":ref("Allocation"),
 "finance.reconciliation_completed.v1":ref("ReconciliationRun"),
})

def referenced_schemas(payload):
    needed = {}
    def visit(value):
        if isinstance(value, dict):
            target = value.get("$ref", "")
            if target.startswith("#/components/schemas/"):
                name = target.rsplit("/", 1)[-1]
                if name not in needed:
                    needed[name] = SCHEMAS[name]
                    visit(needed[name])
            for child in value.values(): visit(child)
        elif isinstance(value, list):
            for child in value: visit(child)
    visit(payload)
    return needed

for event, payload in payloads.items():
    schema = {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object", "allOf": [{"$ref": "../../schemas/event-envelope.schema.json"}], "properties": {"event_type": {"const": event}, "producer": {"const": "payment-service"}, "payload": payload}, "components": {"schemas": referenced_schemas(payload)}}
    write("contracts/events/payments/" + event + ".schema.json", schema)

print(f"Generated {len(INTERNAL) + len(PUBLIC) + 1} routes and {len(payloads)} event schemas.")
