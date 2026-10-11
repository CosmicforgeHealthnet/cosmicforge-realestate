"""Explicit, test-only real Paystack certification runner; never echoes keys.

Prerequisites: ignored root .env and real CF_TEST_POSTGRES_URL.
Hosted checkout URLs are written only under ignored tmp/payments. Complete
the four hosted TEST checkouts while the finite readback window is running.
Existing service adapters and Financial Core execute the certification.
"""
import json
import argparse
import os
import ssl
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TMP = ROOT / "tmp/payments"
FLAGS = (
    "PAYMENT_LIVE_CUSTODY_ENABLED", "PAYMENT_LIVE_TRANSFERS_ENABLED",
    "PAYMENT_LIVE_COLLECTIONS_ENABLED", "PAYMENT_LIVE_REFUNDS_ENABLED",
    "PAYMENT_RECURRING_EXECUTION_ENABLED", "PAYMENT_CUSTOMER_CASHOUT_ENABLED",
)

def read_env():
    result = {}
    for line in (ROOT / ".env").read_text(encoding="utf-8-sig").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        value = value.strip()
        if len(value) >= 2 and value[0] in "\"'" and value[-1] == value[0]:
            value = value[1:-1]
        result[key.strip()] = value
    return result

def require(condition, message):
    if not condition:
        raise SystemExit(message)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--followup", action="store_true", help="Read original pending outcomes only; no provider mutations")
    parser.add_argument("--receipt-checkout", action="store_true", help="One TEST checkout proving atomic receipt and reconciliation")
    args = parser.parse_args()
    env = read_env()
    secret = env.get("PAYSTACK_SECRET_KEY", "")
    public = env.get("PAYSTACK_PUBLIC_KEY", "")
    require(secret.startswith("sk_test_") and public.startswith("pk_test_"), "TEST credentials required; values suppressed")
    require(env.get("PAYSTACK_BASE_URL") == "https://api.paystack.co", "Unexpected API origin")
    require(env.get("APP_ENV", "development") != "production", "Production is prohibited")
    require(all(env.get(k, "false").lower() in ("false", "0", "") for k in FLAGS), "Financial activation flags must be disabled")
    require(subprocess.run(["git", "check-ignore", "-q", ".env"], cwd=ROOT).returncode == 0, "Root .env must remain ignored")
    tracked = subprocess.check_output(["git", "ls-files", "-z"], cwd=ROOT).decode().split("\0")
    for name in tracked:
        path = ROOT / name
        if path.is_file():
            data = path.read_bytes()
            require(secret.encode() not in data and public.encode() not in data, "A supplied key is tracked; contents suppressed")
    require(os.environ.get("CF_TEST_POSTGRES_URL"), "Real isolated PostgreSQL is required")
    TMP.mkdir(parents=True, exist_ok=True)
    # Resolve the actual merchant from authenticated customer data. This is
    # test bootstrap discovery only, not a second business payment adapter.
    email = "cosmicforge-certification-20261011@example.com"
    def bootstrap_call(method, path, body=None):
        request = urllib.request.Request(
            "https://api.paystack.co" + path,
            data=None if body is None else json.dumps(body).encode(),
            method=method,
            headers={"Authorization": "Bearer " + secret, "Content-Type": "application/json", "User-Agent": "CosmicForge-Payment-Service/1.0"},
        )
        try:
            response = urllib.request.urlopen(request, context=ssl.create_default_context(), timeout=25)
        except urllib.error.HTTPError as response_error:
            response = response_error
        with response:
            parsed = json.loads(response.read(1 << 20))
            return response.status, parsed
    code, response = bootstrap_call("GET", "/customer/" + email)
    if code == 404:
        require(not args.followup, "Read-only follow-up cannot create a missing bootstrap customer")
        code, response = bootstrap_call("POST", "/customer", {"email": email, "first_name": "CosmicForge", "last_name": "Sandbox"})
    require(200 <= code < 300 and response.get("status") is True, "Authoritative merchant bootstrap failed; response suppressed")
    customer = response["data"]
    require(customer.get("domain") == "test" and isinstance(customer.get("integration"), int), "Merchant test context missing")
    (TMP / "paystack-bootstrap.json").write_text(json.dumps({"merchant_ref": str(customer["integration"]), "customer_code": customer["customer_code"], "customer_domain": "test"}) + "\n", encoding="utf-8")
    child = os.environ.copy()
    child.update({"PAYSTACK_SECRET_KEY": secret, "CF_SANDBOX_ROOT": str(ROOT), "CF_RUN_PAYSTACK_SANDBOX": "1", "CF_REQUIRE_INTEGRATION": "1"})
    for flag in FLAGS:
        child[flag] = "false"
    evidence = TMP / ("paystack-receipt-test-results.jsonl" if args.receipt_checkout else "paystack-followup-test-results.jsonl" if args.followup else "paystack-real-test-results.jsonl")
    # No command-line credential and no uncontrolled live terminal log.
    with evidence.open("w", encoding="utf-8") as out:
        test = "^TestRealPaystackCheckoutReceiptReconciliation$" if args.receipt_checkout else "^TestRealPaystackSandboxReadbackFollowup$" if args.followup else "^TestRealPaystackSandbox$"
        result = subprocess.run(["go", "test", "-tags", "paystack_sandbox", "-json", "-count=1", "-timeout=15m", "./services/payment-service/tests/integration", "-run", test], cwd=ROOT, env=child, stdout=out, stderr=subprocess.STDOUT)
    for path in TMP.glob("paystack-*.json*"):
        data = path.read_bytes()
        require(secret.encode() not in data and public.encode() not in data, "Secret detected in evidence; output suppressed")
    print("Real sandbox runner exit:", result.returncode, "(keys and HTTP headers suppressed)")
    return result.returncode

if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, urllib.error.URLError):
        raise SystemExit("Sandbox configuration/network failure; sensitive details suppressed")
