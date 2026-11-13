#!/usr/bin/env python3
"""Contracts for the domainkit services (marketplace customer side,
property-manager, artisan, professional-services, admin).

Generates, from one reviewed description per route and per event:
  contracts/public/openapi/<domain>.yaml          gateway actor/public API
  contracts/internal/openapi/<service>-domain.yaml signed internal API
  contracts/events/<dir>/<type>.schema.json       produced event schemas
  postman folders 01, 04, 05, 06, 07

The route lists are verified against the Go registrations (App.Public,
App.Actor, App.Internal closures plus the shared domainkit account routes):
an undocumented or stale route fails --check, as does any drift in output.
"""
import glob
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
UUID = {"type": "string", "format": "uuid"}
TIME = {"type": "string", "format": "date-time"}
DATE = {"type": "string", "format": "date"}
S, I, B = {"type": "string"}, {"type": "integer"}, {"type": "boolean"}
UUIDS = {"type": "array", "items": UUID, "maxItems": 20}

ACCOUNT = [
    ("POST", "/auth/register", "public", "Register an account of this domain (no privileged role is self-granted).", {"email": S, "password": S, "display_name": S}, 201),
    ("POST", "/auth/login", "public", "Log in; returns access and refresh tokens.", {"email": S, "password": S}, 200),
    ("POST", "/auth/refresh", "public", "Rotate the refresh token (reuse revokes the session).", {"refresh_token": S}, 200),
    ("GET", "/me", "account", "The authenticated account of this domain.", None, 200),
    ("POST", "/auth/logout", "account", "Revoke the current session.", None, 200),
]

