"""Check local/index/evidence without ever printing credential values."""
import json
import re
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[2]
def git(*args):
    return subprocess.check_output(["git", *args], cwd=root)

ignored = subprocess.run(["git", "check-ignore", "-q", ".env"], cwd=root).returncode == 0
tracked_env = bool(git("ls-files", ".env").strip())
keys = []
if (root / ".env").exists():
    for line in (root / ".env").read_text(encoding="utf-8-sig").splitlines():
        name, separator, value = line.strip().partition("=")
        if separator and name in {"PAYSTACK_SECRET_KEY", "PAYSTACK_PUBLIC_KEY"}:
            value = value.strip().strip('"').strip("'")
            if value:
                keys.append(value.encode())
paths = set(git("ls-files", "-z").decode().split("\0"))
paths.update(git("ls-files", "--others", "--exclude-standard", "-z").decode().split("\0"))
paths.update(str(p.relative_to(root)) for p in (root / "tmp/payments").rglob("*") if p.is_file() and p.suffix in {".md", ".json", ".jsonl", ".txt", ".log"})
exact_matches = []
literal_headers = []
credential_patterns = []
header = re.compile(rb"(?i)(?:authorization|x-service-authorization)[\"']?\s*[:=]\s*[\"']?\s*(?:bearer\s+)?eyJ[a-zA-Z0-9_-]{20,}")
credential = re.compile(rb"\b(?:sk|pk)_(?:test|live)_[a-zA-Z0-9]{20,}\b")
checked = 0
for name in paths:
    if not name or name == ".env":
        continue
    p = root / name
    if not p.is_file():
        continue
    data = p.read_bytes()
    checked += 1
    if any(k in data for k in keys):
        exact_matches.append(name)
    if header.search(data):
        literal_headers.append(name)
    if credential.search(data):
        credential_patterns.append(name)
index = git("diff", "--cached", "--binary")
index_key_match = any(k in index for k in keys)
result = dict(env_ignored=ignored, env_tracked=tracked_env, supplied_key_count=len(keys), files_checked=checked,
              exact_key_matches=exact_matches, index_key_match=index_key_match,
              literal_secret_headers=literal_headers, credential_pattern_matches=credential_patterns)
print(json.dumps(result, indent=2))
if not ignored or tracked_env or exact_matches or index_key_match or literal_headers or credential_patterns:
    raise SystemExit("Secret/credential evidence check failed; no credential values were printed")
