// Development-only DOM regression test. Runtime SemaDesk has no Node dependency.
// Run: NODE_PATH=/path/to/node_modules node tests/frontend.cjs
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');

const assets = path.resolve(__dirname, '../internal/web/assets');
const fixture = {
  type: 'snapshot', revision: 1, width: 1280, height: 800,
  control: { paused: false, owner: '' },
  agent_status: { phase: 'idle', label: 'Ready for a task' },
  nodes: [
    { id: 'win', role: 'frame', name: 'Demo', bounds: { x: 0, y: 0, width: 1000, height: 700 }, window: 1, z: 1, depth: 0, states: [], actions: [] },
    { id: 'button', parent: 'win', role: 'push button', name: 'Guardar', bounds: { x: 100, y: 100, width: 120, height: 35 }, window: 1, z: 1, depth: 1, states: ['enabled'], actions: ['click'] },
    { id: 'input', parent: 'win', role: 'entry', name: 'Nombre', value: 'original', bounds: { x: 100, y: 150, width: 220, height: 35 }, window: 1, z: 1, depth: 1, states: ['editable'], actions: ['set_text'] },
    { id: 'memo', parent: 'win', role: 'entry', name: 'Notas', value: 'Línea uno\nLínea dos', bounds: { x: 100, y: 205, width: 300, height: 90 }, window: 1, z: 1, depth: 1, states: ['editable', 'multiline', 'focused'], actions: ['set_text'] },
    { id: 'opaque', role: 'vision_fallback', name: 'Wine', bounds: { x: 1000, y: 0, width: 280, height: 600 }, window: 2, z: 2, depth: 0, states: [], actions: [], vision_fallback: true },
  ],
};

function installMock(snapshot) {
  window.__actions = [];
  window.__sockets = [];
  window.__snapshot = snapshot;
  class MockWebSocket extends EventTarget {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;
    constructor(url) {
      super();
      this.url = url;
      this.readyState = 0;
      window.__sockets.push(this);
      setTimeout(() => {
        this.readyState = 1;
        const event = new Event('open');
        this.dispatchEvent(event);
        this.onopen?.(event);
        if (new URL(url).pathname === '/ws') {
          window.__ws = this;
          this.emit(snapshot);
        }
      }, 5);
    }
    emit(message) {
      const event = new MessageEvent('message', { data: JSON.stringify(message) });
      this.dispatchEvent(event);
      this.onmessage?.(event);
    }
    send(data) {
      if (new URL(this.url).pathname !== '/ws') return;
      const message = JSON.parse(data);
      window.__actions.push(message);
      if (message.type === 'snapshot') { this.emit(window.__snapshot); return; }
      if (message.type === 'action' && message.node && window.__validateObservations) {
        const target = window.__snapshot.nodes.find(node => node.id === message.node);
        if (!target || (target.name || '') !== message.expect_name) {
          this.emit({ type: 'result', id: message.id, ok: false, code: 'stale_observation', error: 'raw backend detail' });
          return;
        }
      }
      if (message.type === 'takeover') {
        this.emit({ type: 'control', control: { paused: message.paused, owner: message.paused ? new URL(this.url).searchParams.get('client') : '' } });
        if (message.paused) this.emit({ type: 'lease', token: 'test-private-lease' });
      }
      if (message.action === 'set_text' && window.__failNextText) {
        window.__failNextText = false;
        this.emit({ type: 'result', id: message.id, ok: false, error: 'Test native rejection' });
        return;
      }
      if (message.action === 'set_text' && window.__holdNextText) {
        window.__holdNextText = false;
        window.__heldText = { socket: this, id: message.id };
        return;
      }
      if (message.id) this.emit({ type: 'result', id: message.id, ok: true });
    }
    close(code = 1000) {
      this.readyState = 3;
      const event = new CloseEvent('close', { code, wasClean: true });
      this.dispatchEvent(event);
      this.onclose?.(event);
    }
  }
  window.WebSocket = MockWebSocket;
}

