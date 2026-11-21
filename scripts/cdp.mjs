// Minimal Chrome DevTools Protocol driver for the browser checks in this
// directory. No dependencies: Node's built-in fetch, WebSocket and spawn.
//
// These checks drive the real UI and assert on DOM state rather than
// screenshots — a picture of a page that scrolled wrong looks almost right.
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const CANDIDATES = [
  process.env.CHROME,
  '/opt/google/chrome/chrome',
  '/usr/bin/google-chrome-stable',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser'
].filter(Boolean);

export function findChrome() {
  return CANDIDATES.find((path) => existsSync(path));
}

/** Page-side expression: the breadcrumb's second label, i.e. the open notebook. */
export const CRUMB_NOTEBOOK =
  `[...document.querySelectorAll('.crumbs .crumb')][1]?.textContent?.trim() ?? ''`;

export class Checks {
  constructor() {
    this.results = [];
  }

  /**
   * `detail` may be a function, in which case it is only called on a failure —
   * otherwise a passing check prints its own error message.
   */
  check(name, ok, detail = '') {
    this.results.push({ name, ok });
    const shown = typeof detail === 'function' ? (ok ? '' : detail()) : detail;
    console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${shown ? '  — ' + shown : ''}`);
    return this;
  }

  report() {
    const failed = this.results.filter((r) => !r.ok);
    console.log(`\n${this.results.length - failed.length}/${this.results.length} checks passed`);
    return failed.length;
  }
}

/**
 * Launches headless Chrome, opens the page, and hands back a driver. The
 * browser is always killed, including when the body throws.
 */
export async function withBrowser({ port, app, profile }, body) {
  const chrome = findChrome();
  if (!chrome) {
    console.log('SKIP: no Chrome binary found (set CHROME=/path/to/chrome)');
    return 0;
  }

  const proc = spawn(chrome, [
    '--headless=new',
    `--remote-debugging-port=${port}`,
    '--no-first-run',
    '--no-default-browser-check',
    `--user-data-dir=${profile}`,
    '--disable-gpu',
    'about:blank'
  ], { stdio: 'ignore' });

  try {
    return await connect(port, app, body);
  } finally {
    proc.kill();
  }
}

async function connect(port, app, body) {
  for (let i = 0; i < 150; i++) {
    try {
      if ((await fetch(`http://127.0.0.1:${port}/json/version`)).ok) break;
    } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }

  // Newer Chrome only accepts PUT on /json/new.
  const target = await (
    await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' })
  ).json();

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('ws failed')); });

  const ui = new UI(ws);
  try {
    await ui.send('Page.enable');
    await ui.send('Runtime.enable');
    await ui.send('DOM.enable');
    await ui.send('Page.navigate', { url: app });
    await ui.settle();
    return await body(ui);
  } finally {
    ws.close();
  }
}

const KEY_CODES = { Escape: 27, Enter: 13, Tab: 9, Backspace: 8 };

class UI {
  constructor(ws) {
    this.ws = ws;
    this.nextId = 1;
    this.pending = new Map();
    this.pageErrors = [];
    this.consoleErrors = [];

    ws.onmessage = (e) => {
      let msg;
      try {
        msg = JSON.parse(e.data);
      } catch {
        return; // a malformed frame is not worth tearing the run down for
      }
      if (msg.id && this.pending.has(msg.id)) {
        const { res, rej } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (msg.error) rej(new Error(JSON.stringify(msg.error)));
        else res(msg.result);
        return;
      }
      if (msg.method === 'Runtime.exceptionThrown') {
        this.pageErrors.push(
          msg.params.exceptionDetails.exception?.description ?? msg.params.exceptionDetails.text
        );
      } else if (msg.method === 'Runtime.consoleAPICalled'
        && ['error', 'warning'].includes(msg.params.type)) {
        this.consoleErrors.push(
          msg.params.args.map((a) => a.value ?? a.description ?? a.type).join(' ')
        );
      }
    };
  }

