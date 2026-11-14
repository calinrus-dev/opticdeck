# Contributing to SemaDesk

For changes to the protocol, input handling, or session lifecycle, read the
[architecture decision](docs/adr/0001-architecture.md) and the
[security policy](SECURITY.md).

Install Go 1.23 or newer and run `./scripts/install-deps.sh`. Build with
`make build` and use a separate private `--state-dir` for development.
Integration tests must use their own virtual X11 display and session bus.

```sh
gofmt -w path/to/changed.go
make test
make race
make vet
make smoke
```

The race detector needs a C compiler. The smoke tests also use
`gtk-builder-tool` and `xmessage`. See the
[validation guide](docs/VALIDATION.md) for test dependencies and browser
commands. Mention any checks you could not run.

Add a regression test when existing checks would miss a bug in the protocol,
authorization, concurrency, or geometry. Integration tests should use
bounded timeouts and clean up the sessions they start. For UI changes, check
keyboard input, focus, scale, and reconnect behavior. Preserve DOM node
identity when applying semantic diffs.

Use `scripts/vendor-web.sh` to update noVNC with a pinned version and
SHA-256 checksum. Retain the notices and licenses for all distributed assets.

In a pull request, describe the problem, the new behavior, and how you tested
it. For a UI change, attach a screenshot from a demo session with no tokens or
personal data. For a protocol change, document the payload and how consumers
recover from a missed revision. Keep the web client and documentation in sync.

Use the private reporting route in [SECURITY.md](SECURITY.md) for
vulnerabilities. Keep session directories, authentication tokens, X11 cookies,
browser profiles, and entered text out of the repository.
