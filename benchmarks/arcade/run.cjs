const assert = require('node:assert/strict');
const { execFile } = require('node:child_process');
const fs = require('node:fs/promises');
const { createServer } = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { setTimeout: delay } = require('node:timers/promises');
const { promisify } = require('node:util');

const exec = promisify(execFile);
const opticdeck = process.env.OPTICDECK || path.resolve(__dirname, '../../bin/opticdeck');
const chromium = process.env.CHROMIUM_EXECUTABLE || '/usr/bin/chromium';

async function serveFixture() {
  const assets = new Map(await Promise.all([
    ['/', 'index.html', 'text/html'],
    ['/relay.css', 'relay.css', 'text/css'],
    ['/relay.js', 'relay.js', 'text/javascript'],
  ].map(async ([url, file, type]) => [url, { type, body: await fs.readFile(path.join(__dirname, file)) }])));
  const server = createServer((request, response) => {
    const asset = assets.get(request.url);
    if (!asset || request.method !== 'GET') return response.writeHead(404).end();
    response.writeHead(200, {
      'Content-Type': asset.type + '; charset=utf-8',
      'Content-Security-Policy': "default-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'",
      'Cache-Control': 'no-store',
    }).end(asset.body);
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  return { server, url: 'http://127.0.0.1:' + server.address().port + '/' };
}

async function waitFor(read, check, label) {
  const until = performance.now() + 10000;
  do {
    const value = await read();
    const result = check(value);
    if (result) return result;
    await delay(30);
  } while (performance.now() < until);
  throw new Error('Timed out waiting for ' + label);
}

async function main() {
  const stateDir = await fs.mkdtemp(path.join(os.tmpdir(), 'opticdeck-arcade-'));
  let fixture;
  const cli = async (command, ...args) => JSON.parse((await exec(opticdeck, [command, '--state-dir', stateDir, ...args], { timeout: 45000 })).stdout);
  const nodes = async () => (await cli('tree')).nodes;
  const text = expected => waitFor(nodes, current => current.find(node => node.name === expected || node.value === expected), expected);
  const tap = key => cli('act', '--action', 'press_key', '--key', key);
  const trace = [];
  const frames = process.env.OPTICDECK_ARCADE_FRAMES;
  let browser;
  let canvas;
  let started = false;
  async function capture(label) {
    if (canvas) await canvas.screenshot({ path: path.join(frames, label + '.png'), timeout: 10000 });
  }

  try {
    fixture = await serveFixture();
    const state = await cli('start', '--width', '1280', '--height', '800');
    started = true;
    await cli('launch', '--toolkit', 'electron', '--', chromium, '--no-first-run', '--no-default-browser-check', '--start-maximized', fixture.url);
    const start = await waitFor(nodes, current => current.find(node => node.role === 'button' && node.name === 'Start run'), 'Start run button');
    if (frames) {
      const { chromium: playwrightChromium } = require('playwright');
      await fs.mkdir(frames, { recursive: true });
      browser = await playwrightChromium.launch({ executablePath: chromium, chromiumSandbox: true });
      const page = await browser.newPage({ viewport: { width: 1680, height: 1080 } });
      const url = new URL(state.url);
      if (state.token) url.searchParams.set('token', state.token);
      await page.goto(url.href);
      await page.getByText('Connected · VNC + DOM', { exact: true }).waitFor({ timeout: 15000 });
      canvas = page.locator('#rfb-screen canvas');
    }
    await cli('act', '--node', start.id, '--action', 'click');
    await text('Running.');
    await text('Column 1, row 6.');
    await capture('01-start');

    await cli('act', '--action', 'hold_key', '--key', 'Right', '--duration-ms', '315');
    await text('Relay A lit.');
    await text('Column 3, row 6.');
    trace.push('Held Right for 315 ms. The runner moved two cells and lit relay A.');
    await capture('02-first-relay');

    await tap('Right');
    await text('Gap. Dash to cross.');
    await text('Column 3, row 6.');
    trace.push('Right alone stopped at the gap.');
    await tap('space');
    await text('Dash landed.');
    await text('Column 5, row 6.');
    trace.push('Space dashed across the gap to column 5.');
    await capture('03-dash');

    for (const key of ['Up', 'Up', 'Up']) await tap(key);
    await text('Relay B lit.');
    await text('2 / 2');
    trace.push('Three Up taps lit relay B.');
    await tap('Right');
    await text('Wall. Change direction.');
    await tap('Down');
    await text('Column 5, row 4.');
    await tap('Up');
    await tap('Up');
    await tap('Right');
    await text('Column 6, row 2.');
    await tap('Left');
    await text('Column 5, row 2.');
    for (const key of ['Right', 'Right', 'Right', 'Right']) await tap(key);
    await text('Course cleared. Both relays lit.');
    await text('Column 9, row 2.');
    await text('2 / 2');
    const elapsed = await waitFor(nodes, current => current.flatMap(node => [node.value, node.name]).find(v => /^\d+\.\d s$/.test(v || '')), 'run timer');
    assert.ok(parseFloat(elapsed) < 45, 'the course must finish before the timer expires');
    trace.push('Up, Down, Left and Right taps respected walls and reached the exit.');
    await capture('04-finished');
    console.log(JSON.stringify({ result: 'Course cleared. Both relays lit.', relays: 2, elapsed, trace }, null, 2));
  } finally {
    try {
      await browser?.close();
    } finally {
      try {
        if (started) {
          await cli('stop');
          await waitFor(() => cli('status'), state => state.running === false, 'temporary session shutdown');
          started = false;
        }
      } finally {
        fixture?.server.close();
        if (!started) await fs.rm(stateDir, { recursive: true, force: true });
      }
    }
  }
}

main().catch(error => {
  console.error(String(error.message).replace(/token=[^&\s"']+/g, 'token=[redacted]'));
  process.exitCode = 1;
});