SERVICES = {
    "marketplace": {
        "service": "marketplace-service", "folder": "01 Marketplace", "role": "CUSTOMER", "glob": "services/marketplace-service/internal/customer/*.go",
        "title": "Marketplace customer API",
        "description": "Customer accounts, discovery over Marketplace's listing projection, and checkout journeys orchestrated through the owning services. Marketplace never decides price, stock, reservation, confirmation or money: quotes, holds and reservations are Business decisions, collection is a Business request to the Payment Service, and the customer pays on the Payment intent's hosted checkout (GET /api/v1/payments/payment-intents/{id}).",
        "routes": [
            ("GET", "/listings", "public", "Search published listings (query: q, date_from, date_to). Availability and totals are indicative; the canonical price is the Business quote.", None, 200),
            ("GET", "/listings/{id}", "public", "Listing detail with the projected availability calendar.", None, 200),
            ("POST", "/listings/{id}/views", "public", "Record a listing view (client UUIDv7 view_id) for delivery to Business funnel analytics.", {"view_id": UUID}, 202),
            ("POST", "/quotes", "actor", "Ask Business for the canonical quote of a stay; opens a QUOTED checkout.", {"listing_id": UUID, "date_from": DATE, "date_to": DATE, "adults": I, "children": I, "quantity": I}, 201),
            ("GET", "/checkouts", "actor", "The customer's checkouts.", None, 200),
            ("GET", "/checkouts/{id}", "actor", "Checkout reconciled with the Business reservation readback.", None, 200),
            ("POST", "/checkouts/{id}/hold", "actor", "Create the canonical HOLD reservation from the quote through the signed channel-receipt contract, and assert the customer identity to Business.", {"version": I}, 200),
            ("POST", "/checkouts/{id}/pay", "actor", "With explicit consent: provision the customer's own Payment wallet and ask Business to collect the held obligation (Business confirms after its own Payment readback).", {"consent": B}, 202),
            ("POST", "/checkouts/{id}/cancel", "actor", "Cancel a quote locally, or ask Business to cancel the reservation under its source-cancellation policy.", {"version": I}, 200),
            ("POST", "/checkouts/{id}/review", "actor", "Verified-stay review, only after Business reports the stay completed; accepted into Business reputation first.", {"rating": I, "body": S}, 201),
        ],
        "internal": [],
        "events": {
            "marketplace.checkout.updated.v1": ("checkout", {"checkout_id": UUID, "listing_id": UUID, "organisation_id": UUID, "property_id": UUID, "state": S, "business_reservation_id": UUID, "business_state": S, "financial_state": S, "version": I}, ["checkout_id", "state"]),
            "marketplace.review.submitted.v1": ("stay_review", {"review_id": UUID, "checkout_id": UUID, "organisation_id": UUID, "property_id": UUID, "rating": I}, ["review_id", "checkout_id", "rating"]),
        },
    },
    "property-manager": {
        "service": "property-manager-service", "folder": "04 Property Manager", "role": "PROPERTY_MANAGER", "glob": "services/property-manager-service/internal/usecases/*.go",
        "title": "Property Manager API",
        "description": "Mandates projected from Business manager assignments. Acceptance, capability decisions and delegated actions are re-validated live by Business; revocation, suspension and expiry follow Business events.",
        "routes": [
            ("GET", "/mandates", "actor", "The manager's mandates.", None, 200),
            ("GET", "/mandates/{id}", "actor", "One mandate.", None, 200),
            ("POST", "/mandates/{id}/accept", "actor", "Accept an offered mandate (Business readback must be ACTIVE and unexpired).", None, 200),
            ("POST", "/mandates/{id}/decline", "actor", "Decline an offered mandate.", None, 200),
            ("GET", "/mandates/{id}/authorization", "actor", "Live Business capability decision (query: capability).", None, 200),
            ("POST", "/mandates/{id}/maintenance-requests", "actor", "Delegated maintenance request, re-validated by Business against the live delegation.", {"kind": S, "title": S, "subject_id": UUID, "reason": S}, 201),
        ],
        "internal": [
            ("GET", "/mandates/by-assignment/{id}", "Mandate projection for a Business assignment (business-operations-service, admin-service)."),
            ("POST", "/accounts/{id}/status", "Suspend or reactivate a manager account (business-operations-service, admin-service)."),
        ],
        "events": {
            "property_manager.mandate.updated.v1": ("mandate", {"mandate_id": UUID, "business_assignment_id": UUID, "organisation_id": UUID, "state": S, "business_state": S, "action": S, "version": I}, ["mandate_id", "action"]),
            "property_manager.action.recorded.v1": ("mandate", {"mandate_id": UUID, "business_assignment_id": UUID, "organisation_id": UUID, "action": S, "business_record_id": UUID}, ["mandate_id", "action"]),
        },
    },
    "artisan": {
        "service": "artisan-service", "folder": "05 Artisan", "role": "ARTISAN", "glob": "services/artisan-service/internal/usecases/*.go",
        "title": "Artisan API",
        "description": "Work and quote requests projected from Business. Acceptance, arrival, completion, staged Media evidence, quotes and warranties are reported to Business before the local commit.",
        "routes": [
            ("GET", "/work-requests", "actor", "The artisan's work requests (query: state).", None, 200),
            ("GET", "/work-requests/{id}", "actor", "One work request.", None, 200),
            ("POST", "/work-requests/{id}/accept", "actor", "Accept an offered assignment.", {"version": I, "reason": S}, 200),
            ("POST", "/work-requests/{id}/decline", "actor", "Decline an offered assignment.", {"version": I, "reason": S}, 200),
            ("POST", "/work-requests/{id}/arrive", "actor", "Record arrival (BEFORE evidence required).", {"version": I, "reason": S}, 200),
            ("POST", "/work-requests/{id}/complete", "actor", "Record completion (AFTER evidence required) with the workmanship warranty.", {"version": I, "warranty_days": I, "reason": S}, 200),
            ("POST", "/work-requests/{id}/evidence", "actor", "Attach Media evidence to a stage (BEFORE, DURING, AFTER).", {"stage": S, "media_reference": UUID, "note": S}, 201),
            ("GET", "/quote-requests", "actor", "The artisan's quote requests.", None, 200),
            ("POST", "/quote-requests/{id}/submit", "actor", "Submit a priced quote with evidence.", {"amount_minor": I, "currency": S, "evidence_refs": UUIDS, "reason": S}, 200),
            ("GET", "/warranties", "actor", "Warranties issued for completed work.", None, 200),
        ],
        "internal": [
            ("GET", "/work-requests/by-assignment/{id}", "Work request projection for a Business assignment (business-operations-service, admin-service)."),
            ("POST", "/accounts/{id}/status", "Suspend or reactivate an artisan account (business-operations-service, admin-service)."),
        ],
        "events": {
            "artisan.work_request.updated.v1": ("work_request", {"work_request_id": UUID, "business_assignment_id": UUID, "organisation_id": UUID, "state": S, "action": S, "version": I}, ["work_request_id", "state"]),
            "artisan.quote.updated.v1": ("quote_request", {"quote_request_id": UUID, "business_quote_id": UUID, "organisation_id": UUID, "state": S, "amount_minor": I, "currency": S}, ["quote_request_id", "state"]),
        },
    },
    "professional-services": {
        "service": "professional-services-service", "folder": "06 Professional Services", "role": "PROFESSIONAL", "glob": "services/professional-services-service/internal/usecases/*.go",
        "title": "Professional Services API",
        "description": "Professional credentials decided by the Verification Service, appointments and progress certifications projected from Business; findings, reports and certifications require a VERIFIED credential and are reported to Business. Document access is a live, purpose-limited Business decision.",
        "routes": [
            ("GET", "/profile", "actor", "The professional profile and credential state.", None, 200),
            ("PUT", "/profile", "actor", "Profession and licence number.", {"profession": S, "license_number": S}, 200),
            ("POST", "/profile/verification", "actor", "Open a PROFESSIONAL_CREDENTIAL case with the Media licence evidence, submit it and request processing.", {"media_id": UUID}, 202),
            ("POST", "/profile/verification/refresh", "actor", "Read the Verification Service decision back.", None, 200),
            ("GET", "/appointments", "actor", "The professional's appointments.", None, 200),
            ("POST", "/appointments/{id}/accept", "actor", "Accept an appointment.", None, 200),
            ("POST", "/appointments/{id}/start", "actor", "Start an appointment.", None, 200),
            ("POST", "/appointments/{id}/report", "actor", "Submit the report (verified credential required).", {"report_reference": UUID}, 200),
            ("POST", "/appointments/{id}/findings", "actor", "Record a finding with Media evidence (verified credential required).", {"kind": S, "title": S, "evidence_refs": UUIDS}, 201),
            ("GET", "/appointments/{id}/documents/{grant}/authorization", "actor", "Live Business decision on a purpose-limited data-room grant.", None, 200),
            ("GET", "/certifications", "actor", "Progress certification requests.", None, 200),
            ("POST", "/certifications/{id}/review", "actor", "Start the review (verified credential required).", None, 200),
            ("POST", "/certifications/{id}/certify", "actor", "Certify with a certificate reference and evidence (verified credential required).", {"certificate_reference": S, "evidence_refs": UUIDS}, 200),
            ("POST", "/certifications/{id}/reject", "actor", "Reject the certification request.", {"reason": S}, 200),
        ],
        "internal": [
            ("GET", "/professionals/{id}/credential", "Credential readback (business-operations-service, admin-service)."),
            ("POST", "/accounts/{id}/status", "Suspend or reactivate a professional account (business-operations-service, admin-service)."),
        ],
        "events": {
            "professional.credential.updated.v1": ("account", {"account_id": UUID, "state": S, "verification_id": UUID, "expires_at": TIME}, ["account_id", "state"]),
            "professional.appointment.updated.v1": ("appointment", {"appointment_id": UUID, "business_appointment_id": UUID, "organisation_id": UUID, "state": S, "action": S}, ["appointment_id", "state"]),
            "professional.certification.updated.v1": ("certification_request", {"certification_request_id": UUID, "business_certification_id": UUID, "organisation_id": UUID, "state": S, "action": S}, ["certification_request_id", "state"]),
        },
    },
    "admin": {
        "service": "admin-service", "folder": "07 Admin", "role": "REVIEWER|SUPERVISOR", "glob": "services/admin-service/internal/usecases/*.go",
        "title": "Admin governance API",
        "description": "Review cases (Business disputes, moderation, financial remedy approvals, high-risk reviews, appeals) under reviewer/supervisor maker-checker. Supervisor decisions require a fresh TOTP code in X-MFA-Code. Dispute decisions are recorded on the Business dispute first; restrictions suspend the account in the owning service; remedy approvals authorise only (execution belongs to the Payment Service).",
        "routes": [
            ("POST", "/mfa/enrol", "account", "Enrol a TOTP factor (seed sealed at rest).", None, 201),
            ("POST", "/mfa/confirm", "account", "Confirm the TOTP factor.", {"code": S}, 200),
            ("GET", "/cases", "actor", "Review queue (query: state, kind).", None, 200),
            ("GET", "/cases/{id}", "actor", "Case with its append-only notes.", None, 200),
            ("POST", "/cases/{id}/notes", "actor", "Append a note with evidence references.", {"note": S, "evidence_refs": UUIDS}, 201),
            ("GET", "/restrictions", "actor", "Account restrictions (query: state).", None, 200),
            ("POST", "/cases/{id}/claim", "actor", "Reviewer claims an open case.", {"version": I}, 200),
            ("POST", "/cases/{id}/propose", "actor", "Assigned reviewer proposes an outcome.", {"version": I, "outcome": S, "rationale": S}, 200),
            ("POST", "/restrictions", "actor", "Reviewer proposes a restriction grounded in a decided case.", {"case_id": UUID, "subject_service": S, "subject_account_id": UUID, "reason": S}, 201),
            ("POST", "/cases/{id}/return", "actor", "Supervisor returns a proposal for rework.", {"reason": S}, 200),
            ("POST", "/cases/{id}/decide", "actor", "Distinct supervisor decides (X-MFA-Code).", {"version": I, "rationale": S}, 200),
            ("POST", "/restrictions/{id}/approve", "actor", "Distinct supervisor approves; the owning service suspends the account (X-MFA-Code).", {"reason": S}, 200),
            ("POST", "/restrictions/{id}/reject", "actor", "Supervisor rejects a proposed restriction.", {"reason": S}, 200),
            ("POST", "/restrictions/{id}/lift", "actor", "Supervisor lifts a restriction; the owning service reactivates the account (X-MFA-Code).", {"reason": S}, 200),
            ("POST", "/accounts/{id}/roles", "actor", "Supervisor grants REVIEWER/SUPERVISOR to another account (X-MFA-Code).", {"roles": {"type": "array", "items": S}, "reason": S}, 200),
        ],
        "internal": [
            ("POST", "/cases", "Open a moderation, financial remedy or high-risk case (idempotent per source reference)."),
            ("GET", "/cases/by-source", "Authoritative case readback for the raising service (query: kind, reference)."),
            ("POST", "/cases/{id}/appeals", "Open the single appeal of a decided case."),
        ],
        "events": {
            "admin.case.updated.v1": ("review_case", {"case_id": UUID, "kind": S, "state": S, "operation": S, "version": I, "source_service": S, "source_reference": S}, ["case_id", "kind", "state"]),
            "admin.case.decided.v1": ("review_case", {"case_id": UUID, "kind": S, "outcome": S, "source_service": S, "source_reference": S, "organisation_id": UUID, "parent_case_id": UUID, "amount_minor": I, "currency": S, "execution": {"const": "PENDING_PAYMENT_SERVICE"}}, ["case_id", "kind", "outcome"]),
            "admin.restriction.updated.v1": ("restriction", {"restriction_id": UUID, "case_id": UUID, "subject_service": S, "subject_account_id": UUID, "state": S, "version": I}, ["restriction_id", "state"]),
        },
    },
}
EVENT_DIRS = {"marketplace": "marketplace", "property-manager": "property-managers", "artisan": "artisans", "professional-services": "professionals", "admin": "admin"}
AREAS = {"marketplace": "marketplace", "property-manager": "property_manager", "artisan": "artisan", "professional-services": "professional", "admin": "admin"}


