import { Chess } from '/rules.mjs';

const files = 'abcdefgh';
const ranks = '87654321';
const names = { p: 'pawn', n: 'knight', b: 'bishop', r: 'rook', q: 'queen', k: 'king' };
const glyphs = { p: '♟', n: '♞', b: '♝', r: '♜', q: '♛', k: '♚' };
const material = { p: 100, n: 320, b: 330, r: 500, q: 900, k: 0 };
const shortGame = '7k/8/5K2/8/8/3Q4/8/8 w - - 0 1';
const board = document.getElementById('board');
const status = document.getElementById('state');
const history = document.getElementById('history');
const squares = new Map();
let game = new Chess();
let selection;
let thinking;
let reply;

function squareLabel(square, piece) {
  return square + ' ' + (piece ? (piece.color === 'w' ? 'white ' : 'black ') + names[piece.type] : 'empty');
}

for (const [row, rank] of [...ranks].entries()) {
  for (const [col, file] of [...files].entries()) {
    const coord = file + rank;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'square ' + ((row + col) % 2 ? 'dark' : 'light');
    button.dataset.square = coord;
    const piece = document.createElement('span');
    piece.className = 'piece';
    piece.setAttribute('aria-hidden', 'true');
    button.append(piece);
    if (col === 0 || row === 7) {
      const coordinate = document.createElement('span');
      coordinate.className = 'coordinate';
      coordinate.setAttribute('aria-hidden', 'true');
      coordinate.textContent = col === 0 && row !== 7 ? rank : row === 7 && col === 0 ? coord : file;
      button.append(coordinate);
    }
    board.append(button);
    squares.set(coord, { button, piece });
  }
}

function summary() {
  if (game.isCheckmate()) return game.turn() === 'b' ? 'Checkmate. White wins.' : 'Checkmate. Machine wins.';
  if (game.isStalemate()) return 'Draw by stalemate.';
  if (game.isDraw()) return 'Draw.';
  if (thinking) return 'Machine thinking.';
  if (selection) return selection + ' selected. Choose a destination.';
  if (game.isCheck()) return 'White to move. Check.';
  return 'White to move.';
}

function render() {
  const moves = game.history({ verbose: true });
  const last = moves.at(-1);
  const targets = new Set(selection ? game.moves({ square: selection, verbose: true }).map(move => move.to) : []);
  for (const [square, { button, piece }] of squares) {
    const entry = game.get(square);
    button.setAttribute('aria-label', squareLabel(square, entry));
    button.setAttribute('aria-pressed', String(selection === square));
    button.classList.toggle('white', entry?.color === 'w');
    button.classList.toggle('black', entry?.color === 'b');
    button.classList.toggle('legal', targets.has(square));
    button.classList.toggle('last', last?.from === square || last?.to === square);
    piece.textContent = entry ? glyphs[entry.type] : '';
  }
  status.textContent = summary();
  history.replaceChildren(...moves.map(move => {
    const item = document.createElement('li');
    item.textContent = (move.color === 'w' ? 'White ' : 'Machine ') + move.san;
    return item;
  }));
  history.scrollTop = history.scrollHeight;
}

function score(position) {
  let total = 0;
  for (const [row, values] of position.board().entries()) {
    for (const [col, piece] of values.entries()) {
      if (!piece) continue;
      const center = piece.type === 'k' ? 0 : 8 - Math.abs(3.5 - row) - Math.abs(3.5 - col);
      total += (piece.color === 'b' ? 1 : -1) * (material[piece.type] + center);
    }
  }
  return total;
}

function evaluate(position, depth, alpha, beta) {
  if (position.isCheckmate()) return (position.turn() === 'w' ? 1 : -1) * (100000 + depth);
  if (position.isDraw()) return 0;
  if (depth === 0) return score(position);

  const maximize = position.turn() === 'b';
  let best = maximize ? -Infinity : Infinity;
  for (const move of position.moves({ verbose: true })) {
    position.move(move);
    const value = evaluate(position, depth - 1, alpha, beta);
    position.undo();
    if (maximize) {
      best = Math.max(best, value);
      alpha = Math.max(alpha, best);
    } else {
      best = Math.min(best, value);
      beta = Math.min(beta, best);
    }
    if (alpha >= beta) break;
  }
  return best;
}

function computerMove() {
  let best = -Infinity;
  let selectedMove;
  const moves = game.moves({ verbose: true }).sort((a, b) => a.san.localeCompare(b.san, 'en'));
  for (const move of moves) {
    game.move(move);
    const value = evaluate(game, 1, best, Infinity);
    game.undo();
    if (value > best) {
      best = value;
      selectedMove = move;
    }
  }
  if (selectedMove) game.move(selectedMove);
  thinking = false;
  render();
}

function selectSquare(square) {
  if (game.isGameOver() || thinking) return;
  if (square === selection) {
    selection = undefined;
    render();
    return;
  }
  if (game.get(square)?.color === 'w') {
    selection = square;
    render();
    return;
  }
  if (!selection) return;
  const legal = game.moves({ square: selection, verbose: true }).find(move => move.to === square && (!move.promotion || move.promotion === 'q'));
  if (!legal) {
    status.textContent = 'Illegal move. Choose a legal destination for ' + selection + '.';
    return;
  }
  game.move(legal);
  selection = undefined;
  thinking = !game.isGameOver();
  render();
  if (thinking) reply = setTimeout(computerMove, 125);
}

board.addEventListener('click', event => {
  const square = event.target.closest('button[data-square]')?.dataset.square;
  if (square) selectSquare(square);
});

board.addEventListener('keydown', event => {
  const square = event.target.dataset.square;
  if (!square) return;
  const offsets = { ArrowUp: [0, 1], ArrowDown: [0, -1], ArrowLeft: [-1, 0], ArrowRight: [1, 0] };
  const shift = offsets[event.key];
  if (!shift) return;
  event.preventDefault();
  const col = files.indexOf(square[0]) + shift[0];
  const row = Number(square[1]) + shift[1];
  squares.get(files[col] + row)?.button.focus();
});

document.getElementById('new-game').addEventListener('submit', event => {
  event.preventDefault();
  clearTimeout(reply);
  selection = undefined;
  thinking = false;
  game = document.getElementById('position').value === 'mate-two' ? new Chess(shortGame) : new Chess();
  render();
});

render();
