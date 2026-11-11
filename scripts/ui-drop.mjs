// Drag-and-drop and the unsupported-file path, neither of which is reachable
// through a file picker.
//
// Requires a running server: `make ui`. Standalone:
//   APP=http://127.0.0.1:8080 node scripts/ui-drop.mjs
import { Checks, withBrowser } from './cdp.mjs';

const app = process.env.APP ?? 'http://127.0.0.1:8080/';
const cdpPort = Number(process.env.CDP_PORT ?? 9322);

const checks = new Checks();

await withBrowser({ port: cdpPort, app, profile: '/tmp/libris-ui-profile-drop' }, async (ui) => {
  await ui.click('New');
  await ui.until(`document.querySelector('.dropzone') && true`);

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
  checks.check('no uncaught page errors', ui.pageErrors.length === 0, ui.pageErrors.slice(0, 2).join(' | '));
  checks.check('no console errors', ui.consoleErrors.length === 0, ui.consoleErrors.slice(0, 2).join(' | '));
});

process.exit(checks.report());
