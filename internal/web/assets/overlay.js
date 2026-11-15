import RFB from './novnc/core/rfb.js';

const $ = id => document.getElementById(id);
const overlay = $('semantic-overlay');
const stage = $('stage');
const viewport = $('viewport');
const frame = $('scaled-frame');
const actionLog = $('action-log');
const latestAction = $('latest-action');
const elements = new Map();
const nodes = new Map();
const pending = new Map();
const pageURL = new URL(location.href);
const urlToken = pageURL.searchParams.get('token');
let token = '';
let client;
try {
  // Keep existing session keys so tabs that already hid the token can reload.
  if (urlToken !== null) sessionStorage.setItem('semadesk.token', urlToken);
  token = urlToken ?? sessionStorage.getItem('semadesk.token') ?? '';
  client = sessionStorage.getItem('semadesk.client');
  if (!client) {
    client = crypto.randomUUID();
    sessionStorage.setItem('semadesk.client', client);
  }
  if (urlToken !== null) {
    pageURL.searchParams.delete('token');
    history.replaceState(history.state, '', pageURL);
  }
} catch {
  token = urlToken ?? '';
  client = crypto.randomUUID();
}

let socket;
let rfb;
let rfbReady = false;
let reconnectTimer;
let rfbTimer;
let stopped = false;
let revision = 0;
let width = 1280;
let height = 800;
let haveSnapshot = false;
let syncPending = false;
let control = { paused: false, owner: '' };
let lease = '';
let reconnectAttempt = 0;
let textTimers = new Map();
let textWrites = new Map();
let clickTimers = new Map();
let dirty = new Map();
let composing = new Set();
let highlighted;
let highlightTimer;

function endpoint(path) {
  const url = new URL(path, location.href);
  url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
  if (token) url.searchParams.set('token', token);
  url.searchParams.set('client', client);
  if (path === '/rfb' && isOwner() && lease) url.searchParams.set('lease', lease);
  return url.href;
}

function isOwner() {
  return control.paused && control.owner === client;
}

function online() {
  return socket && socket.readyState === WebSocket.OPEN;
}

function setStatus(text, state) {
  if ($('connection').textContent === text) return;
  $('connection').textContent = text;
  $('connection').className = `status ${state}`;
}

function updateConnectedStatus() {
  if (!online()) return;
  if (rfbReady && haveSnapshot) setStatus('Connected · VNC + DOM', 'online');
  else if (haveSnapshot) setStatus('Connecting VNC…', 'syncing');
  else setStatus('Syncing desktop…', 'syncing');
}

function showError(message) {
  $('error').textContent = message;
  $('error').hidden = false;
}

function clearError() {
  $('error').hidden = true;
  $('error').textContent = '';
}

function send(message) {
  if (!online()) throw new Error('Disconnected from OpticDeck');
  socket.send(JSON.stringify(message));
}

function request(message) {
  const id = crypto.randomUUID();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error('OpticDeck did not confirm the operation in time'));
    }, 15000);
    pending.set(id, { resolve, reject, timer });
    try {
      send({ ...message, id });
    } catch (error) {
      clearTimeout(timer);
      pending.delete(id);
      reject(error);
    }
  });
}

function rejectPending() {
  for (const entry of pending.values()) {
    clearTimeout(entry.timer);
    entry.reject(new Error('The connection closed before OpticDeck confirmed the operation'));
  }
  pending.clear();
}

function perform(node, action, extra = {}) {
  if (!haveSnapshot || syncPending) return Promise.reject(new Error('The desktop controls are syncing'));
  if (control.paused) return Promise.reject(new Error('Manual control paused the agent'));
  highlightAction(node);
  return request({ type: 'action', node, action, ...extra });
}

function run(promise) {
  promise.catch(error => showError(error.message));
}

function scale() {
  const availableWidth = Math.max(1, viewport.clientWidth - 2);
  const availableHeight = Math.max(1, viewport.clientHeight - 2);
  const ratio = Math.min(1, availableWidth / width, availableHeight / height);
  stage.style.width = `${width}px`;
  stage.style.height = `${height}px`;
  stage.style.transform = `scale(${ratio})`;
  stage.style.setProperty('--action-ring', `${Math.min(10, 3 / ratio)}px`);
  frame.style.width = `${Math.ceil(width * ratio)}px`;
  frame.style.height = `${Math.ceil(height * ratio)}px`;
}

