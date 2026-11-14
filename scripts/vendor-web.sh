#!/usr/bin/env bash
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
version=1.6.0
expected=5066103959ef4e9b10f37e5a148627360dd8414e4cf8a7db92bdbd022e728aaa
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
curl --fail --location --proto '=https' --tlsv1.2 --max-time 120 \
  "https://codeload.github.com/novnc/noVNC/tar.gz/refs/tags/v${version}" \
  --output "$temporary/source.tar.gz"
printf '%s  %s\n' "$expected" "$temporary/source.tar.gz" | sha256sum --check --status
tar -xzf "$temporary/source.tar.gz" -C "$temporary"
source_dir="$temporary/noVNC-${version}"
target_dir="$root/internal/web/assets/novnc"
mkdir -p "$target_dir/docs"
rm -rf -- "$target_dir/core" "$target_dir/vendor"
cp -R -- "$source_dir/core" "$source_dir/vendor" "$target_dir/"
cp -- "$source_dir/LICENSE.txt" "$source_dir/AUTHORS" "$target_dir/"
cp -- "$source_dir"/docs/LICENSE* "$target_dir/docs/"
printf 'Verified noVNC %s and copied it to %s\n' "$version" "$target_dir"