async function checkStaleObservation(browser, origin) {
  const page = await browser.newPage();
  try {
    await page.addInitScript(installMock, fixture);
    await page.goto(`${origin}/?token=fixture`);
    await page.getByRole('button', { name: 'Guardar', exact: true }).waitFor();
    await page.evaluate(() => {
      window.__validateObservations = true;
      // A diff can reach the browser before its double-click debounce expires.
      document.getElementById('native-button').dispatchEvent(new MouseEvent('click', { detail: 1, bubbles: true }));
      const changed = { ...__snapshot.nodes[1], name: 'e4 white pawn' };
      __snapshot = { ...__snapshot, revision: 2, nodes: __snapshot.nodes.map(node => node.id === changed.id ? changed : node) };
      __ws.emit({ type: 'diff', base: 1, revision: 2, upsert: [changed], remove: [] });
    });
    const notice = page.getByRole('alert').filter({ hasText: 'The desktop changed' });
    await notice.waitFor();
    const stateAfterStale = await page.evaluate(() => ({
      clicks: __actions.filter(message => message.action === 'click').map(message => message.expect_name),
      refreshes: __actions.filter(message => message.type === 'snapshot').length,
    }));
    assert.deepEqual(stateAfterStale, { clicks: ['Guardar'], refreshes: 1 });
    assert.doesNotMatch(await notice.textContent(), /raw backend detail/);
    await page.getByRole('button', { name: 'e4 white pawn', exact: true }).click();
    await page.waitForFunction(() => __actions.filter(message => message.action === 'click').length === 2);
    assert.equal(await page.evaluate(() => __actions.findLast(message => message.action === 'click').expect_name), 'e4 white pawn');
    await notice.waitFor({ state: 'hidden' });

    await page.evaluate(() => {
      const nextButton = { ...__snapshot.nodes[1], name: 'e4 empty' };
      __ws.emit({ type: 'diff', base: 2, revision: 3, upsert: [nextButton], remove: [] });
      __ws.emit({ type: 'log', action: 'click', node: 'button', target_name: 'e4 white pawn', ok: true });
      const input = document.getElementById('native-input');
      input.value = 'Discard this old draft';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      const changed = { ...__snapshot.nodes[2], name: 'Updated name', value: 'New native value' };
      __snapshot = {
        ...__snapshot, revision: 4,
        nodes: __snapshot.nodes.map(node => [nextButton, changed].find(item => item.id === node.id) || node),
      };
    });
    assert.equal(await page.locator('#latest-action-text').textContent(), 'Click · e4 white pawn');
    await notice.waitFor();
    const input = page.getByRole('textbox', { name: 'Updated name', exact: true });
    await input.waitFor();
    assert.equal(await input.inputValue(), 'New native value');
    await input.dispatchEvent('change');
    await input.dispatchEvent('blur');
    assert.deepEqual(await page.evaluate(() => __actions.filter(message => message.action === 'set_text').map(message => message.expect_name)), ['Nombre']);
    await input.fill('New observed draft');
    await page.waitForFunction(() => __actions.filter(message => message.action === 'set_text').length === 2);
    assert.equal(await page.evaluate(() => __actions.findLast(message => message.action === 'set_text').expect_name), 'Updated name');
  } finally {
    await page.close();
  }
}