def scanned(cfg):
    src = "".join(Path(f).read_text() for f in sorted(glob.glob(str(ROOT / cfg["glob"]))) if not f.endswith("_test.go"))
    out = {}
    for kind in ("Public", "Actor", "Internal"):
        m = re.search(kind + r": func\(r chi\.Router\) \{(.*?)\n\t\t\},", src, re.S)
        if m:
            out[kind] = sorted((meth.upper(), path) for meth, path in re.findall(r'r\.(Get|Post|Put|Patch|Delete)\("([^"]+)"', m.group(1)))
    return out


def schema(fields):
    return {"type": "object", "additionalProperties": False, "properties": fields}


def envelope():
    return {"type": "object", "properties": {"data": {"type": "object"}}, "required": ["data"]}


def openapi(domain, cfg):
    paths = {}
    for method, path, auth, summary, body, status in ACCOUNT + cfg["routes"]:
        op = {"summary": summary, "operationId": (domain + method + path).replace("/", "_").replace("{", "").replace("}", "").replace("-", "_"),
              "responses": {str(status): {"description": "Success", "content": {"application/json": {"schema": envelope()}}},
                            "default": {"description": "Platform error envelope", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}}}
        if auth != "public":
            op["security"] = [{"bearerAuth": []}]
            if auth == "actor":
                op["description"] = "Requires an ACTIVE account with role " + cfg["role"] + "."
        else:
            op["security"] = []
        params = [{"name": p, "in": "path", "required": True, "schema": UUID} for p in re.findall(r"\{(\w+)\}", path)]
        if method != "GET":
            params.append({"name": "Idempotency-Key", "in": "header", "required": method in ("POST", "PUT") and path not in ("/auth/login", "/auth/refresh", "/listings/{id}/views", "/mfa/enrol", "/mfa/confirm"), "schema": S})
        if "X-MFA-Code" in summary:
            params.append({"name": "X-MFA-Code", "in": "header", "required": True, "schema": S})
        if params:
            op["parameters"] = params
        if body:
            op["requestBody"] = {"required": True, "content": {"application/json": {"schema": schema(body)}}}
        paths.setdefault("/api/v1/" + domain + path, {})[method.lower()] = op
    return {"openapi": "3.1.0", "info": {"title": cfg["title"], "version": "1.0.0", "description": cfg["description"]},
            "servers": [{"url": "{gateway}", "variables": {"gateway": {"default": "http://localhost:8080"}}}], "paths": dict(sorted(paths.items())),
            "components": {"securitySchemes": {"bearerAuth": {"type": "http", "scheme": "bearer", "bearerFormat": "JWT"}},
                           "schemas": {"Error": {"type": "object", "properties": {"error": {"type": "object", "properties": {"code": S, "message": S, "request_id": S}, "required": ["code", "message"]}}, "required": ["error"]}}}}