new ResizeObserver(scale).observe(viewport);

function setDimensions(w, h) {
  if (!Number.isFinite(w) || !Number.isFinite(h) || w <= 0 || h <= 0 || (w === width && h === height)) return;
  width = w;
  height = h;
  scale();
}

function normalizeRole(node) {
  const role = String(node.role || '').toLowerCase().replace(/[ _]+/g, '-');
  return ({
    'push-button': 'button', 'toggle-button': 'button', 'check-box': 'checkbox',
    'radio-button': 'radio', 'menu-item': 'menuitem', 'check-menu-item': 'menuitemcheckbox',
    'radio-menu-item': 'menuitemradio', 'text-entry': 'textbox', 'entry': 'textbox',
    'password-text': 'textbox', 'page-tab': 'tab', 'page-tab-list': 'tablist',
    'combo-box': 'combobox', 'list-box': 'listbox', 'list-item': 'option',
    'progress-bar': 'progressbar', 'spin-button': 'spinbutton',
    'scroll-bar': 'scrollbar', 'tool-bar': 'toolbar', 'status-bar': 'status',
    'frame': 'dialog', 'window': 'dialog', 'label': 'text',
    'paragraph': 'text', 'document-text': 'document', 'document-frame': 'document',
    'panel': 'group', 'filler': 'group', 'vision-fallback': 'region',
  })[role] || role;
}

function isEditable(node, role) {
  return ['textbox', 'combobox', 'spinbutton'].includes(role) ||
    (node.states || []).includes('editable');
}

function tagFor(node, role) {
  if (node.vision_fallback) return 'div';
  if (isEditable(node, role)) return (node.states || []).some(state => state === 'multi-line' || state === 'multiline') ? 'textarea' : 'input';
  if (role === 'checkbox' || role === 'radio') return 'input';
  if (['button', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'tab', 'link'].includes(role)) return 'button';
  return 'div';
}

function flushText(id) {
  clearTimeout(textTimers.get(id));
  textTimers.delete(id);
  const previous = textWrites.get(id);
  if (!dirty.has(id)) return previous?.promise || Promise.resolve();
  const value = dirty.get(id);
  dirty.delete(id);
  if (previous?.text === value) return previous.promise;
  const write = { text: value, promise: null, settled: false, remoteBefore: '' };
  // Serializing each field preserves A -> B -> A, including edits made while
  // an earlier write is still in flight. Repeated change/blur shares its promise.
  write.promise = (previous?.promise || Promise.resolve()).catch(() => {}).then(() => {
    write.remoteBefore = nodes.get(id)?.value || '';
    return perform(id, 'set_text', { text: value });
  }).then(result => {
    write.settled = true;
    const remote = nodes.get(id)?.value || '';
    if (textWrites.get(id) === write && remote !== write.remoteBefore && remote !== value) textWrites.delete(id);
    return result;
  }, error => {
    if (textWrites.get(id) === write) {
      textWrites.delete(id);
      if (online() && nodes.has(id) && !dirty.has(id) && elements.get(id)?.value === value) dirty.set(id, value);
    }
    throw error;
  });
  textWrites.set(id, write);
  return write.promise;
}

function queueText(id, element) {
  dirty.set(id, element.value);
  clearTimeout(textTimers.get(id));
  if (!composing.has(id)) {
    textTimers.set(id, setTimeout(() => run(flushText(id)), 180));
  }
}

const keyNames = { ' ': 'Space', ArrowUp: 'Up', ArrowDown: 'Down', ArrowLeft: 'Left', ArrowRight: 'Right', Escape: 'Escape', Delete: 'Delete', Backspace: 'BackSpace', PageUp: 'PageUp', PageDown: 'PageDown' };

