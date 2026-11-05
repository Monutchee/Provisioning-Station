// Exercise the real renderer and security policy without serial hardware.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from 'playwright';

const directory = await mkdtemp(join(tmpdir(), 'station-terminal-test-'));
const server = spawn(process.env.STATION_BINARY || '/tmp/mnc-station-browser', ['serve', '--http-listen=127.0.0.1:0', '--tftp-listen=127.0.0.1:0', `--data-dir=${directory}`], { env: { ...process.env, MNC_STATION_API_TOKEN: '' } });
let browser;
try {
  const url = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('Server did not start')), 10000);
    let output = '';
    const read = (data) => {
      output += data;
      const match = output.match(/Dashboard:\s+(http:\/\/\S+)/);
      if (match) { clearTimeout(timeout); resolve(match[1]); }
    };
    server.stdout.on('data', read);
    server.stderr.on('data', read);
    server.once('exit', (code) => { clearTimeout(timeout); reject(new Error(`Server exited ${code}: ${output}`)); });
  });
  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE, args: ['--no-sandbox'] });
  const page = await browser.newPage();
  const errors = [];
  const warnings = [];
  const external = [];
  browser.on('page', (tab) => tab.on('request', (request) => {
    if (/^https?:/.test(request.url()) && new URL(request.url()).origin !== new URL(url).origin) external.push(request.url());
  }));
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => { if (message.type() === 'warning') warnings.push(message.text()); });
  // Keep the real response and CSP but avoid unrelated application polling.
  await page.route('**/app.js', (route) => route.fulfill({ contentType: 'text/javascript', body: '' }));
  const response = await page.goto(url);
  assert.match(response.headers()['content-security-policy'], /script-src 'self'/);
  await page.evaluate(async () => {
    const { hterm } = await import('/vendor/hterm/hterm.js');
    window.testingHterm = hterm;
    const Original = hterm.Terminal;
    hterm.Terminal = class extends Original { constructor(...args) { super(...args); window.actualTerminal = this; } };
    const { createTerminal } = await import('/terminal.js');
    document.body.replaceChildren();
    const host = document.createElement('div');
    host.style.cssText = 'position:relative;width:800px;height:320px';
    document.body.append(host);
    window.input = [];
    window.adapter = await createTerminal(host, (text) => input.push(text));
    window.host = host;
    window.rows = () => [...actualTerminal.scrollbackRows_, ...actualTerminal.screen_.rowsArray].map((row) => row.textContent).join('\n');
  });
  await page.evaluate(() => {
    adapter.write('a\nb');
    if (actualTerminal.getCursorColumn() !== 2) throw new Error('LF unexpectedly became CRLF');
    adapter.write('\x1b[31mred\x1b[0m');
    if (!actualTerminal.screen_.rowsArray.some((row) => row.querySelector('span[style]'))) throw new Error('ANSI styling missing');
    adapter.reset();
    adapter.write(new Uint8Array([0xe7, 0x95]));
    adapter.write(new Uint8Array([0x8c, 0xf0, 0x9f]));
    adapter.write(new Uint8Array([0x99, 0x82]));
    if (!rows().includes('界🙂')) throw new Error('Split UTF-8 was corrupted');
    adapter.write('\x1b[5n');
    if (!input.includes('\x1b[0n')) throw new Error('Terminal response missing');
    actualTerminal.io.onVTKeystroke('keyboard');
    actualTerminal.io.sendString('paste-λ');
    if (!input.includes('keyboard') || !input.includes('paste-λ')) throw new Error('Input callbacks missing');
    adapter.reset();
    adapter.write('a'.repeat(16383) + '🙂z');
    if (!rows().replaceAll('\n', '').includes('🙂z')) throw new Error('UTF-16 chunk boundary corrupted');
    adapter.write(new Uint8Array([0xe7]));
    adapter.reset();
    adapter.write(new Uint8Array([0x61]));
    if (!rows().startsWith('a') || rows().includes('\ufffd')) throw new Error('Decoder did not reset');
    adapter.reset();
    adapter.write(Array.from({ length: 10300 }, (_, i) => `row-${i}\r\n`).join(''));
    if (actualTerminal.scrollbackRows_.length !== 10000) throw new Error('History limit not rolling');
    if (rows().includes('row-0\n') || !rows().includes('row-10299')) throw new Error('History retained wrong rows');
    [...actualTerminal.scrollbackRows_, ...actualTerminal.primaryScreen_.rowsArray].forEach((row, index) => {
      if (row.rowIndex !== index) throw new Error(`Wrong row index ${index}: ${row.rowIndex}`);
    });
  });
  await page.waitForTimeout(100);
  await page.evaluate(() => actualTerminal.scrollPort_.scrollRowToTop(500));
  await page.waitForTimeout(100);
  await page.evaluate(() => {
    const selectedRow = actualTerminal.scrollbackRows_[500];
    const selection = actualTerminal.scrollPort_.document_.getSelection();
    const range = actualTerminal.scrollPort_.document_.createRange();
    range.selectNodeContents(selectedRow);
    selection.removeAllRanges();
    selection.addRange(range);
    const selectedText = selection.toString();
    if (!selectedText.startsWith('row-')) throw new Error('Selection did not capture terminal text');
    const before = actualTerminal.scrollPort_.getTopRowIndex();
    adapter.write('extra\r\n');
    if (actualTerminal.scrollPort_.getTopRowIndex() !== before - 1) throw new Error(`Scrolled viewport did not follow retained rows: ${before} -> ${actualTerminal.scrollPort_.getTopRowIndex()}, end=${actualTerminal.scrollPort_.isScrolledEnd}`);
    if (selection.toString() !== selectedText) throw new Error('Rolling trim changed retained selection');
    adapter.reset();
    if (actualTerminal.scrollbackRows_.length || rows().includes('row-')) throw new Error('Reset left history');
    window.columns = actualTerminal.screenSize.width;
    host.style.width = '480px';
  });
  await page.waitForTimeout(100);
  await page.evaluate(() => {
    if (actualTerminal.screenSize.width >= columns) throw new Error('Resize did not fit');
    window.sideEffects = [];
    window.open = () => sideEffects.push('open');
    testingHterm.copySelectionToClipboard = () => sideEffects.push('clipboard');
    testingHterm.Terminal.prototype.copyStringToClipboard = () => sideEffects.push('clipboard-string');
    window.Notification = class { constructor() { sideEffects.push('notification'); } };

  });
  await page.evaluate(() => {
    const title = document.title;
    adapter.write('\x1b]52;c;Y2xpcGJvYXJk\x07\x1b]9;notify\x07\x1b]0;changed\x07\x1b]1337;File=inline=1:AAAA\x07\x1b]8;;https://example.com\x07link\x1b]8;;\x07');
    adapter.write('\x1b%@\u009b31m');
    if (document.title !== title || sideEffects.length) throw new Error('Remote output caused side effects');
    if (actualTerminal.scrollPort_.getDocument().querySelector('a[href], img')) throw new Error('Remote output created links or images');
    if (!actualTerminal.vt.codingSystemLocked_ || !actualTerminal.vt.codingSystemUtf8_) throw new Error('Output changed locked UTF8 encoding');
    if (actualTerminal.vt.enable8BitControl) throw new Error('8-bit controls enabled');
    for (const start of ['\x1b]', '\x1bP', '\x1b^', '\x1b_', '\x1b[']) {
      adapter.reset();
      adapter.write(start + '1'.repeat(40000));
      adapter.write(start === '\x1b[' ? 'mrecovered' : '\x1b\\recovered');
      if (!rows().includes('recovered') || rows().includes('1111111111')) throw new Error('Oversized control failed recovery');
    }
    adapter.write('\x1b]4;999;red\x07\x1b]private\x07\x1b[999 q');
    adapter.dispose();
    adapter.write('late');
    if (host.childElementCount) throw new Error('Dispose left DOM');
  });
  // Run the unchanged application UART protocol through synthetic API/WS peers.
  const app = await browser.newPage();
  app.on('pageerror', (error) => errors.push(error.message));
  app.on('console', (message) => { if (message.type() === 'warning') warnings.push(message.text()); });
  let observerMode = false;
  let stream;
  const sent = [];
  const port = { id: 'synthetic-port', name: 'synthetic UART' };
  await app.route('**/app.js', async (route) => {
    const original = await route.fetch();
    const capture = `import { hterm } from './vendor/hterm/hterm.js';
      const Original = hterm.Terminal;
      hterm.Terminal = class extends Original { constructor(...args) { super(...args); window.auditTerminal = this; } };\n`;
    await route.fulfill({ response: original, body: capture + await original.text() });
  });
  await app.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    let payload = {};
    let status = 200;
    if (path.endsWith('/capabilities')) payload = { version: 'browser-test', xsdb: { available: false } };
    if (path.endsWith('/ports')) payload = { ports: [port] };
    if (path.endsWith('/jobs')) payload = { jobs: [{ id: 'synthetic-job', state: 'succeeded', createdUtc: '2026-01-01T00:00:00Z', request: {}, serialCapture: {} }] };
    if (path.endsWith('/sessions') && route.request().method() === 'POST') {
      const request = route.request().postDataJSON();
      if (observerMode && request.access === 'controller') {
        status = 409; payload = { error: { message: 'already controlled' } };
      } else payload = { id: 'synthetic-session', websocketPath: '/synthetic-stream', attachToken: 'synthetic-token', port };
    }
    if (path.endsWith('/serial-transcript')) {
      await route.fulfill({ contentType: 'application/octet-stream', body: Buffer.from('transcript-λ界🙂\r\n') });
      return;
    }
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(payload) });
  });
  await app.routeWebSocket('**/synthetic-stream', (socket) => {
    stream = socket;
    socket.onMessage((message) => {
      if (typeof message === 'string') {
        assert.deepEqual(JSON.parse(message), { type: 'attach', token: 'synthetic-token' });
        socket.send(JSON.stringify({ type: 'ready', access: observerMode ? 'observer' : 'controller', baudRate: 115200 }));
      } else sent.push(Buffer.from(message).toString());
    });
  });
  await app.goto(url);
  await app.waitForFunction(() => window.auditTerminal?.io?.previousIO_ && !document.getElementById('console-connect').disabled);
  await app.locator('#console-connect').click();
  await app.waitForFunction(() => document.getElementById('console-status').textContent.includes('controller'));
  await app.evaluate(() => {
    auditTerminal.io.onVTKeystroke('key');
    auditTerminal.io.sendString('paste-λ');
  });
  stream.send(Buffer.from('\x1b[5n'));
  await app.waitForTimeout(100);
  assert.deepEqual(sent, ['key', 'paste-λ', '\x1b[0n']);
  await app.locator('#console-disconnect').click();
  observerMode = true;
  await app.locator('#console-connect').click();
  await app.waitForFunction(() => document.getElementById('console-status').textContent.includes('read-only'));
  await app.evaluate(() => { auditTerminal.io.onVTKeystroke('blocked'); auditTerminal.io.sendString('blocked-paste'); });
  stream.send(Buffer.from('\x1b[5nobserver-output'));
  await app.waitForTimeout(100);
  assert.equal(sent.length, 3, 'Observer transmitted input or automatic replies');
  await app.locator('#console-transcript').click();
  await app.waitForFunction(() => document.getElementById('console-status').textContent.startsWith('Transcript'));
  const replay = await app.evaluate(() => [...auditTerminal.scrollbackRows_, ...auditTerminal.screen_.rowsArray].map((row) => row.textContent).join('\n'));
  assert.match(replay, /transcript-λ界🙂/);
  assert.doesNotMatch(replay, /observer-output|Monutchee serial console/);
  assert.deepEqual(errors, []);
  assert.deepEqual(warnings, []);
  assert.deepEqual(external, []);
  console.log('Browser terminal checks passed (real embedded server CSP, Unicode, ANSI, input/observer guards, rolling history/selection, transcript reset, resize, hostile controls, disposal).');
} finally {
  await browser?.close();
  server.kill('SIGTERM');
  await rm(directory, { recursive: true, force: true });
}
