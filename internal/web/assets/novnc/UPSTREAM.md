# noVNC 1.6.0

Sources: https://github.com/novnc/noVNC/tree/v1.6.0

The unmodified `core/` and `vendor/` ES modules are served from this directory.
The upstream MPL-2.0 license is in LICENSE.txt, with component licenses in docs/.
Upstream authors are listed in AUTHORS. This is intentionally vendored so that
the compiled SemaDesk binary needs neither npm nor an external CDN.

To refresh the same pinned version, run `scripts/vendor-web.sh` from the project.
The script verifies SHA-256 of the official release archive before extraction:
`5066103959ef4e9b10f37e5a148627360dd8414e4cf8a7db92bdbd022e728aaa`.
