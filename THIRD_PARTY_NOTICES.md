# Third-party software

SemaDesk itself is MIT licensed. Source distributions and binary release bundles
must retain this notice, the root LICENSE, docs/licenses, and noVNC's notices.

| Component | Pinned version | License / original notice |
| --- | --- | --- |
| godbus/dbus/v5 | 5.1.0 | BSD, [notice](docs/licenses/godbus.txt) |
| gorilla/websocket | 1.5.3 | BSD, [notice](docs/licenses/gorilla-websocket.txt) |
| jezek/xgb | 1.1.1 | BSD, [notice](docs/licenses/xgb.txt) |
| noVNC | 1.6.0 | MPL-2.0 and component notices, [license](internal/web/assets/novnc/LICENSE.txt) |
| pako (vendored by noVNC) | noVNC 1.6.0 upstream snapshot | [notice](internal/web/assets/novnc/vendor/pako/LICENSE) |

The noVNC JavaScript is shipped as unmodified source ES modules. Its source and
per-file license headers are embedded in the daemon and remain available under
`/novnc/` in an authenticated session. Refresh sources reproducibly using
`scripts/vendor-web.sh`; the archive SHA-256 is fixed there. No third-party code
is loaded from a CDN at runtime. Go module checksums are in `go.sum`.

Xvnc/TigerVNC, openbox, D-Bus and AT-SPI2 are system dependencies distributed by
the operating system under their respective licenses; they are not bundled in
the SemaDesk executable or source archive.