function onKey(event, id, editable) {
  if (event.key === 'Tab' || event.isComposing || composing.has(id)) return;
  const modifier = ['Control', 'Shift', 'Alt', 'Meta', 'AltGraph'].includes(event.key);
  if (modifier) return;
  const special = ['Enter', 'Escape', 'ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End', 'PageUp', 'PageDown', 'Delete', 'Backspace', ' '].includes(event.key) || /^F\d{1,2}$/.test(event.key);
  const chord = event.ctrlKey || event.altKey || event.metaKey;
  // Native input editing stays local until a single EditableText update. Textbox
  // navigation and select/copy/paste keep standard browser editing semantics.
  if (editable && !['Enter', 'Escape'].includes(event.key) && !event.altKey && !event.metaKey && (!event.ctrlKey || ['a', 'c', 'v', 'x', 'z', 'y'].includes(event.key.toLowerCase()))) return;
  if (!special && !chord && !editable) return;
  if (!special && !chord && editable) return;
  event.preventDefault();
  event.stopPropagation();
  const key = keyNames[event.key] || event.key;
  const prefix = [event.ctrlKey && 'Ctrl', event.altKey && 'Alt', event.shiftKey && 'Shift', event.metaKey && 'Super'].filter(Boolean);
  run(flushText(id).then(() => chord || event.shiftKey
    ? perform(id, 'shortcut', { key: [...prefix, key].join('+') })
    : perform(id, 'press_key', { key })));
}

function nativePoint(event) {
  const rect = stage.getBoundingClientRect();
  return {
    x: Math.min(width - 1, Math.max(0, Math.floor((event.clientX - rect.left) * width / rect.width))),
    y: Math.min(height - 1, Math.max(0, Math.floor((event.clientY - rect.top) * height / rect.height))),
  };
}

function createElement(node, role, tag) {
  const element = document.createElement(tag);
  element.id = `native-${node.id}`;
  element.dataset.nodeId = node.id;
  element.className = 'semantic-node';
  if (tag === 'button') element.type = 'button';
  const editable = isEditable(node, role) && !node.vision_fallback;
  if (tag === 'input') {
    element.type = role === 'checkbox' ? 'checkbox' : role === 'radio' ? 'radio' : (node.states || []).includes('protected') || String(node.role).includes('password') ? 'password' : 'text';
    if (editable) { element.autocomplete = 'off'; element.spellcheck = false; }
  }
  element.addEventListener('click', event => {
    event.stopPropagation();
    if (control.paused || !haveSnapshot) { event.preventDefault(); return; }
    if (editable) {
      run(perform(node.id, 'focus'));
      return;
    }
    if (role === 'checkbox' || role === 'radio') event.preventDefault();
    // Collapse the two click events preceding dblclick into one double action.
    // Programmatic element.click() has detail=0 and can dispatch immediately.
    const point = node.vision_fallback ? nativePoint(event) : {};
    clearTimeout(clickTimers.get(node.id));
    if (event.detail === 0) run(perform(node.id, 'click', point));
    else if (event.detail === 1) clickTimers.set(node.id, setTimeout(() => {
      clickTimers.delete(node.id);
      run(perform(node.id, 'click', point));
    }, 240));
  });
  element.addEventListener('dblclick', event => {
    event.preventDefault();
    event.stopPropagation();
    clearTimeout(clickTimers.get(node.id));
    clickTimers.delete(node.id);
    run(perform(node.id, 'double_click', node.vision_fallback ? nativePoint(event) : {}));
  });
  element.addEventListener('contextmenu', event => {
    event.preventDefault();
    event.stopPropagation();
    run(perform(node.id, 'right_click', node.vision_fallback ? nativePoint(event) : {}));
  });
  element.addEventListener('keydown', event => onKey(event, node.id, editable));
  if (editable) {
    element.addEventListener('compositionstart', () => composing.add(node.id));
    element.addEventListener('compositionend', () => { composing.delete(node.id); queueText(node.id, element); });
    element.addEventListener('input', () => queueText(node.id, element));
    element.addEventListener('change', () => { dirty.set(node.id, element.value); run(flushText(node.id)); });
    element.addEventListener('blur', () => run(flushText(node.id)));
  }
  return element;
}

const validRoles = new Set(['button', 'textbox', 'checkbox', 'radio', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'tab', 'tablist', 'combobox', 'listbox', 'option', 'slider', 'spinbutton', 'progressbar', 'scrollbar', 'toolbar', 'status', 'dialog', 'alertdialog', 'document', 'group', 'region', 'menu', 'menubar', 'tree', 'treeitem', 'treegrid', 'grid', 'row', 'cell', 'columnheader', 'rowheader', 'table', 'link', 'heading', 'separator', 'img', 'tooltip', 'alert', 'application', 'log', 'list']);