def internal_openapi(domain, cfg):
    paths = {}
    for method, path, summary in cfg["internal"]:
        op = {"summary": summary, "operationId": ("internal" + domain + method + path).replace("/", "_").replace("{", "").replace("}", "").replace("-", "_"),
              "security": [{"serviceToken": []}], "responses": {"200": {"description": "Success", "content": {"application/json": {"schema": envelope()}}}}}
        params = [{"name": p, "in": "path", "required": True, "schema": UUID} for p in re.findall(r"\{(\w+)\}", path)]
        if method == "POST":
            params.append({"name": "Idempotency-Key", "in": "header", "required": True, "schema": S})
            op["requestBody"] = {"required": True, "content": {"application/json": {"schema": {"type": "object"}}}}
        if params:
            op["parameters"] = params
        paths.setdefault("/internal/v1" + path, {})[method.lower()] = op
    return {"openapi": "3.1.0", "info": {"title": cfg["service"] + " internal API", "version": "1.0.0", "description": "Signed service-to-service routes (EdDSA service JWT in Authorization, explicit caller allow-list). Never routed by the public gateway."},
            "paths": dict(sorted(paths.items())), "components": {"securitySchemes": {"serviceToken": {"type": "http", "scheme": "bearer", "bearerFormat": "service JWT"}}}}


