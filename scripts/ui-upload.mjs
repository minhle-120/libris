// Upload path through the real UI: create a notebook on the server, upload with
// a real file input, watch status, dedupe, filter, detach.
//
// Requires a running server: `make ui` starts one against a throwaway data
// directory. Standalone: APP=http://127.0.0.1:8080 node scripts/ui-upload.mjs
import { writeFileSync } from 'node:fs';
import { Checks, withBrowser, UPLOAD_FIXTURE } from './cdp.mjs';

// The fake embeddings server `make ui` starts alongside the app; /control makes
// it refuse, which is how a source ends up in the state the retry button is for.
const embed = process.env.EMBED;

/** What the fake embedding server saw, so a click can be measured at the socket. */
const stats = async () => {
  if (!embed) return { requests: 0, lastPrompt: null };
  return (await fetch(`${embed}/stats`)).json();
};

/** The Node-side twin of `ui.until`: poll something that is not in the page. */
async function poll(tries, every, step) {
  for (let i = 0; i < tries; i++) {
    const value = await step();
    if (value) return value;
    await new Promise((r) => setTimeout(r, every));
  }
  return null;
}
const control = (down) =>
  fetch(`${embed}/control`, {
    method: 'POST',
    body: JSON.stringify({ down }),
    headers: { 'content-type': 'application/json' }
  });

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
  await ui.eval(`
    const el = document.querySelector('dialog.new-notebook input');
    el.value = 'Upload Lab';
    el.dispatchEvent(new Event('input', { bubbles: true }));
    return true;
  `);
  await ui.click('Create');
  const created = await ui.until(
    `return document.querySelectorAll('.sidebar .list .item').length > ${before}
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
  checks.check('notebook view opens', opened.title === 'Upload Lab', `title=${opened.title}`);
  checks.check('Add sources is a working control', opened.addEnabled === true);
  checks.check('empty state invites an upload', /Nothing in this notebook yet/.test(opened.empty ?? ''), opened.empty);

  // ── upload a real PDF ──────────────────────────────────────────
  await ui.setFiles('input[type=file]', [UPLOAD_FIXTURE]);

  // `until` takes an expression (it is evaluated up to twice per poll).
  const row = await ui.until(`
    return document.querySelector('.source') && {
      name: document.querySelector('.source .name')?.textContent,
      status: document.querySelector('.source .badge')?.textContent.trim()
    }
  `);
  checks.check('the 202 response shows a row at once', !!row && row.name === 'two-page.pdf', JSON.stringify(row));
  checks.check('the row starts as processing', row?.status === 'processing', `status=${row?.status}`);

  const ready = await ui.until(`
    return document.querySelector('.source .badge')?.textContent.trim() !== 'processing' &&
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

  // ── the chunk inspector ────────────────────────────────────────
  await ui.click('See how this source was chunked');
  const view = await ui.until(`
    const d = document.querySelector('dialog');
    // Truthiness must mean "rendered", not merely "open": the first poll lands
    // while listSegments is still in flight and would read the loading text.
    if (!d || !d.open || !d.querySelector('.chunk, .tile')) return null;
    return {
      heading: d.querySelector('h2')?.textContent,
      summary: d.querySelector('header p')?.textContent.replace(/\\s+/g, ' ').trim(),
      docs: [...d.querySelectorAll('.doc-head h3')].map((h) => h.textContent),
      ordinals: [...d.querySelectorAll('.ord')].map((o) => o.textContent),
      prov: [...d.querySelectorAll('.prov span:first-child')].map((p) => p.textContent),
      text: d.querySelector('.text')?.textContent.trim().slice(0, 24),
      legend: d.querySelector('footer .small')?.textContent.trim()
    };
  `);
  checks.check('the inspector opens on the source name', /two-page.pdf/.test(view?.heading ?? ''), view?.heading);
  checks.check('it counts the chunks by kind',
    /3 chunks/.test(view?.summary ?? '') && /1 text/.test(view?.summary ?? '') && /2 images/.test(view?.summary ?? ''),
    view?.summary);
  checks.check('a PDF is shown as its two derived documents',
    JSON.stringify(view?.docs) === JSON.stringify(['page text', 'page rasters']), JSON.stringify(view?.docs));
  // Only the text document uses .ord; the raster tiles show their ordinal in the
  // caption, which the raster checks below assert on.
  checks.check('a text chunk carries its ordinal id',
    JSON.stringify(view?.ordinals) === JSON.stringify(['0000']), JSON.stringify(view?.ordinals));
  // The text chunk straddles a page break, which is what page_end is for.
  checks.check('provenance shows the page span', /p1[^0-9]2/.test((view?.prov ?? [])[0] ?? ''), JSON.stringify(view?.prov));
  checks.check('the text chunk shows its passage', /Page one says the sky/.test(view?.text ?? ''), view?.text);
  checks.check('a lone chunk reports no overlap', /no chunk repeats/.test(view?.legend ?? ''), view?.legend);

  const imgs = await ui.until(`
    const list = [...document.querySelectorAll('dialog img')];
    return list.length === 2 && list.every((i) => i.complete && i.naturalWidth > 0) && {
      sizes: list.map((i) => i.naturalWidth + '\u00d7' + i.naturalHeight).join(' '),
      caps: [...document.querySelectorAll('dialog .cap')].map((c) => c.textContent.replace(/\\s+/g, ' ').trim())
    }
  `, 100, 100);
  checks.check('page rasters are served and rendered', imgs?.sizes === '459\u00d7459 459\u00d7459', imgs?.sizes);
  checks.check('a raster is labelled with its own page',
    /0000 p1 459/.test(imgs?.caps?.[0] ?? '') && /0001 p2 459/.test(imgs?.caps?.[1] ?? ''),
    JSON.stringify(imgs?.caps));

  // A real Escape press: a synthetic KeyboardEvent is untrusted, and Chrome only
  // closes a <dialog> on trusted input.
  await ui.press('Escape');
  await ui.settle(300);
  const closed = await ui.eval(`
    const open = [...document.querySelectorAll('dialog')].filter((d) => d.open);
    return { total: document.querySelectorAll('dialog').length,
             open: open.map((d) => d.getAttribute('aria-label')) };
  `);
  // Counting dialogs would now include the new-notebook dialog, which lives in the
  // shell whether or not it is open: what matters is that none of them is.
  checks.check('the dialog closes', closed.open.length === 0, JSON.stringify(closed));

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
  // The remove button is found by title, not position: the inspect button now
  // sits in the same group, and a positional selector silently clicks that.
  await ui.eval(`
    document
      .querySelector('.source .row-actions button[title^="Remove"]')
      .click();
    return true;
  `);
  const removed = await ui.until(`
    return document.querySelectorAll('.source').length === 0 && {
      empty: document.querySelector('.empty h2')?.textContent,
      count: document.querySelector('.sidebar .count')?.textContent ?? null
    };
  `);
  checks.check('remove detaches the source', !!removed, () => 'rows never reached 0');
  checks.check('empty state returns', /Nothing in this notebook yet/.test(removed?.empty ?? ''), removed?.empty);
  checks.check('sidebar count cleared', removed?.count === null, `count=${removed?.count}`);

  // ── the embedding server goes away, and comes back ─────────────
  // `ready` means searchable, so a source uploaded while the embedding server is
  // down must not claim to be ready — and must be recoverable without the user
  // finding the file again. This is the whole point of ADR 0004, seen from the UI.
  if (embed) {
    // The filter section leaves a search applied, which would hide this row.
    // Leaving app state dirty between sections is how a later check ends up
    // asserting on an empty list.
    await ui.eval(`
      const input = document.querySelector('.filter input');
      input.value = '';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      return true;
    `);

    await control(true);
    const textFile = '/tmp/libris-retry-fixture.txt';
    writeFileSync(textFile, `retry probe ${Date.now()}\n` + 'unique sentence ninety-one.\n');

    await ui.setFiles('input[type=file]', [textFile]);
    const broken = await ui.until(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.textContent.includes('libris-retry-fixture'));
      if (!row) return null;
      const note = row.querySelector('.note');
      if (!/failed/.test(row.querySelector('.badge')?.textContent ?? '') && !note) return null;
      return {
        badge: row.querySelector('.badge')?.textContent,
        note: note?.textContent ?? '',
        again: !!row.querySelector('button[title^="Run this file"]'),
        // The chunks survive, so the inspector stays useful even when embedding did not.
        chunks: !!row.querySelector('button[title^="See how"]')
      };
    `, 200);
    checks.check(
      'a source with no embedding server reports failed, never ready',
      /failed/.test(broken?.badge ?? ''),
      () => `badge=${broken?.badge}`
    );
    checks.check(
      'the reason names the server to fix',
      /8124|embed/i.test(broken?.note ?? ''),
      broken?.note
    );
    checks.check(
      'a failed source offers to be rebuilt',
      broken?.again === true,
      () => JSON.stringify(broken)
    );
    checks.check('a failed source can still be inspected', broken?.chunks === true);

    await control(false);
    await ui.eval(`
      const btn = document.querySelector('button[title^="Run this file"]');
      if (!btn) throw new Error('no rebuild button on the failed row');
      btn.click();
      return true;
    `);
    const healed = await ui.until(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.textContent.includes('libris-retry-fixture'));
      // trim(): Svelte renders {source.status} with surrounding whitespace, so
      // the text node is " ready". Every other check in this file trims too.
      const badge = row?.querySelector('.badge')?.textContent.trim();
      return badge === 'ready' ? { badge } : null;
    `);
    checks.check('retry takes it to ready once the server returns', healed?.badge === 'ready', `badge=${healed?.badge}`);

    // ── rebuilding a source that already works ───────────────────
    // It is the same button. A corpus has to be re-runnable on purpose, because the
    // embedding model, its pooling, the prompt scheme or the chunker can all change
    // underneath it. Clicking must visibly redo the work and bring the row back: a
    // button that quietly no-ops is worse than not having one.
    const offered = await ui.eval(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.textContent.includes('libris-retry-fixture'));
      return {
        on: !!row?.querySelector('button[title^="Run this file"]'),
        // The row's whole fact line: kind, chunk count, size, age. Rebuilding must
        // leave it identical, which is what "as if it was just uploaded, and the
        // same source afterwards" means from the UI's side.
        facts: row?.querySelector('.facts')?.textContent.replace(/\s+/g, ' ').trim() ?? null
      };
    `);
    checks.check('a ready source offers the same rebuild', offered.on);
    const before = await stats();
    await ui.eval(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.textContent.includes('libris-retry-fixture'));
      row.querySelector('button[title^="Run this file"]').click();
      return true;
    `);
    // Wait on the socket, not the badge. The row is already `ready` when the rebuild
    // is clicked, so a poll that returns as soon as it sees `ready` returns before the
    // pipeline has run — and then reports a working rebuild as one that asked the
    // embedding server nothing.
    const after =
      (await poll(150, 100, async () => {
        const s = await stats();
        return s.requests > before.requests ? s : null;
      })) ?? (await stats());

    const again = await ui.until(`
      const row = [...document.querySelectorAll('.source')].find((r) =>
        r.textContent.includes('libris-retry-fixture'));
      const badge = row?.querySelector('.badge')?.textContent.trim();
      return badge === 'ready'
        ? { badge, facts: row?.querySelector('.facts')?.textContent.replace(/\s+/g, ' ').trim() ?? null }
        : null;
    `);
    checks.check(
      'a rebuild actually asked the embedding server again',
      after.requests > before.requests,
      () => `requests ${before.requests} → ${after.requests}`
    );
    checks.check('rebuilding brings the row back to ready', again?.badge === 'ready', () => `badge=${again?.badge}`);
    checks.check(
      'the rebuilt row is the same source, not a new one',
      !!again?.facts && again.facts === offered.facts,
      () => `${offered.facts} → ${again.facts}`
    );
    checks.check(
      'the new vectors are embedded the way the pipeline embeds',
      /title: /.test(after.lastPrompt ?? ''),
      () => `first input=${after.lastPrompt}`
    );
  } else {
    console.log('  skip EMBED is not set, so the retry path is not exercised');
  }

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