async function checkContactSheet(browser, origin) {
  const page = await browser.newPage();
  let downloads = 0;
  page.on('download', () => { downloads++; });
  try {
    await page.route('**/novnc/core/rfb.js', route => route.fulfill({
      contentType: 'text/javascript',
      body: `export default class FixtureRFB extends EventTarget {
        constructor(container) {
          super();
          const canvas = document.createElement('canvas');
          canvas.width = 480;
          canvas.height = 300;
          const context = canvas.getContext('2d');
          context.fillStyle = '#285879';
          context.fillRect(0, 0, canvas.width, canvas.height);
          container.appendChild(canvas);
          setTimeout(() => this.dispatchEvent(new Event('connect')), 5);
        }
        disconnect() {}
      }`,
    }));
    await page.addInitScript(installMock, fixture);
    await page.goto(`${origin}/?token=fixture`);
    await page.getByRole('button', { name: 'Capture contact sheet', exact: true }).click();
    const preview = page.getByRole('dialog', { name: 'Contact sheet' });
    await preview.waitFor();
    await page.waitForFunction(() => document.querySelector('#contact-sheet-image').naturalWidth > 0);
    const image = preview.getByRole('img', { name: /six sequential|6 sequential/ });
    assert.equal(await image.isVisible(), true);
    assert.deepEqual(await image.evaluate(img => [img.naturalWidth, img.naturalHeight]), [1488, 684]);
    const save = preview.getByRole('link', { name: 'Save JPEG' });
    assert.match(await save.getAttribute('href'), /^blob:/);
    assert.match(await save.getAttribute('download'), /^opticdeck-contact-sheet-.*\.jpg$/);
    assert.equal(downloads, 0, 'the preview does not write a file');
    await preview.getByRole('button', { name: 'Close', exact: true }).click();
    await preview.waitFor({ state: 'hidden' });

    const sheet = await page.evaluate(async () => {
      const { captureContactSheet } = await import('/contact-sheet.js');
      const source = document.createElement('canvas');
      source.width = 1920;
      source.height = 1080;
      const sourceContext = source.getContext('2d');
      const colors = [[240, 20, 25], [20, 235, 30], [35, 45, 235], [225, 218, 32]];
      function paint(index) {
        const color = colors[index];
        sourceContext.fillStyle = `rgb(${color.join(',')})`;
        sourceContext.fillRect(0, 0, source.width, source.height);
      }
      paint(0);
      const result = await captureContactSheet(() => source, {
        frameCount: 4, intervalMs: 150,
        onFrame: ({ number }) => { if (number < colors.length) paint(number); },
      });
      const bytes = new Uint8Array(await result.blob.arrayBuffer());
      const decoded = await createImageBitmap(result.blob);
      const context = result.canvas.getContext('2d');
      context.drawImage(decoded, 0, 0);
      const pixels = result.frames.map(({ x, y, width, height }) =>
        Array.from(context.getImageData(x + width / 2, y + height / 2, 1, 1).data).slice(0, 3));
      const resolution = [decoded.width, decoded.height];
      decoded.close();
      const controller = new AbortController();
      let canceledFrames = 0;
      let canceled;
      try {
        await captureContactSheet(() => source, {
          signal: controller.signal,
          onFrame: () => { canceledFrames++; controller.abort(); },
        });
      } catch (error) {
        canceled = error.name;
      }
      return {
        colors, pixels, resolution, canceled, canceledFrames, mime: result.blob.type, byteLength: bytes.length,
        jpegMarkers: [...bytes.slice(0, 2), ...bytes.slice(-2)], offsets: result.frames.map(frame => frame.atMs),
        order: result.frames.map(frame => frame.number),
      };
    });
    assert.deepEqual(sheet.resolution, [996, 624]);
    assert.deepEqual(sheet.order, [1, 2, 3, 4]);
    assert.equal(sheet.offsets[0], 0);
    assert.ok(sheet.offsets.slice(1).every((time, index) => time - sheet.offsets[index] >= 145), 'frames use sequential monotonic sampling');
    assert.equal(sheet.mime, 'image/jpeg');
    assert.ok(sheet.byteLength > 1000);
    assert.deepEqual(sheet.jpegMarkers, [0xff, 0xd8, 0xff, 0xd9]);
    sheet.pixels.forEach((pixel, index) => pixel.forEach((channel, component) => {
      assert.ok(Math.abs(channel - sheet.colors[index][component]) <= 8, `frame ${index + 1} preserves its own source pixels`);
    }));
    assert.equal(sheet.canceled, 'AbortError');
    assert.equal(sheet.canceledFrames, 1);
  } finally {
    await page.close();
  }
}

