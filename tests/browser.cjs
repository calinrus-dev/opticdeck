// Real end-to-end test. First launch tests/fixture.ui in a running OpticDeck.
// SEMADESK_STATE=/private/state/state.json node tests/browser.cjs
// Alternatively set SEMADESK_URL to the complete session URL (including token).
// Requires development dependency `playwright` and a Chromium installation.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');

function sessionURL() {
  if (process.env.SEMADESK_URL) return process.env.SEMADESK_URL;
  const state = JSON.parse(fs.readFileSync(process.env.SEMADESK_STATE || '/tmp/semadesk-integration-state/state.json', 'utf8'));
  const url = new URL(state.url);
  if (state.token) url.searchParams.set('token', state.token);
  return url.href;
}

async function eventually(check, label, timeout = 12000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise(resolve => setTimeout(resolve, 120));
  }
  throw new Error(`Timed out waiting for ${label}`);
}

(async () => {
  const url = new URL(sessionURL());
  const browser = await chromium.launch({
    executablePath: process.env.CHROMIUM_EXECUTABLE || '/usr/bin/chromium',
    headless: true,
    args: [
      '--disable-background-timer-throttling', '--disable-backgrounding-occluded-windows', '--disable-renderer-backgrounding',
      ...(process.env.SEMADESK_TEST_NO_SANDBOX === '1' ? ['--no-sandbox', '--disable-dev-shm-usage'] : []),
    ],
  });
  try {
    const context = await browser.newContext({ viewport: { width: 1600, height: 1100 } });
    const page = await context.newPage();
    const pageErrors = [];
    const websocketURLs = [];
    page.on('pageerror', error => pageErrors.push(error.message));
    page.on('websocket', socket => websocketURLs.push(socket.url()));
    await page.goto(url.href);
    const headers = { Authorization: `Bearer ${url.searchParams.get('token') || ''}` };
    const api = route => new URL(route, url.origin).href;
    async function tree() {
      const response = await context.request.get(api('/api/tree'), { headers });
      assert.equal(response.status(), 200, 'tree API must authenticate');
      return response.json();
    }
    await page.waitForFunction(() => document.getElementById('connection').textContent.includes('VNC + DOM'), null, { timeout: 15000 });
    assert.equal(new URL(page.url()).searchParams.has('token'), false, 'token must leave the address bar');
    await page.reload();
    await page.waitForFunction(() => document.getElementById('connection').textContent.includes('VNC + DOM'), null, { timeout: 15000 });
    await page.waitForSelector('#rfb-screen canvas');
    const statusReply = await context.request.post(api('/api/agent-status'), { headers, data: { phase: 'working', label: 'Checking the demo' } });
    assert.equal(statusReply.status(), 200);
    await page.getByText('Checking the demo', { exact: true }).waitFor();
    await page.getByRole('button', { name: 'Pause agent', exact: true }).click();
    await page.getByRole('button', { name: 'Resume agent', exact: true }).waitFor();
    const pausedAction = await context.request.post(api('/api/act'), { headers, data: { action: 'press_key', key: 'Escape' } });
    assert.equal(pausedAction.status(), 409, 'pause must block native actions');
    await page.reload();
    await page.getByRole('button', { name: 'Resume agent', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Resume agent', exact: true }).click();
    await page.getByRole('button', { name: 'Pause agent', exact: true }).waitFor();
    const editor = page.getByRole('textbox', { name: 'Editor Unicode', exact: true });
    const checkbox = page.getByRole('checkbox', { name: 'Comprobación', exact: true });
    await editor.waitFor();
    await checkbox.waitFor();
    const initial = await tree();
    assert.equal(JSON.stringify(initial).includes('fixture-protected-2FA-9381'), false, 'password must be redacted');
    assert.equal(await page.getByRole('textbox', { name: 'Contraseña de prueba', exact: true }).inputValue(), '');
    const editorNode = initial.nodes.find(node => node.name === 'Editor Unicode');
    const checkboxNode = initial.nodes.find(node => node.name === 'Comprobación' && /check/i.test(node.role));
    assert.ok(editorNode && checkboxNode, 'GTK fixture must expose editor and checkbox through AT-SPI');
    const stale = await context.request.post(api('/api/act'), { headers, data: {
      action: 'click', node: checkboxNode.id, expect_name: 'Old checkbox label',
    } });
    assert.equal(stale.status(), 409);
    assert.equal((await stale.json()).code, 'stale_observation');
    const fresh = await context.request.get(api('/api/tree?fresh=1'), { headers });
    assert.equal(fresh.status(), 200);
    assert.ok((await fresh.json()).nodes.some(node => node.id === checkboxNode.id));
    const capture = await context.request.post(api('/api/capture'), { headers, data: {
      frames: 2, interval_ms: 100, x: 10, y: 10, width: 200, height: 120,
    } });
    assert.equal(capture.status(), 200);
    assert.equal(capture.headers()['x-opticdeck-grid'], '2x1');
    assert.equal(capture.headers()['x-opticdeck-region'], '(10,10) 200x120');
    assert.equal((await capture.body()).readUInt16BE(), 0xffd8);
    await page.getByRole('button', { name: 'Capture contact sheet' }).click();
    const gridPreview = page.getByRole('dialog', { name: 'Contact sheet' });
    await gridPreview.getByRole('img').waitFor();
    await gridPreview.getByRole('button', { name: 'Close' }).click();
    const checkedBefore = (checkboxNode.states || []).includes('checked');
    const value = 'OpticDeck · España 世界 🦊';
    await editor.fill(value);
    await eventually(async () => (await tree()).nodes.find(node => node.id === editorNode.id)?.value === value, 'EditableText UTF-8 native value');
    await checkbox.click();
    await eventually(async () => ((await tree()).nodes.find(node => node.id === checkboxNode.id)?.states || []).includes('checked') !== checkedBefore, 'native checkbox action');
    await page.getByRole('button', { name: 'Take control', exact: true }).click();
    await page.getByRole('button', { name: 'Return control to agent', exact: true }).waitFor();
    const rejected = await context.request.post(api('/api/act'), { headers, data: { action: 'press_key', key: 'Escape' } });
    assert.equal(rejected.status(), 409, 'human takeover must block semantic actions');
    await eventually(async () => websocketURLs.some(candidate => new URL(candidate).pathname === '/rfb' && new URL(candidate).searchParams.has('lease')), 'private manual RFB lease');
    await page.locator('#clipboard').fill(value);
    await page.getByRole('button', { name: 'Send to session', exact: true }).click();
    await page.locator('#clipboard').fill('');
    await page.getByRole('button', { name: 'Read from session', exact: true }).click();
    await eventually(async () => await page.locator('#clipboard').inputValue() === value, 'bidirectional UTF-8 clipboard');
    await page.getByRole('button', { name: 'Return control to agent', exact: true }).click();
    await page.getByRole('button', { name: 'Take control', exact: true }).waitFor();
    assert.equal(await page.locator('#clipboard').isDisabled(), true);
    await editor.fill(editorNode.value || '');
    await eventually(async () => (await tree()).nodes.find(node => node.id === editorNode.id)?.value === (editorNode.value || ''), 'fixture restoration');
    await checkbox.click();
    await eventually(async () => ((await tree()).nodes.find(node => node.id === checkboxNode.id)?.states || []).includes('checked') === checkedBefore, 'checkbox restoration');
    assert.deepEqual(pageErrors, [], 'browser modules must run without uncaught errors');
    if (process.env.SEMADESK_SCREENSHOT) {
      await page.locator('#hitboxes').check();
      await page.screenshot({ path: process.env.SEMADESK_SCREENSHOT, fullPage: true });
    }
    console.log('PASS real browser: noVNC, AT-SPI, stale target guard, capture, pause/reconnect, agent status, Unicode editing, takeover, clipboard');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
