const { app, BrowserWindow, Menu } = require('electron');

app.whenReady().then(() => {
  Menu.setApplicationMenu(null);
  const window = new BrowserWindow({
    width: 640,
    height: 420,
    title: 'OpticDeck benchmark · Electron',
    webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false },
  });
  const html = `<!doctype html>
    <html lang="en">
      <meta charset="UTF-8">
      <meta http-equiv="Content-Security-Policy"
        content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'">
      <title>OpticDeck benchmark · Electron</title>
      <style>
        body { margin: 28px; font: 16px sans-serif; }
        label, button, output { display: block; margin-block: 14px; }
        #project { display: block; margin-block: 6px; width: min(25rem, 90%); }
      </style>
      <h1>Electron benchmark</h1>
      <form>
        <label>Project name <input id="project" value="Original project"></label>
        <label><input id="draft" type="checkbox"> Include draft</label>
        <button>Apply changes</button>
        <output>Pending</output>
      </form>
      <script>
        document.querySelector('form').addEventListener('submit', event => {
          event.preventDefault();
          const project = document.querySelector('#project').value;
          const suffix = document.querySelector('#draft').checked ? 'draft included' : 'draft excluded';
          document.querySelector('output').value = 'Saved: ' + project + ' (' + suffix + ')';
        });
      </script>
    </html>`;
  return window.loadURL(`data:text/html;charset=utf-8,${encodeURIComponent(html)}`);
}).catch(() => app.exit(1));
app.on('window-all-closed', () => app.quit());