(async () => {
  const server = http.createServer((request, response) => {
    const pathname = new URL(request.url, 'http://fixture').pathname;
    const filename = path.join(assets, pathname === '/' ? '/index.html' : pathname);
    response.setHeader('Content-Type', pathname.endsWith('.js') ? 'text/javascript' : pathname.endsWith('.css') ? 'text/css' : 'text/html');
    fs.readFile(filename, (error, data) => {
      response.statusCode = error ? 404 : 200;
      response.end(error ? 'Not found' : data);
    });
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  let browser;
  try {
    browser = await chromium.launch({
      executablePath: process.env.CHROMIUM_EXECUTABLE || '/usr/bin/chromium',
      headless: true,
      args: process.env.SEMADESK_TEST_NO_SANDBOX === '1' ? ['--no-sandbox', '--disable-dev-shm-usage'] : [],
    });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = [];
    const controls = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route(/\/api\/(control|stop)$/, async route => {
      const request = route.request();
      const endpoint = new URL(request.url()).pathname;
      controls.push({ endpoint, method: request.method(), auth: request.headers().authorization, data: request.postDataJSON() });
      const result = endpoint === '/api/control'
        ? { ok: true, control: { paused: request.postDataJSON().paused, owner: '' } }
        : { ok: true };
      await route.fulfill({ json: result });
    });
    await page.addInitScript(installMock, fixture);
    await page.goto(`http://127.0.0.1:${server.address().port}/?token=fixture&keep=1#desktop-panel`);
    await page.waitForSelector('#native-button');
    assert.equal(new URL(page.url()).search, '?keep=1', 'only the token is removed from the address bar');
    assert.equal(new URL(page.url()).hash, '#desktop-panel');
    await page.reload();
    await page.waitForSelector('#native-button');
    const semanticSocketURL = await page.evaluate(() => __ws.url);
    assert.equal(new URL(semanticSocketURL).searchParams.get('token'), 'fixture', 'reload keeps the WebSocket session token');
    const agentStatus = page.locator('#agent-status');
    assert.equal(await agentStatus.getAttribute('data-phase'), 'idle');
    assert.equal(await agentStatus.getByText('Ready for a task', { exact: true }).isVisible(), true);
    await page.evaluate(() => __ws.emit({ type: 'agent_status', status: { phase: 'working', label: 'Checking the test window' } }));
    assert.equal(await agentStatus.getAttribute('data-phase'), 'working');
    assert.equal(await agentStatus.getByText('Checking the test window', { exact: true }).isVisible(), true);
    await page.locator('#native-input').press('Alt+Shift+P');
    assert.equal(controls.length, 0, 'typing in a native field keeps viewer shortcuts out');
    await page.getByRole('button', { name: 'Take control', exact: true }).focus();
    await page.keyboard.press('Alt+Shift+P');
    const resume = page.getByRole('button', { name: 'Resume agent', exact: true });
    await resume.waitFor();
    assert.equal(await page.getByRole('button', { name: 'Take control', exact: true }).isDisabled(), true);
    await resume.click();
    await page.getByRole('button', { name: 'Pause agent', exact: true }).waitFor();
    assert.deepEqual(controls, [true, false].map(paused => ({
      endpoint: '/api/control', method: 'POST', auth: 'Bearer fixture', data: { paused },
    })));
    assert.equal(await agentStatus.getAttribute('data-phase'), 'working', 'pausing does not invent an agent update');
    const appOutline = page.getByRole('combobox', { name: 'Outline app' });
    await appOutline.selectOption({ label: 'Demo' });
    assert.equal(await page.locator('#native-button').evaluate(element => element.classList.contains('window-outline')), true);
    assert.equal(await page.locator('#native-opaque').evaluate(element => element.classList.contains('window-outline')), false);
    assert.equal(await page.locator('#native-opaque').isVisible(), true, 'other windows remain visible');
    await appOutline.selectOption('');
    assert.equal(await page.getByText('No actions yet.', { exact: true }).isVisible(), true);
    await page.evaluate(() => __ws.emit({ type: 'log', action: 'click', node: 'button', ok: true }));
    assert.equal(await page.getByText('No actions yet.', { exact: true }).isVisible(), false);
    const activity = page.getByRole('list', { name: 'Action log' });
    assert.equal(await activity.getByText('Click', { exact: true }).isVisible(), true);
    assert.equal(await activity.getByText('Guardar', { exact: true }).isVisible(), true);
    assert.equal(await page.locator('#latest-action-text').textContent(), 'Click · Guardar');
    assert.equal(await page.locator('#native-button').evaluate(element => element.classList.contains('action-flash')), true);
    assert.equal(await page.getByRole('textbox', { name: 'Notas', exact: true }).evaluate(element => element.localName), 'textarea');
    assert.equal(await page.locator('#native-memo').getAttribute('data-native-focused'), 'true');
    assert.equal(await page.locator('#native-memo').getAttribute('data-states'), 'editable,multiline,focused');
    assert.equal(await page.getByRole('button', { name: 'Guardar', exact: true }).count(), 1);
    await page.getByRole('button', { name: 'Guardar', exact: true }).press('Tab');
    assert.equal(await page.evaluate(() => document.activeElement.id), 'native-input', 'Tab moves between browser controls');
    await page.getByRole('button', { name: 'Guardar', exact: true }).click();
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(() => __actions.filter(message => message.action === 'click').length), 1);
    await page.getByRole('button', { name: 'Guardar', exact: true }).dblclick();
    await page.waitForTimeout(300);
    assert.equal(await page.evaluate(() => __actions.filter(message => message.action === 'click').length), 1);
    assert.equal(await page.evaluate(() => __actions.filter(message => message.action === 'double_click').length), 1);
    await page.getByRole('textbox', { name: 'Nombre', exact: true }).fill('Español 世界');
    await page.waitForTimeout(240);
    assert.equal(await page.evaluate(() => __actions.findLast(message => message.action === 'set_text').text), 'Español 世界');
    await page.locator('#native-input').dispatchEvent('change');
    await page.locator('#native-input').dispatchEvent('blur');
    await page.waitForTimeout(30);
    assert.equal(await page.evaluate(() => __actions.filter(message => message.action === 'set_text').length), 1, 'change/blur must reuse successful debounce write');
    await page.evaluate(() => { window.__failNextText = true; });
    await page.locator('#native-input').fill('Reintento');
    await page.waitForTimeout(240);
    await page.locator('#native-input').dispatchEvent('change');
    await page.waitForTimeout(30);
    assert.equal(await page.evaluate(() => __actions.filter(message => message.action === 'set_text' && message.text === 'Reintento').length), 2, 'failed text must remain retryable');
    for (const value of ['A', 'B', 'A']) {
      await page.locator('#native-input').fill(value);
      await page.waitForTimeout(240);
    }
    assert.deepEqual(await page.evaluate(() => __actions.filter(message => message.action === 'set_text').slice(-3).map(message => message.text)), ['A', 'B', 'A']);
    await page.evaluate(() => __ws.emit({ type: 'diff', base: 1, revision: 2, upsert: [{ ...__snapshot.nodes[2], value: 'Normalizado' }], remove: [] }));
    await page.locator('#native-input').fill('A');
    await page.waitForTimeout(240);
    assert.equal(await page.evaluate(() => __actions.filter(message => message.action === 'set_text' && message.text === 'A').length), 3, 'new remote value must invalidate the last acknowledged text');
    await page.getByRole('button', { name: 'Guardar', exact: true }).press('PageUp');
    await page.getByRole('button', { name: 'Guardar', exact: true }).press('PageDown');
    assert.equal(await page.evaluate(() => __actions.some(message => message.action === 'press_key' && message.key === 'PageUp')), true);
    assert.equal(await page.evaluate(() => __actions.some(message => message.action === 'press_key' && message.key === 'PageDown')), true);
    await page.evaluate(() => __ws.emit({
      type: 'diff', base: 2, revision: 3,
      upsert: [
        { ...__snapshot.nodes[1], name: 'Listo', bounds: { x: 110, y: 101, width: 120, height: 35 } },
        { ...__snapshot.nodes[2], states: ['editable', 'protected'], value: '' },
        { ...__snapshot.nodes[3], states: ['editable', 'multi-line'] },
      ], remove: [],
    }));
    assert.equal(await page.getByRole('button', { name: 'Listo', exact: true }).count(), 1);
    assert.equal(await page.locator('#semantic-overlay').getAttribute('data-revision'), '3');
    assert.equal(await page.locator('#native-input').getAttribute('type'), 'password');
    assert.equal(await page.locator('#native-input').inputValue(), '');
    await page.evaluate(() => __ws.emit({
      type: 'log', action: 'set_text', node: 'input', ok: false,
      text: 'private-typed-text', key: 'private-key', clipboard: 'private-clipboard', error: 'private-error',
    }));
    assert.equal(await page.locator('#latest-action-text').textContent(), 'Edit text failed · Protected field');
    assert.equal(await activity.getByText('Protected field', { exact: true }).isVisible(), true);
    assert.doesNotMatch(await activity.innerText(), /Nombre|private-/);
    assert.equal(await page.locator('#native-input').evaluate(element => element.classList.contains('action-failed')), true);
    assert.equal(await page.locator('#native-memo').getAttribute('data-native-focused'), 'false');
    assert.equal(await page.locator('#native-memo').evaluate(element => element.localName), 'textarea');
    await page.evaluate(() => __ws.emit({ type: 'diff', base: 90, revision: 91, upsert: [], remove: [] }));
    assert.equal(await page.evaluate(() => __actions.filter(message => message.type === 'snapshot').length), 1);
    await page.evaluate(() => { window.__holdNextText = true; });
    await page.getByRole('textbox', { name: 'Nombre', exact: true }).fill('En vuelo');
    await page.waitForTimeout(240);
    await page.getByRole('button', { name: 'Take control', exact: true }).click();
    await page.waitForTimeout(40);
    assert.equal(await page.evaluate(() => __actions.filter(message => message.type === 'takeover').length), 0, 'takeover must wait for the text acknowledgment');
    assert.equal(await page.evaluate(() => __actions.filter(message => message.action === 'set_text' && message.text === 'En vuelo').length), 1, 'in-flight change/blur must reuse its write');
    await page.evaluate(() => __heldText.socket.emit({ type: 'result', id: __heldText.id, ok: true }));
    await page.waitForTimeout(150);
    assert.equal(await page.locator('#semantic-overlay').evaluate(element => element.classList.contains('manual')), true);
    assert.equal(await page.locator('#clipboard').isDisabled(), false);
    const rfbURL = await page.evaluate(() => __sockets.filter(socket => new URL(socket.url).pathname === '/rfb').at(-1).url);
    assert.equal(new URL(rfbURL).searchParams.get('lease'), 'test-private-lease');
    await page.setViewportSize({ width: 360, height: 660 });
    assert.equal(await page.locator('#latest-action').isVisible(), true, 'narrow view keeps the latest action visible');
    assert.equal(await page.getByRole('region', { name: 'Session clipboard' }).isVisible(), true, 'narrow view keeps the clipboard available');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), 360, 'narrow view has no horizontal page scroll');
    await page.getByRole('button', { name: 'Return control to agent', exact: true }).click();
    await page.waitForTimeout(150);
    assert.equal(await page.locator('#semantic-overlay').evaluate(element => element.classList.contains('manual')), false);
    assert.equal(await page.locator('#clipboard').isDisabled(), true);
    await page.getByRole('button', { name: 'Pause agent', exact: true }).focus();
    await page.keyboard.press('Alt+Shift+Escape');
    const stopDialog = page.getByRole('dialog', { name: 'Stop the virtual session?' });
    await stopDialog.waitFor();
    await stopDialog.getByRole('button', { name: 'Keep running' }).click();
    await stopDialog.waitFor({ state: 'hidden' });
    assert.equal(controls.length, 2, 'canceling never sends a stop request');
    await page.getByRole('button', { name: 'Stop session', exact: true }).click();
    await stopDialog.getByRole('button', { name: 'Stop session', exact: true }).click();
    await stopDialog.waitFor({ state: 'hidden' });
    assert.deepEqual(controls.at(-1), { endpoint: '/api/stop', method: 'POST', auth: 'Bearer fixture', data: null });
    assert.equal(await page.locator('#connection').textContent(), 'Session stopped');
    assert.deepEqual(errors, []);
    const origin = `http://127.0.0.1:${server.address().port}`;
    await checkStaleObservation(browser, origin);
    await checkContactSheet(browser, origin);
    console.log('PASS frontend: ARIA, UTF-8 input, stale observations, activity, app outlines, agent state, pause, takeover, clipboard, stop confirmation, and timed JPEG contact sheets');
  } finally {
    if (browser) await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
