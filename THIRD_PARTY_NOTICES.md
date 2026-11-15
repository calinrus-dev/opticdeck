# Third-party software

OpticDeck itself is MIT licensed. Source distributions and binary release bundles
must retain this notice, the root LICENSE, docs/licenses, and noVNC's notices.

| Component | Pinned version | License / original notice |
| --- | --- | --- |
| godbus/dbus/v5 | 5.1.0 | BSD, [notice](docs/licenses/godbus.txt) |
| gorilla/websocket | 1.5.3 | BSD, [notice](docs/licenses/gorilla-websocket.txt) |
| jezek/xgb | 1.1.1 | BSD, [notice](docs/licenses/xgb.txt) |
| modelcontextprotocol/go-sdk | 1.8.0 | Apache-2.0 and MIT transition, [notice](docs/licenses/mcp-go-sdk.txt) |
| google/jsonschema-go | 0.4.3 | MIT, [notice](docs/licenses/jsonschema-go.txt) |
| segmentio/asm | 1.1.3 | MIT, [notice](docs/licenses/segmentio-asm.txt) |
| segmentio/encoding | 0.5.4 | MIT, [notice](docs/licenses/segmentio-encoding.txt) |
| yosida95/uritemplate/v3 | 3.0.2 | BSD, [notice](docs/licenses/uritemplate.txt) |
| golang.org/x/oauth2 | 0.35.0 | BSD, [notice](docs/licenses/x-oauth2.txt) |
| golang.org/x/sync | 0.20.0 | BSD, [notice](docs/licenses/x-sync.txt) |
| golang.org/x/sys | 0.41.0 | BSD, [notice](docs/licenses/x-sys.txt) |
| golang.org/x/time | 0.15.0 | BSD, [notice](docs/licenses/x-time.txt) |
| noVNC | 1.6.0 | MPL-2.0 and component notices, [license](internal/web/assets/novnc/LICENSE.txt) |
| pako (vendored by noVNC) | noVNC 1.6.0 upstream snapshot | [notice](internal/web/assets/novnc/vendor/pako/LICENSE) |

The noVNC JavaScript is shipped as unmodified source ES modules. Its source and
per-file license headers are embedded in the daemon and remain available under
`/novnc/` in an authenticated session. Refresh sources reproducibly using
`scripts/vendor-web.sh`; the archive SHA-256 is fixed there. No third-party code
is loaded from a CDN at runtime. Go module checksums are in `go.sum`.

Xvnc/TigerVNC, openbox, D-Bus and AT-SPI2 are system dependencies distributed by
the operating system under their respective licenses; they are not bundled in
the OpticDeck executable or source archive.
