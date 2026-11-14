#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
for tool in dbus-daemon openbox xprop gtk-builder-tool xmessage; do
    command -v "$tool" >/dev/null 2>&1 || { echo "Missing test dependency: $tool" >&2; exit 1; }
done
if ! command -v Xvnc >/dev/null 2>&1 && ! command -v Xtigervnc >/dev/null 2>&1; then
    echo "Missing test dependency: TigerVNC Xvnc" >&2
    exit 1
fi
SEMADESK_E2E=1 "${GO:-go}" test -count=1 -v ./tests
