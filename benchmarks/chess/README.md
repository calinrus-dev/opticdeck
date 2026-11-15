# Board Lab

A local browser chess game for exercising OpticDeck. You play White against
a two-ply machine opponent. [chess.js](https://github.com/jhlywa/chess.js/tree/v1.4.0)
validates moves and decides game outcomes. Its source is loaded locally and the
game server binds to loopback.

## Run the fixture

```sh
chess_deps=$(mktemp -d)
npm install --prefix "$chess_deps" --no-audit --no-fund --save-exact chess.js@1.4.0
CHESS_MODULE="$chess_deps/node_modules/chess.js/dist/esm/chess.js" node benchmarks/chess/serve.cjs
```

The server prints a local URL. Keep it running and, in another terminal, use
the printed URL in place of `http://127.0.0.1:PORT/`.

```sh
make build
session=$(mktemp -d)
bin/opticdeck start --state-dir "$session"
bin/opticdeck launch --state-dir "$session" --toolkit electron -- \
  chromium --no-first-run http://127.0.0.1:PORT/
bin/opticdeck tree --state-dir "$session" --compact --match 'e2 white pawn'
```

Each square is a button whose accessible name identifies its coordinate and
occupant. To play a move, click the source and destination buttons. The
visible status and move history report whose turn it is, illegal moves, the
machine's reply, and the result. The arrow keys move focus between squares.
Promotions use a queen.

The default position is a standard chess game. For a short check, select
**Mate in two**, press **Start game**, and play `f6 → g6`. The machine responds
`h8 → g8`. Play `d3 → d8` and verify **Checkmate. White wins.** The move history
shows `Kg6`, `Kg8`, and `Qd8#`.

When finished, stop the temporary session with
`bin/opticdeck stop --state-dir "$session"`, then stop the HTTP server.