def event_schema(domain, cfg, etype, spec):
    aggregate, props, required = spec
    return {"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "https://contracts.cosmicforge.realestate/events/" + EVENT_DIRS[domain] + "/" + etype + ".schema.json",
            "title": etype, "description": "Producer: " + cfg["service"] + ". Subject: realestate." + etype + ". Aggregate: " + aggregate + ".",
            "allOf": [{"$ref": "../../schemas/event-envelope.schema.json"}],
            "properties": {"event_type": {"const": etype}, "producer": {"const": cfg["service"]}, "aggregate_type": {"const": aggregate},
                           "payload": {"type": "object", "properties": props, "required": required}}}


def postman(domain, cfg):
    items = []
    for method, path, auth, summary, body, _ in ACCOUNT + cfg["routes"]:
        headers = [{"key": "Content-Type", "value": "application/json"}]
        if auth != "public":
            headers.append({"key": "Authorization", "value": "Bearer {{" + domain.replace("-", "_") + "_access_token}}"})
        if method != "GET":
            headers.append({"key": "Idempotency-Key", "value": "{{$guid}}"})
        if "X-MFA-Code" in summary:
            headers.append({"key": "X-MFA-Code", "value": "{{admin_totp_code}}"})
        req = {"method": method, "header": headers, "url": {"raw": "{{gateway_url}}/api/v1/" + domain + re.sub(r"\{(\w+)\}", r"{{\1}}", path), "host": ["{{gateway_url}}"],
                                                               "path": ["api", "v1", domain] + [p for p in re.sub(r"\{(\w+)\}", r"{{\1}}", path).split("/") if p]}, "description": summary}
        if body:
            example = {k: ("{{" + k + "}}" if v.get("format") == "uuid" else 1 if v.get("type") == "integer" else True if v.get("type") == "boolean" else [] if v.get("type") == "array" else "") for k, v in body.items()}
            req["body"] = {"mode": "raw", "raw": json.dumps(example, indent=2), "options": {"raw": {"language": "json"}}}
        items.append({"name": method + " " + path, "request": req})
    return items


