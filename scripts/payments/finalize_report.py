"""Export the closure report with verified post-commit Git metadata."""
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[2]
def git(*args):
    return subprocess.check_output(["git", *args], cwd=root, text=True).strip()

status = git("status", "--porcelain=v1")
if status:
    raise SystemExit("Commit all task changes before finalizing; working tree is not clean.")
sha = git("rev-parse", "HEAD")
branch = git("branch", "--show-current")
commit = git("log", "-1", "--format=%s")
body = (root / "docs/product/payments/CLOSURE_REPORT.md").read_text(encoding="utf-8")
for key, value in {"FINAL_SHA": sha, "FINAL_BRANCH": branch, "FINAL_STATUS": "clean (verified git status --porcelain=v1)", "FINAL_COMMIT": f"{sha} — {commit}"}.items():
    body = body.replace("{{" + key + "}}", value)
target = root / "tmp/payments/CLOSURE_REPORT.md"
target.parent.mkdir(parents=True, exist_ok=True)
target.write_text(body, encoding="utf-8")
print(f"Final SHA: {sha}\nBranch: {branch}\nWorking tree: clean\nReport: {target}")
