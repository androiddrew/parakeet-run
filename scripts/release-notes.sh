#!/usr/bin/env bash
set -euo pipefail
version=${1:?usage: release-notes.sh vX.Y.Z}
python3 - "$version" <<'PY'
import pathlib, re, sys
version = sys.argv[1].removeprefix("v")
if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", version):
    sys.exit("invalid release version")
text = pathlib.Path("CHANGELOG.md").read_text()
match = re.search(r"^## \[" + re.escape(version) + r"\] - \d{4}-\d{2}-\d{2}\s*\n(.*?)(?=^## |^\[Unreleased\]:|\Z)", text, re.M | re.S)
if not match or not match[1].strip():
    sys.exit(f"missing or empty changelog section for {version}")
print(match[1].strip())
PY
