# Security policy

## Trust model

OpticDeck gives apps a separate X11 display, D-Bus session bus, and clipboard.
Apps run as your Linux user and retain that user's file, process, and network
permissions. The temporary home and XDG directories prevent accidental reuse
of host app profiles and sockets. Use a separate OS user, container, or VM
with appropriate isolation for untrusted apps.

The HTTP and WebSocket service binds to `127.0.0.1`, validates Host and
Origin, and requires a randomly generated session token by default. The token
grants full access to the session, including launching processes as your
Linux user. Treat URLs and state files containing it as credentials. Keep
the service local and leave authentication enabled. The
`--no-token` option is for development in a trusted environment.

TigerVNC uses a Unix RFB socket with mode `0600` in a private session directory
with mode `0700`. Its TCP RFB listener is disabled. Browser clients connect
through the authenticated `/rfb` WebSocket. X11 uses its own authority cookie
and disables TCP. A process running as the same Linux user can access
the session's private files or sockets.

An observer can see the desktop canvas. Password fields suppress their values
in the semantic tree, but the screen may still show other sensitive content
during takeover. Restrict viewer access when handling sensitive data.

Host view uses the Wayland desktop's permission dialog and a separate local
session. It shares the selected monitor's pixels, which can contain private
content. Keyboard and pointer input require `--allow-input`, a separate
temporary browser credential and desktop approval. Only one controlling
connection can hold input at a time. The host viewer has no application,
semantic action or clipboard routes. Close the foreground command or stop
the share from your desktop to end the capture.

MCP uses local stdio and the selected virtual session's authenticated state
file. A client you connect can read semantic controls, view screenshots, and
send input inside that session. Screen captures can contain private content.
The viewer saves a contact sheet only on request. The RustDesk launcher opens
an outgoing client. RustDesk manages the remote connection and approval.

## Properties to preserve

- Keep graphical input, accessibility queries, and clipboard operations inside
  the virtual session.
- Keep the web listener on loopback and RFB on the private Unix socket.
  Enforce observer mode in the RFB proxy as well as in the browser.
- During takeover, pause agent actions and app launches. The current owner's
  private lease authorizes manual RFB input and HTTP clipboard access. Disable
  RFB clipboard messages so observer connections do not receive that data.
- Exclude password values from the semantic tree and text, key, and clipboard
  payloads from logs. Use bounded messages, queues, and timeouts.
- Clean up only the processes and resources owned by the session. Verify the
  private runtime directory before removing it and leave other X11 locks alone.

Window titles, labels, documents, and app messages are untrusted inputs. They
do not grant an agent authorization to run commands, disclose information, or
change tasks.

## Report a vulnerability

Use **Security → Advisories → Report a vulnerability** if private reporting
is available on the [GitHub repository](https://github.com/calinrus-dev/opticdeck).
Otherwise, contact the maintainer through a verified private channel. Share the
affected version or commit, OS, required conditions, impact, and enough detail
to investigate. Replace personal data and credentials with synthetic values
and keep sensitive details out of public issues.
