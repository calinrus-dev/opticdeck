const assert = require('node:assert/strict');
const { execFile } = require('node:child_process');
const { createHash } = require('node:crypto');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { performance } = require('node:perf_hooks');
const { setTimeout: delay } = require('node:timers/promises');
const { promisify } = require('node:util');
const { chromium } = require('playwright');

const exec = promisify(execFile);
const opticdeck = process.env.OPTICDECK || path.resolve(__dirname, '../bin/opticdeck');
const electron = process.env.ELECTRON || 'electron';
const rounds = Number(process.env.OPTICDECK_BENCH_SAMPLES || '30');
const warmups = 5;
const includeSamples = process.env.OPTICDECK_BENCH_RAW === '1';
const width = 1280;
const height = 800;

function run(binary, args) {
  return exec(binary, args, { encoding: 'buffer', timeout: 45000, maxBuffer: 8 * 1024 * 1024 });
}

function parse(result) {
  return JSON.parse(result.stdout);
}

async function eventually(read, condition, label) {
  const until = performance.now() + 10000;
  do {
    const state = await read();
    if (condition(state)) return state;
    await delay(30);
  } while (performance.now() < until);
  throw new Error('Timed out waiting for ' + label);
}

function quantiles(values) {
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  const median = sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
  return { median, p95: sorted[Math.ceil(sorted.length * 0.95) - 1] };
}

const controls = {
  editor: { name: 'Project name', role: 'textbox' },
  checkbox: { name: 'Include draft', role: 'checkbox' },
  submit: { name: 'Apply changes', role: 'button' },
};

function matches(node, target) {
  return node.name === target.name && node.role === target.role && !node.vision_fallback && node.actions?.length;
}

function find(tree, target) {
  return tree.nodes.find(node => matches(node, target));
}

async function verifyWorkflow(cli, tree, initial) {
  const editor = find(initial, controls.editor);
  const checkbox = find(initial, controls.checkbox);
  const submit = find(initial, controls.submit);
  assert.ok(editor && checkbox && submit, 'three actionable controls are required');
  const desired = 'OpticDeck – Málaga 世界';
  assert.equal(editor.value, 'Original project');
  assert.equal(checkbox.states?.includes('checked') || false, false);

  const start = performance.now();
  await cli('act', ['--node', editor.id, '--action', 'set_text', '--text', desired]);
  await eventually(tree, state => find(state, controls.editor)?.value === desired, 'Unicode edit');
  await cli('act', ['--node', checkbox.id, '--action', 'click']);
  await eventually(tree, state => find(state, controls.checkbox)?.states?.includes('checked'), 'checked state');
  await cli('act', ['--node', submit.id, '--action', 'click']);
  const result = 'Saved: ' + desired + ' (draft included)';
  await eventually(tree, state => state.nodes.some(node => node.name === result || node.value === result), 'submitted result');
  return { verified: ['set_text + read value', 'click + read checked state', 'click + read application result'],
    workflowMs: Number((performance.now() - start).toFixed(2)) };
}

async function measure(toolkit, fixture, dir, browser) {
  const stateDir = path.join(dir, toolkit);
  const cli = (command, args = []) => run(opticdeck, [command, '--state-dir', stateDir, ...args]);
  const tree = async () => parse(await cli('tree'));
  const context = await browser.newContext({ viewport: { width: 1680, height: 1080 }, deviceScaleFactor: 1 });
  let started = false;
  try {
    const state = parse(await cli('start', ['--width', String(width), '--height', String(height)]));
    started = true;
    const url = new URL(state.url);
    if (state.token) url.searchParams.set('token', state.token);
    await cli('launch', ['--toolkit', toolkit === 'gtk' ? 'auto' : toolkit, '--', ...fixture]);
    const page = await context.newPage();
    await page.goto(url.href);
    await page.getByText('Connected · VNC + DOM', { exact: true }).waitFor({ timeout: 15000 });
    const canvas = page.locator('#rfb-screen canvas');
    await canvas.waitFor({ timeout: 15000 });
    const initial = await eventually(tree, state => Object.values(controls).every(name => find(state, name)), toolkit + ' accessible controls');
    const compact = parse(await cli('tree', ['--compact']));
    assert.ok(Object.values(controls).every(target => compact.controls.some(control => matches(control, target))), 'compact output must expose all named controls');
    const box = await canvas.boundingBox();
    assert.equal(Math.round(box.width), width, 'the canvas must be captured at native width');
    assert.equal(Math.round(box.height), height, 'the canvas must be captured at native height');

    const modes = [
      ['full', async () => (await cli('tree')).stdout],
      ['compact', async () => (await cli('tree', ['--compact'])).stdout],
      ['png', () => canvas.screenshot({ type: 'png', timeout: 10000 })],
    ];
    const values = { full: [], compact: [], png: [] };
    for (let round = 0; round < warmups + rounds; round++) {
      for (let offset = 0; offset < modes.length; offset++) {
        const [key, collect] = modes[(round + offset) % modes.length];
        const begin = performance.now();
        const result = await collect();
        const elapsed = performance.now() - begin;
        if (round >= warmups) values[key].push({ bytes: result.length, ms: elapsed });
      }
    }
    const metrics = Object.fromEntries(Object.entries(values).map(([key, samples]) => [key, {
      bytes: quantiles(samples.map(sample => sample.bytes)),
      ms: Object.fromEntries(Object.entries(quantiles(samples.map(sample => sample.ms))).map(([k, v]) => [k, Number(v.toFixed(2))])),
    }]));
    return { toolkit, nodes: initial.nodes.length, compactControls: compact.controls.length,
      visualFallbacks: initial.nodes.filter(node => node.vision_fallback).length, metrics,
      workflow: await verifyWorkflow(cli, tree, initial), samples: includeSamples ? values : undefined };
  } finally {
    await context.close();
    if (started) {
      await cli('stop');
      await eventually(async () => parse(await cli('status')), state => state.running === false, 'disposable session shutdown');
    }
  }
}

