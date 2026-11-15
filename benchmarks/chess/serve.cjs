const { createServer } = require('node:http');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');

const rules = process.env.CHESS_MODULE;
if (!rules) {
  console.error('Set CHESS_MODULE to the path of chess.js 1.4.0 dist/esm/chess.js.');
  process.exit(2);
}

const pages = new Map([
  ['/', ['text/html; charset=utf-8', join(__dirname, 'index.html')]],
  ['/chess.css', ['text/css; charset=utf-8', join(__dirname, 'chess.css')]],
  ['/chess.mjs', ['text/javascript; charset=utf-8', join(__dirname, 'chess.mjs')]],
  ['/rules.mjs', ['text/javascript; charset=utf-8', rules]],
].map(([url, [type, file]]) => [url, { type, body: readFileSync(file) }]));

const server = createServer((request, response) => {
  const file = pages.get(request.url);
  if (request.method !== 'GET' || !file) {
    response.writeHead(404).end();
    return;
  }
  response.writeHead(200, {
    'Content-Type': file.type,
    'Content-Security-Policy': "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'",
    'X-Content-Type-Options': 'nosniff',
    'Cache-Control': 'no-store',
  }).end(file.body);
});
server.listen(Number(process.env.PORT || '0'), '127.0.0.1', () => {
  console.log('http://127.0.0.1:' + server.address().port + '/');
});
