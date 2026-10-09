#!/usr/bin/env python3
"""Install checksummed NVIDIA Tegra redistributables. Adapted from kokoro-run.

Usage: nvidia-redist.py MANIFEST_URL DEST COMPONENT...
"""
import hashlib
import json
import os
import shutil
import sys
import tarfile
import tempfile
import urllib.request


def main(manifest_url, dest, components):
    base = manifest_url.rsplit("/", 1)[0] + "/"
    with urllib.request.urlopen(manifest_url, timeout=120) as response:
        manifest = json.load(response)
    os.makedirs(dest, exist_ok=True)
    for name in components:
        entry = manifest[name]["linux-aarch64"]
        if "relative_path" not in entry:
            entry = entry["cuda12"]
        with tempfile.TemporaryDirectory() as tmp:
            archive = os.path.join(tmp, "archive.tar.xz")
            digest = hashlib.sha256()
            with urllib.request.urlopen(base + entry["relative_path"], timeout=120) as response, open(archive, "wb") as output:
                while chunk := response.read(1 << 20):
                    digest.update(chunk)
                    output.write(chunk)
            if digest.hexdigest() != entry["sha256"]:
                sys.exit(f"{name}: SHA-256 mismatch")
            with tarfile.open(archive) as package:
                package.extractall(tmp, filter="tar")
            top = os.path.join(tmp, entry["relative_path"].rsplit("/", 1)[1].removesuffix(".tar.xz"))
            shutil.copytree(top, dest, symlinks=True, dirs_exist_ok=True)
        print(f"{name} {manifest[name]['version']}: verified and installed", flush=True)


if __name__ == "__main__":
    if len(sys.argv) < 4:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2], sys.argv[3:])