function attribute(element, name, value) {
  if (value === undefined || value === null || value === '') element.removeAttribute(name);
  else element.setAttribute(name, String(value));
}

function upsert(node) {
  const previousNode = nodes.get(node.id);
  nodes.set(node.id, node);
  const role = normalizeRole(node);
  const tag = tagFor(node, role);
  let element = elements.get(node.id);
  const editable = isEditable(node, role) && !node.vision_fallback;
  if (!element || element.localName !== tag || element.dataset.semanticRole !== role) {
    const replacement = createElement(node, role, tag);
    if (element) element.replaceWith(replacement);
    else overlay.appendChild(replacement);
    element = replacement;
    elements.set(node.id, element);
  }
  element.dataset.semanticRole = role;
  element.dataset.parentId = node.parent || '';
  element.dataset.window = String(node.window || 0);
  element.dataset.actions = (node.actions || []).join(',');
  element.dataset.states = (node.states || []).join(',');
  attribute(element, 'data-shortcut', node.shortcut);
  attribute(element, 'data-vision-fallback', node.vision_fallback ? 'true' : null);
  const states = new Set(node.states || []);
  element.dataset.nativeFocused = String(states.has('focused'));
  if (editable && tag === 'input') {
    element.type = states.has('protected') || String(node.role).includes('password') ? 'password' : 'text';
  }
  const disabled = states.has('disabled') || states.has('defunct');
  const interactive = !!node.vision_fallback || editable || (node.actions || []).length > 0 || ['button', 'checkbox', 'radio', 'menuitem', 'menuitemcheckbox', 'menuitemradio', 'tab', 'link', 'slider', 'option', 'treeitem'].includes(role);
  element.classList.toggle('interactive', interactive);
  element.classList.toggle('editable', editable);
  const bounds = node.bounds || {};
  element.style.left = `${Number(bounds.x) || 0}px`;
  element.style.top = `${Number(bounds.y) || 0}px`;
  element.style.width = `${Math.max(0, Number(bounds.width) || 0)}px`;
  element.style.height = `${Math.max(0, Number(bounds.height) || 0)}px`;
  element.style.zIndex = String(Math.max(0, Number(node.z) || 0) * 10000 + Math.min(9998, Math.max(0, Number(node.depth) || 0)) + (interactive ? 1 : 0));
  attribute(element, 'role', validRoles.has(role) ? role : null);
  attribute(element, 'aria-label', node.vision_fallback ? `${node.name || 'Application'} · visual controls only` : node.name || (editable ? 'Text' : null));
  attribute(element, 'aria-disabled', disabled ? 'true' : null);
  attribute(element, 'aria-checked', ['checkbox', 'radio', 'menuitemcheckbox', 'menuitemradio'].includes(role) ? states.has('indeterminate') ? 'mixed' : states.has('checked') ? 'true' : 'false' : null);
  attribute(element, 'aria-pressed', role === 'button' && (states.has('checked') || states.has('checkable')) ? String(states.has('checked')) : null);
  attribute(element, 'aria-selected', ['tab', 'option', 'treeitem', 'row'].includes(role) ? String(states.has('selected')) : null);
  attribute(element, 'aria-expanded', states.has('expandable') || role === 'combobox' ? String(states.has('expanded')) : null);
  attribute(element, 'aria-required', states.has('required') ? 'true' : null);
  attribute(element, 'aria-multiline', editable && tag === 'textarea' ? 'true' : null);
  attribute(element, 'aria-readonly', editable && states.has('read-only') ? 'true' : null);
  attribute(element, 'aria-keyshortcuts', node.shortcut || null);
  element.tabIndex = interactive && !disabled ? 0 : -1;
  if ('disabled' in element) element.disabled = disabled;
  if (tag === 'input' && ['checkbox', 'radio'].includes(role)) {
    element.checked = states.has('checked');
    element.indeterminate = states.has('indeterminate');
  } else if (editable) {
    const value = node.value || '';
    const lastWrite = textWrites.get(node.id);
    if (lastWrite?.settled && previousNode && value !== (previousNode.value || '') && value !== lastWrite.text) textWrites.delete(node.id);
    // A protected native field never returns its value. Preserve the user's
    // local draft while focused, then clear it as soon as focus leaves.
    const protectedField = element.type === 'password';
    if (!dirty.has(node.id) && (!protectedField || document.activeElement !== element)) element.value = value;
    element.readOnly = states.has('read-only');
  } else {
    element.textContent = node.value || node.name || '';
  }
}

