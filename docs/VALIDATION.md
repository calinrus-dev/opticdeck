# Validation

## OpticDeck 0.2.0

On CachyOS with KDE Wayland and TigerVNC 1.16.2, the Codex built-in browser
displayed a shared 3840 × 2160 monitor after desktop approval. Keyboard and
pointer input reached native applications. The monitor was scaled to 300%.
Live testing compensated for that scale while debugging. The shipped
`--monitor-scale` option applies the same conversion in the RFB proxy. Go tests
cover both pointer formats and ensure an observer cannot send input.

The live Chromium test also passed for the Linux virtual session. Its GTK
fixture yielded 2,463 bytes for the full tree and 505 bytes in compact mode.
The size difference depends on the app. This test measured JSON bytes.

The Windows GitHub Actions runner passed the WPF companion smoke test. It
verified scoped inspection, the scan cap, password redaction, and invocation
of a real UI Automation button.

## SemaDesk 0.1.0

Validated locally on CachyOS (x86-64) with Go 1.27.4, TigerVNC 1.16.2,
AT-SPI2 2.62.1, GTK 3.24, Openbox 3.6, and Chromium 157. The live Chromium
test used an actual virtual session and checked noVNC, native ARIA controls,
Unicode edits, checkboxes, protected values, takeover, and clipboard access.
It also verified that the viewer removes the session token from the address
bar and reconnects after a reload. The screenshot in the README was taken
during this run. The RFB socket accepted a handshake with mode `0600` and
Xvnc had no TCP listeners.

`make test`, `make race`, `make vet`, `make frontend-test`, and `make smoke`
passed. The smoke run also checked a GTK menu extending beyond its parent,
visual fallback for X11 windows without semantic controls, and recovery
after `SIGKILL`.

## Original Debian run

The original integration run on 2026-11-13 used Debian 13 amd64, Go 1.24.13,
TigerVNC 1.15.0, GTK 3, Openbox, and Chromium with Playwright 1.57.0. It ran
on disposable virtual displays. This run preceded the change to a private
Unix RFB socket.

| Check | Coverage in that run |
| --- | --- |
| `make build` | Statically linked Linux amd64 executable |
| `make test`, `make race`, `make vet` | Unit tests, race detector, and Go static analysis |
| `make smoke` | Real Xvnc, D-Bus, Openbox, and GTK session |
| `node tests/frontend.cjs` | DOM behavior in Chromium |
| `node tests/browser.cjs` | Chromium against a live noVNC and GTK session |
| `TestSessionProcessLifetime` | Session processes stop when Openbox exits |
| `TestVirtualDisplayIntegration` | Window stacking, popups, decorations, XTest, and Unicode clipboard transfer above 64 KiB |
| GTK popup extending beyond its parent | All 14 items exposed, including the last item. Menu removed from the tree after closing |

The smoke test exercises the GTK tree, AT-SPI2 actions, UTF-8 text, password
redaction, window geometry, and visual fallback for an opaque X11 window.
It checks shutdown and recovery after `SIGKILL`, including cleanup of the
session's processes and temporary profiles.

The live browser test covers noVNC, accessible selectors, Unicode input, native
checkbox actions, takeover, the HTTP action guard, and bidirectional clipboard
access. The DOM test covers incremental updates, revision recovery, double
clicks, multiline text, Page Up and Page Down, and changes to protected fields.
It also checks that debounce, change, and blur events avoid duplicate writes
while preserving retries, A → B → A edits, and edits pending at takeover.

## Reproduce the checks

```sh
./scripts/install-deps.sh
# Additional smoke-test tools on Debian or Ubuntu
sudo apt-get install libgtk-3-bin x11-utils
make test race vet smoke
```

The integration tests need local Unix sockets and loopback TCP for the HTTP
service. The race detector needs a C compiler. Follow the
[browser test setup](../README.md#develop) to install Playwright, and make
sure Chromium is available. Set `CHROMIUM_EXECUTABLE` if Chromium is outside
`/usr/bin/chromium`.

```sh
session_dir=$(mktemp -d)
./bin/semadesk start --state-dir "$session_dir"
./bin/semadesk launch --state-dir "$session_dir" -- gtk-builder-tool preview "$PWD/tests/fixture.ui"
SEMADESK_STATE="$session_dir/state.json" node tests/browser.cjs
./bin/semadesk stop --state-dir "$session_dir"
```

`SEMADESK_STATE` points to the private session state file. You can also
provide the full session URL via `SEMADESK_URL`. Keep the URL out of shared
scripts and logs.

Qt and Wine were outside the scope of the recorded run. A distribution's
presence in the installer only indicates that it has a package recipe.
Test coverage does not establish a latency or token savings benchmark. The
process guardian cleans up the session process group. Processes that
deliberately escape that group require additional containment for guaranteed
cleanup.
