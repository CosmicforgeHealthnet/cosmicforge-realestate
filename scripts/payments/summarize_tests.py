"""Summarize saved go test -json evidence; never promote simulators to sandbox."""
import argparse
import json
from collections import defaultdict
from pathlib import Path

root = Path(__file__).resolve().parents[2]
parser = argparse.ArgumentParser()
parser.add_argument("--sections-4-6", action="store_true")
parser.add_argument("--sections-7-10",action="store_true")
parser.add_argument("--paystack-sandbox", action="store_true")
parser.add_argument("--sections-11-14", action="store_true")
args = parser.parse_args()
summary = {}
names = ["sections-11-14-final-test-results.jsonl", "sections-11-14-payment-final-test-results.jsonl"] if args.sections_11_14 else ["paystack-sandbox-local-race-results.jsonl", "paystack-sandbox-payment-race-results.jsonl", "paystack-real-test-results.jsonl", "paystack-followup-test-results.jsonl", "paystack-receipt-test-results.jsonl"] if args.paystack_sandbox else ["sections-7-10-final-test-results.jsonl","sections-7-10-payment-final-test-results.jsonl"] if args.sections_7_10 else ["sections-4-6-final-test-results.jsonl", "sections-4-6-payment-final-test-results.jsonl"] if args.sections_4_6 else ["final-test-results.jsonl", "payment-final-test-results.jsonl"]
seen = set()
for name in names:
    suites = defaultdict(lambda: {"tests": 0, "subtests": 0, "failed": [], "skipped": [], "packages_failed": [], "elapsed_seconds": 0.0})
    for line in (root / "docs/product/payments" / name).read_text(encoding="utf-8-sig").splitlines():
        e = json.loads(line)
        package = e.get("Package", "")
        family = "payment-service" if "/payment-service/" in package else "individual-owner-service" if "/individual-owner-service/" in package else "api-gateway"
        s = suites[family]
        test = e.get("Test")
        if e["Action"] == "pass" and test:
            key = (package, test)
            if key in seen:
                raise SystemExit("Repeated test evidence must not be counted twice: " + test)
            seen.add(key)
            s["subtests" if "/" in test else "tests"] += 1
        if e["Action"] == "fail":
            s["failed" if test else "packages_failed"].append(test or package)
        if e["Action"] == "skip" and test:
            s["skipped"].append(test)
        if e["Action"] == "pass" and not test:
            s["elapsed_seconds"] += e.get("Elapsed", 0)
    summary[name] = dict(suites)

out = root / "docs/product/payments" / ("sections-11-14-test-summary.json" if args.sections_11_14 else "paystack-sandbox-test-summary.json" if args.paystack_sandbox else "sections-7-10-test-summary.json" if args.sections_7_10 else "sections-4-6-test-summary.json" if args.sections_4_6 else "test-summary.json")
out.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
print(json.dumps(summary, indent=2))
if any(s["failed"] or s["packages_failed"] or s["skipped"] for suites in summary.values() for s in suites.values()):
    raise SystemExit("Final certification contains failures or skipped tests")
