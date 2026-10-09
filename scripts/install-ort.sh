#!/usr/bin/env bash
# Provision a checksum-pinned CPU runtime for CI and release smoke tests.
set -euo pipefail
arch=${1:?usage: install-ort.sh amd64|arm64 DIRECTORY}
destination=${2:?destination is required}
case "$arch" in
  amd64) archive_arch=x64; checksum=8344d55f93d5bc5021ce342db50f62079daf39aaafb5d311a451846228be49b3 ;;
  arm64) archive_arch=aarch64; checksum=bb76395092d150b52c7092dc6b8f2fe4d80f0f3bf0416d2f269193e347e24702 ;;
  *) printf 'unsupported architecture: %s\n' "$arch" >&2; exit 1 ;;
esac
mkdir -p "$destination"
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT
curl -fsSL "https://github.com/microsoft/onnxruntime/releases/download/v1.22.0/onnxruntime-linux-$archive_arch-1.22.0.tgz" -o "$archive"
printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c -
tar xzf "$archive" --strip-components=1 --no-same-owner -C "$destination"