function remove(id) {
  if (highlighted && elements.get(id) === highlighted) clearActionHighlight();
  elements.get(id)?.remove();
  elements.delete(id);
  nodes.delete(id);
  dirty.delete(id);
  textWrites.delete(id);
  clearTimeout(textTimers.get(id));
  textTimers.delete(id);
  clearTimeout(clickTimers.get(id));
  clickTimers.delete(id);
  composing.delete(id);
}

function updateHierarchy() {
  const children = new Map();
  for (const node of nodes.values()) {
    if (node.parent && nodes.has(node.parent)) {
      if (!children.has(node.parent)) children.set(node.parent, []);
      children.get(node.parent).push(`native-${node.id}`);
    }
  }
  for (const [id, element] of elements) {
    // aria-owns mirrors the AT-SPI hierarchy without nesting absolute boxes,
    // which would otherwise apply parent coordinates a second time.
    attribute(element, 'aria-owns', children.get(id)?.join(' '));
  }
}

function updateMetrics() {
  $('metrics').textContent = `${width} × ${height} · ${nodes.size} ${nodes.size === 1 ? 'node' : 'nodes'} · r${revision}`;
  overlay.dataset.revision = String(revision);
}

function syncSnapshot(message) {
  const incoming = new Set((message.nodes || []).map(node => node.id));
  for (const id of nodes.keys()) if (!incoming.has(id)) remove(id);
  for (const node of message.nodes || []) upsert(node);
  revision = message.revision;
  haveSnapshot = true;
  syncPending = false;
  overlay.classList.remove('disconnected');
  setDimensions(message.width, message.height);
  updateHierarchy();
  updateMetrics();
  setControl(message.control || control);
  updateConnectedStatus();
  clearError();
}

function syncDiff(message) {
  if (!haveSnapshot || syncPending || message.base !== revision) {
    if (!syncPending) {
      syncPending = true;
      send({ type: 'snapshot' });
    }
    return;
  }
  for (const id of message.remove || []) remove(id);
  for (const node of message.upsert || []) upsert(node);
  revision = message.revision;
  setDimensions(message.width, message.height);
  updateHierarchy();
  updateMetrics();
}

function setControl(next) {
  const previousOwner = isOwner();
  const wasPaused = control.paused;
  control = { paused: !!next.paused, owner: next.owner || '' };
  const owner = isOwner();
  if (!owner) lease = '';
  $('takeover').disabled = !online() || !haveSnapshot || (control.paused && !owner);
  $('takeover').textContent = owner ? 'Return control to agent' : control.paused ? 'Another tab has control' : 'Take control';
  $('takeover').classList.toggle('active', owner);
  $('mode').textContent = owner ? 'Manual control · Agent paused' : control.paused ? 'Agent paused in another tab' : 'Observer mode · VNC input off';
  $('mode').classList.toggle('manual', control.paused);
  overlay.classList.toggle('manual', owner);
  for (const id of ['clipboard', 'clipboard-read', 'clipboard-write']) $(id).disabled = !owner || !online();
  if (!owner) $('clipboard').value = '';
  if (rfb) rfb.viewOnly = !owner;
  if (previousOwner !== owner || wasPaused !== control.paused) {
    // The daemon revokes every RFB connection when ownership changes. Creating
    // a new one here also covers notifications arriving before the close frame.
    connectRFB(120);
  }
}

const actionLabels = new Map([
  ['click', 'Click'], ['double_click', 'Double click'], ['right_click', 'Right click'],
  ['focus', 'Focus'], ['set_text', 'Edit text'], ['press_key', 'Key press'], ['shortcut', 'Shortcut'],
]);
const namedTargetRoles = new Map([
  ['button', 'Button'], ['checkbox', 'Checkbox'], ['radio', 'Radio button'], ['menuitem', 'Menu item'],
  ['menuitemcheckbox', 'Menu item'], ['menuitemradio', 'Menu item'], ['tab', 'Tab'],
  ['link', 'Link'], ['slider', 'Slider'],
]);

