# Linux semantic controls benchmark

The benchmark runs three small desktop apps inside OpticDeck. The GTK 3 app
uses GTK widgets through PyGObject, the Qt 6 app uses Qt Widgets, and the
Electron app renders local HTML. They share the same workload. OpticDeck
discovers a text box, checkbox, and button by role and name, enters Unicode
text, enables the checkbox, and reads the result of pressing the button.
Each check reads the resulting app state through the semantic tree.

## Reproduce

Install the [Linux prerequisites](../README.md#install-and-run). The benchmark
also needs Node.js and npm for Playwright, a sandbox-capable Chromium or Chrome
installation for the viewer, Python 3 with PyGObject and GTK 3 typelibs,
and a C++ compiler with `pkg-config` and Qt 6 Widgets development headers.
Use a packaged Electron runtime with its sandbox available.

```sh
make build
npm ci
ELECTRON=/usr/bin/electron43 node benchmarks/run.cjs
```

If Electron is on `PATH` as `electron`, omit `ELECTRON`. Set
`CHROMIUM_EXECUTABLE` if the viewer is outside `/usr/bin/chromium`. Pass
`gtk`, `qt`, or `electron` as arguments to run a subset. The harness uses
the checkout's built CLI by default. Set `OPTICDECK` to an absolute binary
path to measure another build.

```sh
ELECTRON=/usr/bin/electron43 OPTICDECK_BENCH_RAW=1 \
  node benchmarks/run.cjs > benchmark.json
```

`OPTICDECK_BENCH_SAMPLES` changes the recorded rounds per app from the default
of 30. The JSON records the binary version and SHA-256, environment versions,
metrics, and action checks. With `OPTICDECK_BENCH_RAW=1`, it also records
the bytes and elapsed time from each measured sample.

## Method

The harness starts each app in its own temporary OpticDeck session with a
1280 × 800 framebuffer. The Qt and Electron fixtures use the corresponding
`launch --toolkit` presets. Electron's fixture uses an isolated, sandboxed
renderer. The Playwright viewer also uses its browser sandbox. The fixtures
perform no network requests and use synthetic input.

After the browser connects and all three controls appear, each round reads
the full CLI tree, reads the compact CLI tree, and captures the noVNC canvas as
a native-size PNG. The programs and windows are stable during sampling. The
harness rotates acquisition order and discards the first five warmup rounds.
It reports the median and nearest-rank p95. JSON sizes are the bytes written
to stdout. PNG sizes are the encoded screenshot bytes.

CLI latency includes process startup, local HTTP, and writing the tree.
The tree comes from the daemon's latest accessibility snapshot. PNG latency
includes Playwright's capture and encoding of the already connected viewer.
The timings omit application startup, AI model processing, and data transfer
to a remote service. The harness verifies the three app state changes after
sampling.

## Recorded run

OpticDeck 0.3.0 on CachyOS x86-64, Intel Core i7-12700H, on 2026-11-15.
TigerVNC 1.16.2, AT-SPI2 2.62.1, GTK 3.24.52, Qt 6.12.0, Electron
43.8.9, Chromium 157.0.8103.78, Node.js 24.22.3, and Playwright 1.65.1.
The executable SHA-256 was
`ae3147556ff28e595352c6ceae6ef11df0caca505572024351f308391db3deb1`.

| Toolkit | Acquisition | Median bytes | p95 bytes | Median ms | p95 ms |
| --- | --- | ---: | ---: | ---: | ---: |
| GTK 3 | Full tree JSON | 1,971 | 1,971 | 9.04 | 12.21 |
| GTK 3 | Compact tree JSON | 398 | 398 | 8.64 | 10.34 |
| GTK 3 | noVNC canvas PNG | 26,666 | 26,666 | 102.87 | 120.77 |
| Qt 6 | Full tree JSON | 1,682 | 1,682 | 7.47 | 9.65 |
| Qt 6 | Compact tree JSON | 398 | 398 | 8.07 | 11.80 |
| Qt 6 | noVNC canvas PNG | 20,444 | 20,444 | 103.29 | 114.63 |
| Electron | Full tree JSON | 4,971 | 4,971 | 8.36 | 15.40 |
| Electron | Compact tree JSON | 377 | 377 | 8.12 | 13.72 |
| Electron | noVNC canvas PNG | 26,068 | 26,068 | 102.16 | 122.68 |

| Toolkit | Nodes in full tree | Controls in compact tree | PNG / compact bytes | PNG / compact median time |
| --- | ---: | ---: | ---: | ---: |
| GTK 3 | 7 | 3 | 67.0× | 11.9× |
| Qt 6 | 6 | 3 | 51.4× | 12.8× |
| Electron | 19 | 3 | 69.1× | 12.6× |

All three apps exposed the three expected controls and returned the expected
Unicode value, checkbox state, and button result. None used visual fallback
for this workload.

## What the results cover

These measurements describe three comparable interfaces on one machine. A
small PNG may still require visual interpretation, while a JSON tree does
not describe the pixels. Payload bytes do not establish model input tokens or
cost. Real screens, viewport sizes, and accessibility trees can change the
result. FL Studio under Wine and Blender's custom UI are outside this run.
If they expose only window-level semantics, use the corresponding visual
fallback region for work inside that window.
