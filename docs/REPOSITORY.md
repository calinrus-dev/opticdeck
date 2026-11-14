# Repository layout

| Path | Purpose |
| --- | --- |
| [`cmd/semadesk`](../cmd/semadesk) | CLI, daemon supervisor, and process guardian |
| [`internal/session`](../internal/session) | Virtual X11 session, D-Bus, Openbox, and app launching |
| [`internal/desktop`](../internal/desktop) | X11 window geometry, XTest input, and clipboard |
| [`internal/semantic`](../internal/semantic) | AT-SPI2 extraction, reconciliation, and actions |
| [`internal/model`](../internal/model) | Shared JSON types for nodes, revisions, and actions |
| [`internal/server`](../internal/server) | HTTP and WebSockets, takeover, and the RFB proxy |
| [`internal/web`](../internal/web) | Embedded browser UI and vendored noVNC |
| [`scripts`](../scripts) | Dependency install, browser launcher, smoke tests, and asset updates |
| [`packaging`](../packaging) | Linux application launcher and icon |
| [`tests`](../tests) | GTK fixtures and desktop and browser integration tests |
| [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) | Linux build and test workflow |

`make build` creates `bin/semadesk`. Session state, temporary app profiles,
and local test dependencies do not belong in source or release archives.
Keep the [third-party notices](../THIRD_PARTY_NOTICES.md) and associated
licenses with both source and binary distributions.
