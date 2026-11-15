const layout = [
  '...#.....',
  '...#....E',
  '...#B#...',
  '...#.#...',
  '...#.#...',
  'S.A~.#...',
  '...#.....',
];
const vectors = { ArrowUp: [0, -1], ArrowDown: [0, 1], ArrowLeft: [-1, 0], ArrowRight: [1, 0] };
const tileLabels = { S: 'START', A: 'A', B: 'B', E: 'EXIT', '~': 'GAP' };
const tileClasses = { S: 'start', A: 'relay', B: 'relay', E: 'exit', '~': 'gap', '#': 'wall' };
const timeLimit = 45_000;
const board = document.getElementById('board');
const tiles = document.getElementById('tiles');
const runner = document.getElementById('runner');
const score = document.getElementById('score');
const clock = document.getElementById('time');
const status = document.getElementById('status');
const position = document.getElementById('position');
const relays = new Map();
const held = new Map();
let x = 0;
let y = 5;
let direction = 'ArrowRight';
let runStarted = 0;
let running = false;
let dashTimer;

for (const type of layout.join('')) {
  const tile = document.createElement('div');
  tile.className = 'tile ' + (tileClasses[type] || '');
  tile.textContent = tileLabels[type] || '';
  if (type === 'A' || type === 'B') relays.set(type, tile);
  tiles.append(tile);
}

function draw() {
  runner.style.left = ((x + 0.5) / 9 * 100) + '%';
  runner.style.top = ((y + 0.5) / 7 * 100) + '%';
  position.textContent = 'Column ' + (x + 1) + ', row ' + (y + 1) + '.';
  score.textContent = [...relays.values()].filter(tile => tile.classList.contains('active')).length + ' / 2';
}

function finish(message) {
  running = false;
  held.clear();
  status.textContent = message;
}

function tileAt(nx, ny) {
  return layout[ny]?.[nx] ?? '#';
}

function step(key, dash = false) {
  const [dx, dy] = vectors[key];
  const distance = dash ? 2 : 1;
  const nextX = x + dx * distance;
  const nextY = y + dy * distance;
  const next = tileAt(nextX, nextY);
  if (next === '#' || (dash && tileAt(x + dx, y + dy) === '#')) {
    status.textContent = 'Wall. Change direction.';
    return;
  }
  if (next === '~') {
    status.textContent = 'Gap. Dash to cross.';
    return;
  }

  x = nextX;
  y = nextY;
  if (dash) {
    runner.classList.remove('dash');
    requestAnimationFrame(() => runner.classList.add('dash'));
    clearTimeout(dashTimer);
    dashTimer = setTimeout(() => runner.classList.remove('dash'), 550);
    status.textContent = 'Dash landed.';
  } else {
    status.textContent = 'Running.';
  }

  if (relays.has(next) && !relays.get(next).classList.contains('active')) {
    relays.get(next).classList.add('active');
    status.textContent = 'Relay ' + next + ' lit.';
  }
  draw();
  if (next === 'E') {
    if ([...relays.values()].every(tile => tile.classList.contains('active'))) {
      finish('Course cleared. Both relays lit.');
    } else {
      status.textContent = 'Exit locked. Light both relays first.';
    }
  }
}

function start() {
  x = 0;
  y = 5;
  direction = 'ArrowRight';
  held.clear();
  for (const tile of relays.values()) tile.classList.remove('active');
  runStarted = performance.now();
  running = true;
  clock.textContent = '0.0 s';
  status.textContent = 'Running.';
  draw();
  board.focus();
}

board.addEventListener('keydown', event => {
  if (!running) return;
  if (event.key === ' ') {
    event.preventDefault();
    if (!event.repeat) step(direction, true);
  } else if (Object.hasOwn(vectors, event.key)) {
    event.preventDefault();
    if (held.has(event.key)) return;
    direction = event.key;
    held.set(direction, performance.now() + 230);
    step(direction);
  }
});
board.addEventListener('keyup', event => held.delete(event.key));
board.addEventListener('blur', () => held.clear());
window.addEventListener('blur', () => held.clear());
document.getElementById('start').addEventListener('click', start);

setInterval(() => {
  if (!running) return;
  const now = performance.now();
  const elapsed = now - runStarted;
  clock.textContent = (elapsed / 1000).toFixed(1) + ' s';
  if (elapsed >= timeLimit) {
    finish('Time expired. Start a new run.');
    return;
  }
  if (held.has(direction) && held.get(direction) <= now) {
    held.set(direction, now + 110);
    step(direction);
  }
}, 55);
draw();
