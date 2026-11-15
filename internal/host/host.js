import RFB from '/novnc/core/rfb.js';
import { mountContactSheet } from '/contact-sheet.js';

const display = document.getElementById('display');
const viewer = document.getElementById('viewer');
const status = document.getElementById('status');
const share = document.getElementById('share');
const control = document.getElementById('control');
const mode = document.getElementById('mode');
const permissionNote = document.getElementById('permission-note');
const controlNote = document.getElementById('control-note');
const stop = document.getElementById('stop');
const fit = document.getElementById('fit');
const fullscreen = document.getElementById('fullscreen');
let client;
let fitDisplay = true;
const contactSheet = mountContactSheet(() => display.querySelector('canvas'));

const sessionOptions = new URLSearchParams(location.hash.slice(1));
const inputToken = sessionOptions.get('input');
const isX11 = sessionOptions.get('backend') === 'x11' || history.state?.backend === 'x11';
const readyText = isX11 ? 'Select Share display, then accept the connection on the X11 desktop.'
  : 'Choose a monitor in the system dialog after selecting Share display.';
status.textContent = readyText;
control.hidden = !inputToken;
controlNote.hidden = !inputToken;
fullscreen.hidden = !document.fullscreenEnabled;
if (isX11) {
  permissionNote.textContent = 'X11 shares the entire display. Keyboard, mouse and clipboard are disabled in read-only mode.';
  controlNote.textContent = 'Sharing with input sends keys and pointer events to the focused desktop. Clipboard sharing stays off.';
}
history.replaceState(isX11 ? { backend: 'x11' } : null, '', location.pathname);

function shareDisplay(withInput) {
  const target = new URL('/rfb?client=host-view', location.href);
  target.protocol = 'ws:';
  if (withInput) target.searchParams.set('lease', inputToken);
  const connection = new RFB(display, target.href, { shared: true });
  client = connection;
  connection.viewOnly = !withInput;
  connection.scaleViewport = fitDisplay;
  share.disabled = true;
  control.disabled = true;
  stop.disabled = false;
  mode.textContent = withInput ? 'Waiting for permission' : 'Read only';
  permissionNote.hidden = withInput;
  status.textContent = isX11 ? 'Waiting for you to accept on the X11 desktop…' : 'Waiting for you to choose a monitor in the system dialog…';
  connection.addEventListener('connect', () => {
    contactSheet.setAvailable(true);
    if (withInput) mode.textContent = 'Input enabled';
    status.textContent = withInput
      ? 'Keyboard and pointer input are active. Select Stop viewing to disconnect.'
      : 'Display sharing is active. Select Stop viewing to disconnect.';
  });
  connection.addEventListener('disconnect', event => {
    contactSheet.setAvailable(false);
    client = undefined;
    share.disabled = false;
    control.disabled = false;
    stop.disabled = true;
    mode.textContent = 'Read only';
    permissionNote.hidden = false;
    status.textContent = event.detail.clean ? 'Display sharing ended. Select Share display to start again.'
      : 'Display sharing could not connect. Check the desktop prompt and try again.';
  });
}

share.addEventListener('click', () => shareDisplay(false));
control.addEventListener('click', () => shareDisplay(true));

stop.addEventListener('click', () => {
  contactSheet.setAvailable(false);
  client?.disconnect();
});
window.addEventListener('pagehide', () => client?.disconnect());

fit.addEventListener('click', () => {
  fitDisplay = !fitDisplay;
  fit.setAttribute('aria-pressed', String(fitDisplay));
  if (client) client.scaleViewport = fitDisplay;
});

fullscreen.addEventListener('click', async () => {
  try {
    if (document.fullscreenElement === viewer) await document.exitFullscreen();
    else await viewer.requestFullscreen();
  } catch {
    status.textContent = 'Fullscreen is unavailable here.';
  }
});

document.addEventListener('fullscreenchange', () => {
  const active = document.fullscreenElement === viewer;
  fullscreen.setAttribute('aria-pressed', String(active));
  fullscreen.textContent = active ? 'Exit fullscreen' : 'Fullscreen';
});
