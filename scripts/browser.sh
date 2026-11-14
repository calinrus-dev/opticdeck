#!/bin/sh
set -eu

if [ "$#" -lt 1 ]; then
    echo "Usage: $0 'http://127.0.0.1:PORT/?token=SESSION_TOKEN' [browser arguments...]" >&2
    exit 2
fi
url=$1
shift
case "$url" in
    http://127.0.0.1:*) ;;
    *) echo "Expected a loopback SemaDesk URL" >&2; exit 2 ;;
esac

browser=${SEMADESK_BROWSER:-}
if [ -z "$browser" ]; then
    for candidate in chromium chromium-browser google-chrome google-chrome-stable; do
        if command -v "$candidate" >/dev/null 2>&1; then
            browser=$candidate
            break
        fi
    done
fi
if [ -z "$browser" ]; then
    echo "No Chromium browser found; set SEMADESK_BROWSER." >&2
    exit 1
fi
profile=${SEMADESK_BROWSER_PROFILE:-${XDG_CACHE_HOME:-$HOME/.cache}/semadesk/chromium}
mkdir -p "$profile"
chmod 700 "$profile"
exec "$browser" \
    --user-data-dir="$profile" \
    --disable-background-timer-throttling \
    --disable-backgrounding-occluded-windows \
    --disable-renderer-backgrounding \
    --disable-features=CalculateNativeWinOcclusion,IntensiveWakeUpThrottling \
    --no-first-run --no-default-browser-check \
    "$@" "$url"