function actionTarget(id) {
  if (!id) return '';
  const node = nodes.get(id);
  if (!node) return 'Control';
  if ((node.states || []).includes('protected') || String(node.role).includes('password')) return 'Protected field';
  if (node.vision_fallback) return 'Desktop region';
  const role = normalizeRole(node);
  if (isEditable(node, role)) return 'Text field';
  const fallback = namedTargetRoles.get(role);
  if (!fallback) return 'Control';
  // Only use names of controls normally labelled with static UI copy. Native
  // document titles, editable labels, values and action arguments stay out of the log.
  const name = String(node.name || '').replace(/\s+/g, ' ').trim();
  if (!name || (node.value && name.includes(node.value))) return fallback;
  const characters = Array.from(name);
  return characters.length > 96 ? `${characters.slice(0, 95).join('')}…` : name;
}

function clearActionHighlight() {
  clearTimeout(highlightTimer);
  highlighted?.classList.remove('action-flash', 'action-failed');
  highlighted = null;
}

function highlightAction(id, failed = false) {
  clearActionHighlight();
  highlighted = elements.get(id);
  if (!highlighted) return;
  highlighted.classList.add(failed ? 'action-failed' : 'action-flash');
  highlightTimer = setTimeout(clearActionHighlight, 1400);
}

function addLog(message) {
  const action = actionLabels.get(message.action) || 'Desktop action';
  const target = actionTarget(message.node);
  const failed = message.ok === false;
  const label = failed ? `${action} failed` : action;
  latestAction.hidden = false;
  latestAction.classList.toggle('failed', failed);
  $('latest-action-text').textContent = target ? `${label} · ${target}` : label;

  const li = document.createElement('li');
  if (failed) li.className = 'failed';
  if (actionLabels.has(message.action)) li.dataset.action = message.action;
  const summary = document.createElement('strong');
  summary.textContent = label;
  li.appendChild(summary);
  const time = document.createElement('time');
  const date = message.time ? new Date(message.time) : new Date();
  if (!Number.isNaN(date.getTime())) {
    time.dateTime = date.toISOString();
    time.textContent = date.toLocaleTimeString();
    li.appendChild(time);
  }
  if (target) {
    const description = document.createElement('span');
    description.className = 'activity-target';
    description.textContent = target;
    li.appendChild(description);
  }
  const atEnd = actionLog.scrollHeight - actionLog.scrollTop - actionLog.clientHeight < 12;
  $('activity-empty').hidden = true;
  actionLog.appendChild(li);
  while (actionLog.children.length > 200) actionLog.firstChild.remove();
  if (atEnd) actionLog.scrollTop = actionLog.scrollHeight;
  highlightAction(message.node, failed);
}

function connectRFB(delay = 0) {
  clearTimeout(rfbTimer);
  rfbReady = false;
  if (rfb) {
    const old = rfb;
    rfb = null;
    old.disconnect();
  }
  if (!online() || stopped) return;
  updateConnectedStatus();
  rfbTimer = setTimeout(() => {
    if (!online() || stopped) return;
    $('rfb-screen').replaceChildren();
    try {
      const connection = new RFB($('rfb-screen'), endpoint('/rfb'), { shared: true });
      rfb = connection;
      connection.viewOnly = !isOwner();
      connection.scaleViewport = false;
      connection.resizeSession = false;
      connection.clipViewport = false;
      connection.focusOnClick = isOwner();
      connection.addEventListener('connect', () => {
        if (rfb !== connection) return;
        rfbReady = true;
        scale();
        updateConnectedStatus();
      });
      connection.addEventListener('disconnect', event => {
        if (rfb !== connection) return;
        rfb = null;
        if (online() && !stopped) connectRFB(event.detail.clean ? 180 : 1200);
      });
      connection.addEventListener('securityfailure', event => showError(`VNC: ${event.detail.reason || 'security negotiation failed'}`));
      connection.addEventListener('credentialsrequired', () => showError('VNC requested unexpected credentials. Check the session configuration.'));
      connection.addEventListener('clipboard', event => {
        if (isOwner()) $('clipboard').value = event.detail.text || '';
      });
      connection.addEventListener('desktopname', event => {
        $('rfb-screen').dataset.desktopName = event.detail.name;
      });
    } catch (error) {
      showError(`Could not start noVNC: ${error.message}`);
    }
  }, delay);
}

