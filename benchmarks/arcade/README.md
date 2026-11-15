# Relay Run

An offline browser obstacle course for testing real held keys and discrete
keypresses. Arrow keys move the runner. Hold a direction to repeat movement.
Space dashes two cells in the last direction. The exit opens when both relays
are lit. The timer starts at **Start run**, and a run expires after 45 seconds.

```sh
make build
node benchmarks/arcade/run.cjs
```

Use a sandbox-capable Chromium installation. Set `CHROMIUM_EXECUTABLE` if it is
outside `/usr/bin/chromium`. The harness starts a loopback server and an isolated
OpticDeck session. It locates the start button by role and name, then uses
OpticDeck's CLI for every input and every state check. It stops the session and
server after the run.

The test holds Right for 315 ms to light relay A, checks that a normal step
cannot cross the gap, and uses Space to land on the other side. It lights
relay B, verifies wall collision, exercises all four directions, and checks
the completion message with 2/2 relays. In the recorded run on CachyOS,
Chromium 157.0.8103.78 displayed 3.5 seconds at completion.

```sh
npm ci
OPTICDECK_ARCADE_FRAMES=/tmp/relay-run-frames node benchmarks/arcade/run.cjs
```

The optional capture directory stores four PNG frames from the live noVNC
canvas. Playwright captures those frames while the CLI owns game input.
Animation and capture timing can vary with display refresh and host load.
