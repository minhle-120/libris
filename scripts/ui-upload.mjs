// Upload path through the real UI: create a notebook on the server, upload with
// a real file input, watch status, dedupe, filter, detach.
//
// Requires a running server: `make ui` starts one against a throwaway data
// directory. Standalone: APP=http://127.0.0.1:8080 node scripts/ui-upload.mjs
import { Checks, withBrowser, UPLOAD_FIXTURE } from './cdp.mjs';

const app = process.env.APP ?? 'http://127.0.0.1:8080/';
const cdpPort = Number(process.env.CDP_PORT ?? 9321);

const checks = new Checks();

const code = await withBrowser({ port: cdpPort, app, profile: '/tmp/libris-ui-profile' }, async (ui) => {
  const boot = await ui.eval(`
    return {
      brand: document.querySelector('.brand .name')?.textContent,
      notebooks: document.querySelectorAll('.sidebar .list .item').length
    };
  `);
  checks.check('app shell renders from the backend', boot.brand === 'libris', `brand=${boot.brand}`);

  // ── create a notebook ──────────────────────────────────────────
  const before = boot.notebooks;
  await ui.click('New');
  const created = await ui.until(
    `document.querySelectorAll('.sidebar .list .item').length > ${before}
       ? document.querySelectorAll('.sidebar .list .item').length : null`
  );
  checks.check('New creates a notebook on the server', created === before + 1, `rows=${created}`);

  const opened = await ui.eval(`
    return {
      title: document.querySelector('.title h1')?.textContent,
      addEnabled: [...document.querySelectorAll('button')]
        .some((b) => b.textContent.includes('Add sources') && !b.disabled),
      empty: document.querySelector('.empty h2')?.textContent
    };
  `);
  checks.check('notebook view opens', opened.title === 'Untitled notebook', `title=${opened.title}`);
  checks.check('Add sources is a working control', opened.addEnabled === true);
  checks.check('empty state invites an upload', /Nothing in this notebook yet/.test(opened.empty ?? ''), opened.empty);

  // ── upload a real PDF ──────────────────────────────────────────
  await ui.setFiles('input[type=file]', [UPLOAD_FIXTURE]);

  // `until` takes an expression (it is evaluated up to twice per poll).
  const row = await ui.until(`
    document.querySelector('.source') && {
      name: document.querySelector('.source .name')?.textContent,
      status: document.querySelector('.source .badge')?.textContent.trim()
    }
  `);
  checks.check('the 202 response shows a row at once', !!row && row.name === 'two-page.pdf', JSON.stringify(row));
  checks.check('the row starts as processing', row?.status === 'processing', `status=${row?.status}`);

  const ready = await ui.until(`
    document.querySelector('.source .badge')?.textContent.trim() !== 'processing' &&
      document.querySelector('.source') && {
      status: document.querySelector('.source .badge')?.textContent.trim(),
      facts: document.querySelector('.source .facts')?.textContent.replace(/\\s+/g, ' ').trim(),
      count: document.querySelector('.sidebar .count')?.textContent.trim(),
      open: document.querySelector('.iconlink')?.getAttribute('href')
    }
  `, 200, 100);

  checks.check('polling reaches ready', ready?.status === 'ready', `status=${ready?.status}`);
  checks.check('a PDF reports its chunks', /3 chunks/.test(ready?.facts ?? ''), ready?.facts);
  checks.check('the original is a binary size, in binary units', /702 B/.test(ready?.facts ?? ''), ready?.facts);
  checks.check('sidebar count refreshed after upload', ready?.count === '1', `count=${ready?.count}`);
  checks.check(
    'open link targets the kept original',
    /^\/api\/sources\/[0-9a-f]{64}\/original$/.test(ready?.open ?? ''),
    ready?.open
  );

  // ── the same bytes again must not create a second row ──────────
  await ui.setFiles('input[type=file]', [UPLOAD_FIXTURE]);
  await ui.settle(1500);
  const dedup = await ui.eval(`
    return { rows: document.querySelectorAll('.source').length,
             statuses: [...document.querySelectorAll('.source .badge')].map((b) => b.textContent.trim()) };
  `);
  checks.check('identical bytes are deduped, not duplicated', dedup.rows === 1, JSON.stringify(dedup));

  // ── filter ─────────────────────────────────────────────────────
  const filtered = await ui.eval(`
    const input = document.querySelector('.filter input');
    const type = (value) => {
      input.value = value;
      input.dispatchEvent(new Event('input', { bubbles: true }));
    };
    type('no-such-file');
    await new Promise((r) => setTimeout(r, 100));
    const misses = document.querySelectorAll('.source').length;
    const message = document.querySelector('.center')?.textContent.trim();
    type('two-page');
    await new Promise((r) => setTimeout(r, 100));
    return { misses, message, hits: document.querySelectorAll('.source').length };
  `);
  checks.check('filter hides non-matching sources', filtered.misses === 0 && /No source matches/.test(filtered.message ?? ''), filtered.message);
  checks.check('filter shows matching sources', filtered.hits === 1, `hits=${filtered.hits}`);

  // ── detach ─────────────────────────────────────────────────────
  await ui.eval(`document.querySelector('.source .row-actions button').click(); return true;`);
  const removed = await ui.until(`
    document.querySelectorAll('.source').length === 0 && {
      empty: document.querySelector('.empty h2')?.textContent,
      count: document.querySelector('.sidebar .count')?.textContent ?? null
    };
  `);
  checks.check('remove detaches the source', !!removed, () => 'rows never reached 0');
  checks.check('empty state returns', /Nothing in this notebook yet/.test(removed?.empty ?? ''), removed?.empty);
  checks.check('sidebar count cleared', removed?.count === null, `count=${removed?.count}`);

  // ── settings still work in the same session ────────────────────
  const settings = await ui.eval(`
    [...document.querySelectorAll('header .right button')].at(-2).click();
    for (let i = 0; i < 60; i++) {
      if (document.querySelector('.panels .card')) break;
      await new Promise((r) => setTimeout(r, 50));
    }
    return { groups: document.querySelectorAll('.subnav .nav-item').length,
             inputs: document.querySelectorAll('.panels input').length };
  `);
  checks.check('settings still render from the backend', settings.groups >= 3 && settings.inputs > 10, JSON.stringify(settings));

  checks.check('no uncaught page errors', ui.pageErrors.length === 0, ui.pageErrors.slice(0, 2).join(' | '));
  checks.check('no console errors', ui.consoleErrors.length === 0, ui.consoleErrors.slice(0, 2).join(' | '));

  return ui.pageErrors.length + ui.consoleErrors.length === 0 ? 0 : 1;
});

process.exit(checks.report() || code);