def main():
    check = "--check" in sys.argv
    outputs, problems = {}, []
    collection_path = ROOT / "postman/CosmicForge_RealEstate.postman_collection.json"
    collection = json.loads(collection_path.read_text(encoding="utf-8-sig"))
    total = 0
    for domain, cfg in SERVICES.items():
        found = scanned(cfg)
        documented_actor = sorted((m, p) for m, p, a, *_ in cfg["routes"] if a != "public")
        documented_public = sorted((m, p) for m, p, a, *_ in cfg["routes"] if a == "public")
        documented_internal = sorted((m, p) for m, p, _ in cfg["internal"])
        actor = sorted(found.get("Actor", []))
        if domain == "admin":
            documented_actor = sorted(documented_actor)
        for label, got, want in (("public", found.get("Public", []), documented_public), ("actor", actor, documented_actor), ("internal", found.get("Internal", []), documented_internal)):
            if got != want:
                problems.append(f"{domain} {label} routes: registered {got} documented {want}")
        events = set(re.findall(r'"(' + re.escape(AREAS[domain]) + r'\.[a-z_]+\.[a-z_]+\.v1)"', "".join(Path(f).read_text() for f in glob.glob(str(ROOT / cfg["glob"])) if not f.endswith("_test.go"))))
        if events != set(cfg["events"]):
            problems.append(f"{domain} events: emitted {sorted(events)} documented {sorted(cfg['events'])}")
        cfg["events"][AREAS[domain] + ".account.registered.v1"] = ("account", {"account_id": UUID, "roles": {"type": "array", "items": S}}, ["account_id", "roles"])
        outputs[ROOT / "contracts/public/openapi" / (domain + ".yaml")] = json.dumps(openapi(domain, cfg), indent=2) + "\n"
        if cfg["internal"]:
            outputs[ROOT / "contracts/internal/openapi" / (cfg["service"] + "-domain.yaml")] = json.dumps(internal_openapi(domain, cfg), indent=2) + "\n"
        for etype, spec in sorted(cfg["events"].items()):
            outputs[ROOT / "contracts/events" / EVENT_DIRS[domain] / (etype + ".schema.json")] = json.dumps(event_schema(domain, cfg, etype, spec), indent=2) + "\n"
        folder = next(x for x in collection["item"] if x["name"] == cfg["folder"])
        folder["item"] = postman(domain, cfg)
        total += len(ACCOUNT) + len(cfg["routes"]) + len(cfg["internal"])
    existing = {v["key"] for v in collection.setdefault("variable", [])}
    for key in ["gateway_url", "marketplace_access_token", "property_manager_access_token", "artisan_access_token", "professional_services_access_token", "admin_access_token", "admin_totp_code"]:
        if key not in existing:
            collection["variable"].append({"key": key, "value": "http://localhost:8080" if key == "gateway_url" else "", "type": "string"})
    outputs[collection_path] = json.dumps(collection, indent=2, ensure_ascii=False) + "\n"
    if problems:
        raise SystemExit("Route/event drift:\n" + "\n".join(problems))
    changed = []
    for path, text in outputs.items():
        if not path.exists() or path.read_text(encoding="utf-8-sig") != text:
            changed.append(str(path.relative_to(ROOT)))
            if not check:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text, encoding="utf-8")
    if check and changed:
        raise SystemExit("Contract drift: " + ", ".join(changed))
    events = sum(len(c["events"]) for c in SERVICES.values())
    print(f"{total} domain-service routes; {events} event schemas; " + ("verified" if check else "generated"))


if __name__ == "__main__":
    main()
