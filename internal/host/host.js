import RFB from '/novnc/core/rfb.js';

const display = document.getElementById('display');
const status = document.getElementById('status');
const share = document.getElementById('share');
const control = document.getElementById('control');
const mode = document.getElementById('mode');
const permissionNote = document.getElementById('permission-note');
const controlNote = document.getElementById('control-note');
const stop = document.getElementById('stop');
let client;

const inputToken = new URLSearchParams(location.hash.slice(1)).get('input');
control.hidden = !inputToken;
controlNote.hidden = !inputToken;
history.replaceState(null, '', location.pathname);

function shareDisplay(withInput) {
  const target = new URL('/rfb?client=host-view', location.href);
  target.protocol = 'ws:';
  if (withInput) target.searchParams.set('lease', inputToken);
  const connection = new RFB(display, target.href, { shared: true });
  client = connection;
  connection.viewOnly = !withInput;
  connection.scaleViewport = true;
  share.disabled = true;
  control.disabled = true;
  stop.disabled = false;
  mode.textContent = withInput ? 'Input enabled' : 'Read only';
  permissionNote.hidden = withInput;
  status.textContent = 'Waiting for you to choose a monitor in the system dialog…';
  connection.addEventListener('connect', () => {
    status.textContent = withInput
      ? 'Keyboard and pointer input are active. Select Stop viewing to disconnect.'
      : 'Showing the selected monitor. Press Ctrl+C in the terminal to end the session.';
  });
  connection.addEventListener('disconnect', () => {
    client = undefined;
    share.disabled = false;
    control.disabled = false;
    stop.disabled = true;
    mode.textContent = 'Read only';
    permissionNote.hidden = false;
    status.textContent = 'Display sharing ended. Select Share display to start again.';
  });
}

share.addEventListener('click', () => shareDisplay(false));
control.addEventListener('click', () => shareDisplay(true));

stop.addEventListener('click', () => client?.disconnect());