function connect() {
  clearTimeout(reconnectTimer);
  if (stopped) return;
  setStatus('Connecting to OpticDeck…', 'syncing');
  socket = new WebSocket(endpoint('/ws'));
  socket.addEventListener('open', () => {
    reconnectAttempt = 0;
    updateConnectedStatus();
    connectRFB();
  });
  socket.addEventListener('message', event => {
    try {
      const message = JSON.parse(event.data);
      switch (message.type) {
        case 'snapshot': syncSnapshot(message); break;
        case 'diff': syncDiff(message); break;
        case 'control': setControl(message.control || message); break;
        case 'lease':
          if (isOwner() && typeof message.token === 'string') {
            lease = message.token;
            connectRFB(0);
          }
          break;
        case 'log': addLog(message); break;
        case 'result': {
          const entry = pending.get(message.id);
          if (entry) {
            clearTimeout(entry.timer);
            pending.delete(message.id);
            if (message.ok) entry.resolve(message);
            else entry.reject(new Error(message.error || 'Could not run the action'));
          } else if (message.ok === false) showError(message.error || 'Could not run the action');
          break;
        }
        case 'clipboard': if (isOwner()) $('clipboard').value = message.text || ''; break;
        case 'error': showError(message.error || message.message || 'OpticDeck reported an error'); break;
      }
    } catch (error) {
      showError(`Desktop sync: ${error.message}`);
    }
  });
  socket.addEventListener('close', event => {
    haveSnapshot = false;
    syncPending = false;
    rfbReady = false;
    lease = '';
    overlay.classList.add('disconnected');
    clearActionHighlight();
    $('takeover').disabled = true;
    $('mode').textContent = 'Waiting for session';
    $('mode').classList.remove('manual');
    $('metrics').textContent = 'Waiting for snapshot';
    for (const id of ['clipboard', 'clipboard-read', 'clipboard-write']) $(id).disabled = true;
    $('clipboard').value = '';
    rejectPending();
    textWrites.clear();
    for (const timer of textTimers.values()) clearTimeout(timer);
    textTimers.clear();
    for (const timer of clickTimers.values()) clearTimeout(timer);
    clickTimers.clear();
    dirty.clear();
    if (rfb) { const old = rfb; rfb = null; old.disconnect(); }
    clearTimeout(rfbTimer);
    if (stopped) return;
    setStatus('Disconnected · Retrying', 'offline');
    if (event.code === 1008) showError('Session rejected. Reopen the current session URL in a new tab.');
    reconnectTimer = setTimeout(connect, Math.min(15000, 1000 * 2 ** Math.min(4, reconnectAttempt++)));
  });
  socket.addEventListener('error', () => showError('Could not connect. Check that OpticDeck is running and that you opened the current session URL.'));
}

$('hitboxes').addEventListener('change', event => overlay.classList.toggle('show-hitboxes', event.target.checked));
$('takeover').addEventListener('click', () => {
  if (control.paused && !isOwner()) return;
  // Commit any field edit before pausing. Once takeover is granted no semantic
  // action is allowed, including actions already waiting in this browser.
  const edits = [...new Set([...dirty.keys(), ...textWrites.keys()])].map(flushText);
  run(Promise.all(edits).then(() => request({ type: 'takeover', paused: !isOwner() })));
});
$('clipboard-write').addEventListener('click', () => {
  if (isOwner()) run(request({ type: 'clipboard', text: $('clipboard').value }));
});
$('clipboard-read').addEventListener('click', () => {
  if (isOwner()) run(request({ type: 'clipboard_get' }));
});
window.addEventListener('pagehide', () => {
  stopped = true;
  clearTimeout(reconnectTimer);
  clearTimeout(rfbTimer);
  if (socket) socket.close(1000, 'Tab closed');
  if (rfb) rfb.disconnect();
});
window.addEventListener('pageshow', event => { if (event.persisted) { stopped = false; connect(); } });

scale();
connect();
