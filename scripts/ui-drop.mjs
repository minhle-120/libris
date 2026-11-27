// Drag-and-drop and the unsupported-file path, neither of which is reachable
// through a file picker.
//
// Requires a running server: `make ui`. Standalone:
//   APP=http://127.0.0.1:8080 node scripts/ui-drop.mjs
import { Checks, CRUMB_NOTEBOOK, withBrowser } from './cdp.mjs';

const app = process.env.APP ?? 'http://127.0.0.1:8080/';
// Unique per run: make ui shares one server between both scripts, and a
// leftover notebook is enough to make a "wait for the drop zone" test pass
// against the wrong view.
const notebook = `drop-${Date.now()}`;
const cdpPort = Number(process.env.CDP_PORT ?? 9322);

const checks = new Checks();

await withBrowser({ port: cdpPort, app, profile: '/tmp/libris-ui-profile-drop' }, async (ui) => {
  // Created through the API on purpose. Creating it with the New button and then
  // waiting for `.dropzone` races: the previous notebook's drop zone is already
  // on screen, so the first drop can land in it before the view switches.
  await ui.eval(`
    return await (await fetch('/api/notebooks', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: ${JSON.stringify(notebook)} })
    })).json();
  `);
  await ui.reload(app);
  // Deliberate, not incidental: headless Chrome defaults to 800x600, which is narrow
  // enough that the panel is now closed by the narrow rule (web/src/lib/panel.ts). A
  // harness that inherits a window size is a harness that silently tests one layout.
  await ui.setViewport(1400, 900);

  await ui.eval(`
    const item = [...document.querySelectorAll('.sidebar .list .item')]
      .find((i) => i.textContent.includes(${JSON.stringify(notebook)}));
    if (!item) throw new Error('the notebook is not in the sidebar');
    item.click();
    for (let i = 0; i < 100; i++) {
      if (${CRUMB_NOTEBOOK} === ${JSON.stringify(notebook)}) return true;
      await new Promise((r) => setTimeout(r, 50));
    }
    return false;
  `);
  checks.check('the empty notebook under test is the open one',
    (await ui.crumbs())[1] === notebook, (await ui.crumbs()).join(' / '));

  // ── a real drop of a markdown file ─────────────────────────────
  const dropped = await ui.eval(`
    const zone = document.querySelector('.dropzone');
    const dt = new DataTransfer();
    // 20 × 61 bytes = 1220 B, which must render as 1.2 KB.
    dt.items.add(new File(['Notes on retrieval\\n\\nSome prose that should chunk into words.\\n'.repeat(20)],
      'dropped.md', { type: 'text/markdown' }));

    zone.dispatchEvent(new DragEvent('dragenter', { bubbles: true, dataTransfer: dt }));
    // Svelte applies state changes on a microtask, so read the class after one.
    await new Promise((r) => setTimeout(r, 30));
    const highlighted = zone.classList.contains('dragging');
    const hintShown = !!document.querySelector('.drop-hint');

    zone.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: dt }));
    await new Promise((r) => setTimeout(r, 30));
    const hintGone = !document.querySelector('.drop-hint');

    for (let i = 0; i < 200; i++) {
      const badge = document.querySelector('.source .badge');
      if (badge && badge.textContent.trim() !== 'processing') break;
      await new Promise((r) => setTimeout(r, 100));
    }
    const row = document.querySelector('.source');
    return {
      highlighted, hintShown, hintGone,
      name: row?.querySelector('.name')?.textContent,
      status: row?.querySelector('.badge')?.textContent.trim(),
      facts: row?.querySelector('.facts')?.textContent.replace(/\\s+/g, ' ').trim()
    };
  `);

  checks.check('dragging highlights the drop zone', dropped.highlighted && dropped.hintShown);
  checks.check('dropping clears the overlay', dropped.hintGone);
  checks.check('a dropped file uploads', dropped.name === 'dropped.md', `name=${dropped.name}`);
  checks.check('a dropped text file reaches ready', dropped.status === 'ready', `status=${dropped.status}`);
  checks.check('text reports chunks', /chunks/.test(dropped.facts ?? ''), dropped.facts);

  // ── a drop the app does not handle ─────────────────────────────
  // The bug this section exists for: an upload that worked *and* opened the PDF in
  // the same tab, because the drop event reached the document uncancelled. A
  // cancelable event that ends up defaultPrevented is the browser being told no.
  const inert = await ui.eval(`
    const fire = (type, target) => {
      const dt = new DataTransfer();
      dt.items.add(new File(['%PDF-1.4'], 'ghost.pdf', { type: 'application/pdf' }));
      const ev = new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt });
      target.dispatchEvent(ev);
      return ev.defaultPrevented;
    };
    const before = document.querySelectorAll('.source').length;
    const dropOnBody = fire('drop', document.body);
    const overHeader = fire('dragover', document.querySelector('.header'));
    return { before, after: document.querySelectorAll('.source').length, dropOnBody, overHeader };
  `);
  checks.check('a file drop elsewhere on the page is not a navigation', inert.dropOnBody === true, `prevented=${inert.dropOnBody}`);
  checks.check('dragging a file over the chrome is allowed but inert', inert.overHeader === true, `prevented=${inert.overHeader}`);
  checks.check('a drop outside the panel uploads nothing', inert.after === inert.before, `${inert.before} -> ${inert.after}`);
  checks.check('sizes use binary units', /1\.2 KB/.test(dropped.facts ?? ''), dropped.facts);

  // ── an unsupported file keeps its reason visible ───────────────
  const unsupported = await ui.eval(`
    const zone = document.querySelector('.dropzone');
    const dt = new DataTransfer();
    // A zip: magic bytes nothing here can extract.
    dt.items.add(new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04, 0, 0, 0, 0, 0, 0, 0, 0])],
      'archive.zip', { type: 'application/zip' }));
    zone.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: dt }));

    for (let i = 0; i < 200; i++) {
      const badges = document.querySelectorAll('.source .badge');
      if (badges.length === 2 && badges[0].textContent.trim() !== 'processing') break;
      await new Promise((r) => setTimeout(r, 100));
    }
    const row = document.querySelectorAll('.source')[0];
    return {
      rows: document.querySelectorAll('.source').length, // newest first, so [0] is the zip
      status: row?.querySelector('.badge')?.textContent.trim(),
      detail: row?.querySelector('.note')?.textContent.trim(),
      openable: !!row?.querySelector('.iconlink')
    };
  `);

  checks.check('an unsupported upload is still stored', unsupported.rows === 2, `rows=${unsupported.rows}`);
  checks.check('its status is unsupported', unsupported.status === 'unsupported', `status=${unsupported.status}`);
  checks.check('the reason is visible to the user', !!unsupported.detail, `detail=${unsupported.detail}`);
  checks.check('an unsupported file can still be opened', unsupported.openable === true);
  // ── the inspector, on a file built to show the overlap ─────────
  // dropped.md is 20 identical lines, so its word sequence is periodic and every
  // 100-word window is identical: a degenerate case that would shade whole
  // chunks. Unique words make the 50-word overlap observable.
  await ui.eval(`
    const zone = document.querySelector('.dropzone');
    const dt = new DataTransfer();
    dt.items.add(new File([Array.from({ length: 200 }, (_, i) => 'w' + i).join(' ')],
      'overlap.txt', { type: 'text/plain' }));
    zone.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: dt }));
    for (let i = 0; i < 200; i++) {
      const row = [...document.querySelectorAll('.source')]
        .find((r) => r.querySelector('.name')?.textContent === 'overlap.txt');
      if (row?.querySelector('.badge')?.textContent.trim() === 'ready') return true;
      await new Promise((r) => setTimeout(r, 100));
    }
    return false;
  `);

  // The chunker's 100-word chunks exceed the preview cap, and a collapsed chunk
  // hides the tail the suffix test needs, so expand them first.
  await ui.eval(`
    const row = [...document.querySelectorAll('.source')]
      .find((r) => r.querySelector('.name')?.textContent === 'overlap.txt');
    row.querySelector('button[title="See how this source was chunked"]').click();
    for (let i = 0; i < 60; i++) {
      if (document.querySelector('.viewer .chunk')) break;
      await new Promise((r) => setTimeout(r, 50));
    }
    document.querySelectorAll('.viewer .more').forEach((b) => b.click());
    await new Promise((r) => setTimeout(r, 60));
    return true;
  `);

  const overlap = await ui.until(`
    // No row lookup here, and that is the point: the viewer replaced the list in the
    // panel rather than floating over it, so the source row is not in the document
    // while its chunks are being read.
    const d = document.querySelector('.viewer');
    if (!d || !d.querySelector('.chunk')) return null;

    const texts = [...d.querySelectorAll('.chunk .text')].map((t) => {
      const mark = t.querySelector('mark');
      const head = mark ? mark.textContent : '';
      // Text nodes only: the "… +N chars" control is not part of the chunk.
      const body = [...t.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent).join('');
      return { head, full: head + body };
    });
    if (!texts.length) return null;

    let honest = true;
    for (let i = 1; i < texts.length; i++) {
      if (!texts[i].head) honest = false;
      else if (!texts[i - 1].full.endsWith(texts[i].head) || !texts[i].full.startsWith(texts[i].head)) honest = false;
    }
    const words = (t) => t.trim().split(/\\s+/).filter(Boolean).length;
    return {
      rows: texts.length,
      shaded: texts.map((t) => words(t.head)),
      sizes: texts.map((t) => words(t.full)),
      honest,
      legend: d.querySelector('footer .small')?.textContent.trim()
    };
  `, 200, 100);

  checks.check('a 200-word text becomes three chunks', overlap?.rows === 3, `rows=${overlap?.rows}`);
  checks.check('chunks step by 100 words', JSON.stringify(overlap?.sizes) === JSON.stringify([100, 100, 100]),
    JSON.stringify(overlap?.sizes));
  // The configured overlap is 50 words, so the second and third chunk repeat 50.
  checks.check('the overlap between chunks is marked as 50 words',
    JSON.stringify(overlap?.shaded) === JSON.stringify([0, 50, 50]), JSON.stringify(overlap?.shaded));
  checks.check('the shading is literally the previous chunk tail', overlap?.honest === true,
    () => 'shading does not match the text around it');
  checks.check('the legend explains the shading', /repeats the end of the previous chunk/.test(overlap?.legend ?? ''), overlap?.legend);

  await ui.eval(`document.querySelector('.viewer [aria-label="Back to sources"]').click(); return true;`);

  // ── layout, at the widths that have bitten this repo before ──────
  for (const width of [640, 1180]) {
    await ui.setViewport(width);
    await ui.settle(300);
    // At 640 the narrow rule has closed the panel (web/src/lib/panel.ts), and the source
    // list lives inside it — so open it the way a person at that width would, from the
    // rail. What this loop measures is the panel holding the viewer, a question that only
    // exists while the panel is open.
    await ui.eval(`
      const rail = [...document.querySelectorAll('.sidepanel.closed button')].find((b) => /sources/i.test(b.title));
      if (rail) rail.click();
      return !!rail;
    `);
    await ui.settle(300);
    await ui.eval(`
      const row = [...document.querySelectorAll('.source')]
        .find((r) => r.querySelector('.name')?.textContent === 'overlap.txt');
      row.querySelector('button[title="See how this source was chunked"]').click();
      for (let i = 0; i < 60; i++) {
        if (document.querySelector('.viewer .chunk')) break;
        await new Promise((r) => setTimeout(r, 50));
      }
      return true;
    `);
    // The viewer used to be a centred dialog whose job was to fit the viewport. It is
    // a column now, so the same test asks the narrower question: does the window still
    // hold it, and does anything force the page sideways.
    const fit = await ui.eval(`
      const box = document.querySelector('.sidepanel').getBoundingClientRect();
      const body = document.querySelector('.viewer .body');
      return {
        width: Math.round(box.width),
        fits: box.left >= 0 && box.right <= window.innerWidth + 0.5 && box.height <= window.innerHeight,
        scrollsInside: body ? body.scrollHeight > body.clientHeight : false,
        pageScrollsX: document.documentElement.scrollWidth > window.innerWidth
      };
    `);
    checks.check(`the panel holds the viewer at ${width}px`, fit?.fits && !fit?.pageScrollsX, JSON.stringify(fit));
    await ui.eval(`document.querySelector('.viewer [aria-label="Back to sources"]').click(); return true;`);
    await ui.settle(150);
  }
  await ui.clearViewport();

  checks.check('no uncaught page errors', ui.pageErrors.length === 0, ui.pageErrors.slice(0, 2).join(' | '));
  checks.check('no console errors', ui.consoleErrors.length === 0, ui.consoleErrors.slice(0, 2).join(' | '));
});

process.exit(checks.report());
