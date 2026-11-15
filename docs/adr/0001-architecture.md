# ADR 0001 · A virtual desktop with native accessibility controls

Accepted for Linux with TigerVNC and virtual X11.

## Runtime

OpticDeck uses Go and builds with `CGO_ENABLED=0`. The executable embeds the
HTTP server, WebSocket proxy, and browser assets. It uses
`github.com/godbus/dbus/v5` for D-Bus, `github.com/jezek/xgb` for X11 and XTest,
and `github.com/gorilla/websocket` for WebSockets. The versions are pinned in
`go.mod` and `go.sum`. Rust with `zbus`, `x11rb`, `axum`, `tokio`, and
`rust-embed` was also considered. Go kept the implementation of IPC, process
management, and HTTP in one small dependency set.

TigerVNC, Openbox, and AT-SPI2 are system packages. OpticDeck uses an
independent `DISPLAY` and D-Bus session address, sets GTK and Qt to use X11,
and strips the inherited Wayland display and host accessibility bus settings.
Temporary home and XDG directories prevent accidental reuse of host app
profiles or singleton sockets. Apps can open absolute host paths according
to the Linux user's permissions.

noVNC ships as local assets with its notices. The update script pins the
archive by SHA-256. Node and Playwright are development tools for browser
tests.

## Protocol

The server exposes `GET /api/status`, `GET /api/tree`, `POST /api/act`,
`POST /api/launch`, `POST /api/stop`, and `POST /api/clipboard`. The
`/ws` WebSocket carries semantic updates and actions, and `/rfb` carries
the noVNC connection. The web API accepts the token as a Bearer credential
or through the session URL.

An initial snapshot carries `type`, `revision`, `width`, `height`, `nodes`,
and `control`. Each node has a stable ID derived from its D-Bus destination
and object path, along with its parent, role, name, value, states, actions,
shortcut, bounds, and stacking order. Diffs carry `base`, `revision`,
`upsert`, and `remove`. The browser requests a fresh snapshot if it misses a
revision. Slow clients reconnect to receive a snapshot. The extractor
coalesces events and periodically reconciles its state to recover missed
notifications.

Actions include a request ID. HTTP and WebSocket actions share the takeover
lock. Takeover pauses agent actions and launches, closes earlier RFB
connections, and grants manual input to the current owner through a private
lease. The RFB proxy filters observer input on the server. The browser's
`viewOnly` setting also prevents accidental input in its own UI.

## Coordinates and stacking

AT-SPI `Component.GetExtents` provides screen coordinates. X11's
`TranslateCoordinates` and `_NET_FRAME_EXTENTS` identify client and frame
rectangles. `_NET_CLIENT_LIST_STACKING` orders windows from bottom to top.
The extractor correlates windows by process and geometry, clips rectangles
to the framebuffer, and excludes or clips covered hitboxes.

The framebuffer uses native pixels. The browser applies the same CSS
transform to the overlay and canvas, recalculating scale with
`ResizeObserver`. A window with no actionable semantics gets a
`vision_fallback` node. The node describes the region available for
inspection.

## Lifecycle and clipboard

`start` launches a supervisor in its own process group. A private lock
serializes each instance. The supervisor stops its children when it receives
a signal or when Xvnc, D-Bus, or Openbox exits. An external guardian watches
the supervisor's lifetime pipe and terminates its group after an unexpected
exit, including `SIGKILL`. The guardian checks the session path and ownership
before removing temporary profiles and authority files. AT-SPI calls have
timeouts and bounded tree size and depth.

The X11 clipboard uses `UTF8_STRING` and `TARGETS`. Oversized clipboard
transfers and incremental X11 `INCR` transfers are rejected. RFB clipboard
messages are disabled. The takeover owner can read and write UTF-8 clipboard
content over the web connection. Paste fallback uses Ctrl+V inside the
virtual display. The daemon and CLI work without an open browser tab.

## Trust boundaries

HTTP and WebSocket traffic stays on IPv4 loopback. The server checks
Host and Origin and requires a random token by default. TigerVNC exposes
RFB on a mode `0600` Unix socket in the private session directory and
disables RFB over TCP. X11 uses a separate cookie and disables TCP.
The [security policy](../../SECURITY.md) explains which protections this
provides and which permissions session apps retain.