  send(method, params = {}) {
    const id = this.nextId++;
    return new Promise((res, rej) => {
      this.pending.set(id, { res, rej });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }

  /** Runs an async function body in the page and returns its JSON value. */
  async eval(body) {
    const r = await this.send('Runtime.evaluate', {
      expression: `(async () => { ${body} })()`,
      awaitPromise: true,
      returnByValue: true
    });
    if (r.exceptionDetails) {
      throw new Error(r.exceptionDetails.exception?.description ?? JSON.stringify(r.exceptionDetails));
    }
    return r.result.value;
  }

  /**
   * Gives a file input a real file from disk. This is the honest way to test an
   * upload: the browser performs the same multipart encoding it would for a
   * person clicking Choose files.
   */
  async setFiles(selector, files) {
    const found = await this.send('Runtime.evaluate', {
      expression: `document.querySelector(${JSON.stringify(selector)})`
    });
    const objectId = found.result?.objectId; // the handle is nested under `result`
    if (!objectId) throw new Error(`no element matches ${selector}`);
    await this.send('DOM.setFileInputFiles', { files, objectId });
  }

  /** Waits for the initial load and Svelte's first render. */
  async settle(ms = 800) {
    await new Promise((r) => setTimeout(r, ms));
  }

  /** Reloads the app, so server-side changes appear in a client-side list. */
  async reload(url) {
    await this.send('Page.navigate', { url });
    await this.settle();
  }

  /** Pretends to be a narrower window. This is for layout checks, not behaviour. */
  async setViewport(width, height = 800) {
    await this.send('Emulation.setDeviceMetricsOverride', {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: false
    });
    await this.settle(200);
  }

  async clearViewport() {
    await this.send('Emulation.clearDeviceMetricsOverride');
  }

  /**
   * A real key press. Synthetic KeyboardEvents cannot close a <dialog>: the
   * browser only honours Escape for trusted input, which is exactly the
   * behaviour a test must not fake away.
   */
  async press(key, code = '') {
    const base = { type: 'keyDown', key, windowsVirtualKeyCode: KEY_CODES[key] ?? 0, code };
    await this.send('Input.dispatchKeyEvent', base);
    await this.send('Input.dispatchKeyEvent', { ...base, type: 'keyUp' });
  }

  /** Clicks a header/sidebar button by its visible label. */
  click(text) {
    return this.eval(`
      const want = ${JSON.stringify(text)};
      const btn = [...document.querySelectorAll('button')]
        .find((b) => b.textContent.trim() === want || b.title === want);
      if (!btn) throw new Error('no button labelled ' + want);
      btn.click();
      return true;
    `);
  }

  /**
   * The breadcrumb's labels, left to right. The notebook's name is the second one,
   * and the open chunk viewer's source is the last — which is where the app puts it,
   * so a test that asks the header what is open asks the same thing a user does.
   */
  crumbs() {
    return this.eval(
      `return [...document.querySelectorAll('.crumbs .crumb')].map((c) => c.textContent.trim());`
    );
  }

  /**
   * Polls in the page until the code returns something truthy, then returns it.
   * Like `eval`, the argument is a function body and must `return` — passing a
   * bare expression used to fail as a syntax error inside the page.
   */
  until(code, tries = 120, every = 50) {
    return this.eval(`
      const step = () => {
        ${code}
      };
      for (let i = 0; i < ${tries}; i++) {
        const value = step();
        if (value) return value;
        await new Promise((r) => setTimeout(r, ${every}));
      }
      return step();
    `);
  }
}

// A two-page PDF, checked in so the checks have something real to upload:
// poppler reads it without an xref table, and it is 702 bytes.
export const UPLOAD_FIXTURE =
  process.env.UPLOAD_FIXTURE ?? join(dirname(fileURLToPath(import.meta.url)), 'testdata', 'two-page.pdf');
