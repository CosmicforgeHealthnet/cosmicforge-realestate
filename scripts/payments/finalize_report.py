"""Export the closure report with verified post-commit Git metadata."""
import argparse
import subprocess
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("--sections-4-6", action="store_true")
parser.add_argument("--sections-7-10",action="store_true")
parser.add_argument("--paystack-sandbox", action="store_true")
parser.add_argument("--sections-11-14", action="store_true")
parser.add_argument("--master-final", action="store_true")
args = parser.parse_args()
report_name = "PAYMENT_MASTER_FINAL_CLOSURE.md" if args.master_final else "PAYMENT_SECTIONS_11_14_CLOSURE.md" if args.sections_11_14 else "PAYSTACK_SANDBOX_CERTIFICATION.md" if args.paystack_sandbox else "SECTIONS_7_10_CLOSURE_REPORT.md" if args.sections_7_10 else "SECTIONS_4_6_CLOSURE_REPORT.md" if args.sections_4_6 else "CLOSURE_REPORT.md"
root = Path(__file__).resolve().parents[2]
def git(*args):
    return subprocess.check_output(["git", *args], cwd=root, text=True).strip()

status = git("status", "--porcelain=v1")
if status:
    raise SystemExit("Commit all task changes before finalizing; working tree is not clean.")
sha = git("rev-parse", "HEAD")
branch = git("branch", "--show-current")
commit = git("log", "-1", "--format=%s")
body = (root / "docs/product/payments" / report_name).read_text(encoding="utf-8")
for key, value in {"FINAL_SHA": sha, "FINAL_BRANCH": branch, "FINAL_STATUS": "clean (verified git status --porcelain=v1)", "FINAL_COMMIT": f"{sha} — {commit}"}.items():
    body = body.replace("{{" + key + "}}", value)
target = root / "tmp/payments" / report_name
target.parent.mkdir(parents=True, exist_ok=True)
target.write_text(body, encoding="utf-8")
print(f"Final SHA: {sha}\nBranch: {branch}\nWorking tree: clean\nReport: {target}")