async function version(binary, args, stream = 'stdout') {
  try {
    return (await run(binary, args))[stream].toString().trim().split('\n')[0];
  } catch {
    return null;
  }
}

async function main() {
  assert.ok(Number.isInteger(rounds) && rounds >= 5 && rounds <= 300, 'sample count must be an integer between 5 and 300');
  const selected = process.argv.slice(2);
  assert.ok(selected.every(name => ['gtk', 'qt', 'electron'].includes(name)), 'select gtk, qt, electron, or omit to run all');
  const names = selected.length ? [...new Set(selected)] : ['gtk', 'qt', 'electron'];
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'opticdeck-bench-'));
  let browser;
  try {
    browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE || '/usr/bin/chromium', headless: true, chromiumSandbox: true });
    const fixtures = {
      gtk: [process.env.PYTHON || 'python3', path.join(__dirname, 'gtk.py')],
      qt: [path.join(dir, 'qt-bench')],
      electron: [electron, path.join(__dirname, 'electron.cjs')],
    };
    if (names.includes('qt')) {
      const { stdout } = await run('pkg-config', ['--cflags', '--libs', 'Qt6Widgets']);
      await run(process.env.CXX || 'c++', ['-std=c++17', '-O2', '-fPIC', path.join(__dirname, 'qt.cpp'),
        '-o', fixtures.qt[0], ...stdout.toString().trim().split(/\s+/)]);
    }
    const distro = await fs.readFile('/etc/os-release', 'utf8');
    const metadata = { timestamp: new Date().toISOString(), os: distro.match(/^PRETTY_NAME="?([^"\n]+)"?/m)?.[1],
      arch: process.arch, cpu: os.cpus()[0]?.model, node: process.version, chromium: browser.version(),
      playwright: require('playwright/package.json').version,
      tigervnc: await version(process.env.XVNC || 'Xvnc', ['-version'], 'stderr'),
      atspi: await version('pkg-config', ['--modversion', 'atspi-2']),
      gtk: names.includes('gtk') ? await version('pkg-config', ['--modversion', 'gtk+-3.0']) : undefined,
      qt: names.includes('qt') ? await version('pkg-config', ['--modversion', 'Qt6Widgets']) : undefined,
      electron: names.includes('electron') ? await version(electron, ['--version']) : undefined,
      opticdeckVersion: await version(opticdeck, ['--version']),
      opticdeckSha256: createHash('sha256').update(await fs.readFile(opticdeck)).digest('hex'),
      rounds, warmups, framebuffer: [width, height] };
    const results = [];
    for (const name of names) {
      try {
        results.push(await measure(name, fixtures[name], dir, browser));
      } catch (error) {
        results.push({ toolkit: name, error: String(error.message).replace(/token=[^&\s"']+/g, 'token=[redacted]') });
        process.exitCode = 1;
      }
    }
    console.log(JSON.stringify({ metadata, results }, null, 2));
  } finally {
    await browser?.close();
    await fs.rm(dir, { recursive: true, force: true });
  }
}

main().catch(error => {
  console.error(String(error.message).replace(/token=[^&\s"']+/g, 'token=[redacted]'));
  process.exitCode = 1;
});
