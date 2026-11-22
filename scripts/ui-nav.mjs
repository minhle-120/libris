// The shell: sidebar chrome only, nothing uploaded or asked. Create-with-a-name,
// rename, resize, collapse, and what survives a reload.
//
// Requires a running server: `make ui` starts one against a throwaway data
// directory. Standalone: APP=http://127.0.0.1:8080 node scripts/ui-nav.mjs
import { Checks, withBrowser } from './cdp.mjs';

const app = process.env.APP ?? 'http://127.0.0.1:8080/';
const cdpPort = Number(process.env.CDP_PORT ?? 9324);
const checks = new Checks();

/** The sidebar's measured width, as the browser laid it out. */
const WIDTH = `Math.round(document.querySelector('.sidebar').getBoundingClientRect().width)`;

/** Type into a field the way a user does, so Svelte's bind:value sees it. */
const type = (selector, value) => `
  const el = document.querySelector(${JSON.stringify(selector)});
  if (!el) throw new Error('no element ' + ${JSON.stringify(selector)});
  el.value = ${JSON.stringify(value)};
  el.dispatchEvent(new Event('input', { bubbles: true }));
  return el.value;
`;

const code = await withBrowser(
  { port: cdpPort, app, profile: '/tmp/libris-nav-profile' },
  async (ui) => {
    // A previous run of this file leaves a width and a collapsed flag behind in the
    // profile, and half these checks are about what the defaults are.
    await ui.eval(`localStorage.clear(); return true`);
    await ui.reload(app);
    await ui.settle();

    const boot = await ui.eval(`
      return {
        status: document.querySelector('.sidebar')?.textContent.includes('llama.cpp'),
        width: ${WIDTH}
      };
    `);
    checks.check('no model-server status in the sidebar', boot.status === false, `llama=${boot.status}`);
    checks.check('sidebar opens at the default width', boot.width === 288, `width=${boot.width}`);

    // ── create with a name ───────────────────────────────────────
    // The sidebar's + — the header's New button is gone, so this is the only one.
    await ui.click('New notebook');
    const dialog = await ui.eval(`
      const d = document.querySelector('dialog.new-notebook');
      return {
        open: d?.open === true,
        focused: document.activeElement === d?.querySelector('input'),
        createEnabled: [...d.querySelectorAll('button')].some((b) => b.textContent.trim() === 'Create' && !b.disabled)
      };
    `);
    checks.check('New opens a named-notebook dialog', dialog.open, `open=${dialog.open}`);
    checks.check('the name field is focused', dialog.focused, `focused=${dialog.focused}`);
    checks.check('Create is disabled while the name is blank', dialog.createEnabled === false);

    await ui.eval(type('dialog.new-notebook input', 'Nav Notes'));
    await ui.click('Create');
    const created = await ui.until(`
      const row = [...document.querySelectorAll('.sidebar .list .item')]
        .find((b) => b.textContent.includes('Nav Notes'));
      const crumb = [...document.querySelectorAll('.crumbs .crumb')][1]?.textContent;
      return row && crumb === 'Nav Notes' ? { row: true, crumb } : null;
    `);
    checks.check(
      'the typed name is the notebook name',
      created?.crumb === 'Nav Notes',
      JSON.stringify(created)
    );
    checks.check('the dialog closed', await ui.eval(`return document.querySelector('dialog.new-notebook')?.open !== true`));

    // ── rename ───────────────────────────────────────────────────
    await ui.eval(`
      const btn = [...document.querySelectorAll('.rename-open')]
        .find((b) => b.getAttribute('aria-label') === 'Rename Nav Notes');
      if (!btn) throw new Error('no rename button for Nav Notes');
      btn.click();
      return true;
    `);
    const editing = await ui.eval(`
      const el = document.querySelector('.sidebar input.rename');
      return { editing: !!el, prefilled: el?.value, selected: el === document.activeElement };
    `);
    checks.check('rename edits the name in place', editing.editing && editing.prefilled === 'Nav Notes', JSON.stringify(editing));
    checks.check('the current name is selected for typing over', editing.selected, `focused=${editing.selected}`);

    await ui.eval(type('.sidebar input.rename', 'Renamed Notes'));
    await ui.eval(`
      const el = document.querySelector('.sidebar input.rename');
      el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
      return true;
    `);
    const committed = await ui.until(`
      const row = [...document.querySelectorAll('.sidebar .list .item')]
        .find((b) => b.textContent.includes('Renamed Notes'));
      const crumb = [...document.querySelectorAll('.crumbs .crumb')][1]?.textContent;
      return row && crumb === 'Renamed Notes'
        ? { row: true, editing: !!document.querySelector('.sidebar input.rename'), crumb }
        : null;
    `);
    checks.check(
      'Enter commits the new name',
      committed?.editing === false && committed?.crumb === 'Renamed Notes',
      JSON.stringify(committed)
    );

    // Escape must cancel without a request: the row still says what it said.
    await ui.eval(`
      const first = [...document.querySelectorAll('.rename-open')]
        .find((b) => b.getAttribute('aria-label') === 'Rename Renamed Notes');
      first.click();
      return true;
    `);
    await ui.eval(`
      const el = document.querySelector('.sidebar input.rename');
      if (!el) throw new Error('Escape test found no rename field open');
      el.value = 'thrown away';
      el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      return true;
    `);
    await ui.settle(200);
    checks.check(
      'Escape abandons the rename',
      (await ui.eval(`return [...document.querySelectorAll('.sidebar .list .item')].some((b) => b.textContent.includes('Renamed Notes'))`)) === true
    );

    // The rename is a server write, so a reload has to agree with the list.
    const rows = await fetch(`${app}api/notebooks`).then((r) => r.json());
    checks.check(
      'the rename reached the server',
      rows.some((nb) => nb.name === 'Renamed Notes') && !rows.some((nb) => nb.name === 'Nav Notes'),
      rows.map((nb) => nb.name).join(',')
    );

    // ── delete ───────────────────────────────────────────────────
    // A second notebook exists only to be destroyed, and it is opened first: the case
    // worth testing is deleting the notebook that is on screen, because that is the one
    // where the app has to decide what to show next.
    await ui.click('New notebook');
    await ui.eval(type('dialog.new-notebook input', 'Doomed Book'));
    await ui.click('Create');
    await ui.until(`
      return [...document.querySelectorAll('.sidebar .list .item')]
        .some((b) => b.textContent.includes('Doomed Book')) ? true : null;
    `);

    const doomed = await ui.eval(`
      const btn = [...document.querySelectorAll('.delete-open')]
        .find((b) => b.getAttribute('aria-label') === 'Delete Doomed Book');
      if (!btn) return { found: false };
      btn.click();
      return { found: true };
    `);
    checks.check('a row offers delete beside rename', doomed.found);

    const confirm = await ui.eval(`
      const d = document.querySelector('dialog.delete-notebook');
      return {
        open: d?.open === true,
        names: /Doomed Book/.test(d?.querySelector('h2')?.textContent ?? ''),
        keepsFiles: /stay on disk/.test(d?.textContent ?? ''),
        focused: document.activeElement?.textContent.trim(),
        danger: [...(d?.querySelectorAll('button') ?? [])]
          .some((b) => b.textContent.trim() === 'Delete notebook' && b.className.includes('btn-danger'))
      };
    `);
    checks.check('delete asks before it does anything', confirm.open && confirm.names, JSON.stringify(confirm));
    checks.check(
      'the confirmation says the files survive',
      confirm.keepsFiles,
      'that promise is the whole reason a user dares press this'
    );
    checks.check(
      'the destructive button is red and Cancel has the focus',
      confirm.danger && confirm.focused === 'Cancel',
      JSON.stringify(confirm)
    );

    // Esc is a no-op, not a half-delete: the row and the server must still agree.
    // It has to be a real key event — closing a <dialog> on Esc is a default action of
    // a *trusted* key press, so the synthetic KeyboardEvent the rename tests use would
    // report a dialog that never closed.
    for (const type of ['keyDown', 'keyUp']) {
      await ui.send('Input.dispatchKeyEvent', {
        type,
        key: 'Escape',
        code: 'Escape',
        windowsVirtualKeyCode: 27,
        nativeVirtualKeyCode: 27
      });
    }
    await ui.settle(250);
    const survived = await fetch(`${app}api/notebooks`).then((r) => r.json());
    checks.check(
      'Escape closes the confirmation',
      await ui.eval(`return document.querySelector('dialog.delete-notebook')?.open !== true`)
    );
    checks.check(
      'and the notebook is still there',
      survived.some((nb) => nb.name === 'Doomed Book'),
      survived.map((nb) => nb.name).join(',')
    );

    // Delete the one being looked at, so the selection has to move somewhere.
    await ui.eval(`
      [...document.querySelectorAll('.delete-open')]
        .find((b) => b.getAttribute('aria-label') === 'Delete Doomed Book').click();
      return true;
    `);
    await ui.click('Delete notebook');
    await ui.until(`
      return [...document.querySelectorAll('.sidebar .list .item')]
        .some((b) => b.textContent.includes('Doomed Book')) ? null : true;
    `);
    const gone = (await fetch(`${app}api/notebooks`).then((r) => r.json())).map((nb) => nb.name);
    const after = await ui.eval(`
      return {
        listed: [...document.querySelectorAll('.sidebar .list .item')].map((b) => b.textContent.trim()),
        active: document.querySelector('.sidebar .row.active')?.textContent.trim(),
        crumb: [...document.querySelectorAll('.crumbs .crumb')][1]?.textContent
      };
    `);
    checks.check(
      'confirming deletes it on the server',
      !gone.includes('Doomed Book') &&
        !(await ui.eval(`return [...document.querySelectorAll('.sidebar .list .item')].some((b) => b.textContent.includes('Doomed Book'))`)),
      gone.join(',')
    );
    checks.check(
      'another notebook is open in its place',
      /Renamed Notes/.test(after.active ?? '') && after.crumb === 'Renamed Notes',
      JSON.stringify(after)
    );

    // A deleted notebook is gone from the rail too: the collapsed sidebar draws the same
    // list, and a stale id there would be a button for nothing.
    await ui.eval(`document.querySelector('.sidebar .section-head .rail-toggle').click(); return true`);
    await ui.settle(300);
    checks.check(
      'the rail forgets it as well',
      (await ui.eval(`return [...document.querySelectorAll('.rail-item')].map((b) => b.title).join(',')`)).includes('Doomed Book') === false
    );
    await ui.eval(`document.querySelector('.sidebar .rail-toggle').click(); return true`);
    await ui.settle(300);

    // ── resize ───────────────────────────────────────────────────
    // Width is measured in a call of its own: Svelte has not flushed the DOM when
    // the dispatching eval returns, so the same round trip that reads the state is
    // what makes the measurement mean the event it follows.
    const width = () => ui.eval(`return ${WIDTH}`);

    const stored = await ui.eval(`
      const el = document.querySelector('.resizer');
      const r = el.getBoundingClientRect();
      const at = (x) => ({ bubbles: true, cancelable: true, clientX: x, clientY: r.top + 60,
                           pointerId: 1, isPrimary: true, button: 0, buttons: 1 });
      el.dispatchEvent(new PointerEvent('pointerdown', at(r.left + 3)));
      window.dispatchEvent(new PointerEvent('pointermove', at(r.left + 83)));
      window.dispatchEvent(new PointerEvent('pointerup', at(r.left + 83)));
      return localStorage.getItem('libris.sidebar.width');
    `);
    const dragged = await width();
    checks.check('dragging the edge widens the sidebar', dragged === 368, `width=${dragged}`);
    checks.check('the width is stored', stored === '368', `saved=${stored}`);

    await ui.eval(`
      const el = document.querySelector('.resizer');
      el.focus();
      el.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
      return true;
    `);
    const keyed = await width();
    checks.check('arrow keys resize without a mouse', keyed === 352, `width=${keyed}`);

    await ui.eval(`
      document.querySelector('.resizer').dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
      return true;
    `);
    const reset = await width();
    checks.check('double-click resets the width', reset === 288, `width=${reset}`);

    // ── collapse and reopen ──────────────────────────────────────
    await ui.eval(`
      const el = document.querySelector('.resizer');
      const r = el.getBoundingClientRect();
      const at = (x) => ({ bubbles: true, cancelable: true, clientX: x, clientY: r.top + 60,
                           pointerId: 1, isPrimary: true, button: 0, buttons: 1 });
      el.dispatchEvent(new PointerEvent('pointerdown', at(r.left + 3)));
      window.dispatchEvent(new PointerEvent('pointermove', at(r.left - 77)));
      window.dispatchEvent(new PointerEvent('pointerup', at(r.left - 77)));
      return true;
    `);
    await width();
    await ui.click('Hide notebooks');
    const closed = await ui.until(`
      return document.querySelector('.sidebar .rail') ? {
        width: ${WIDTH},
        items: document.querySelectorAll('.sidebar .rail-item').length,
        titled: [...document.querySelectorAll('.sidebar .rail-item')]
          .some((b) => b.getAttribute('title') === 'Renamed Notes')
      } : null;
    `);
    checks.check('collapse leaves a rail', closed?.width === 44 && closed?.items >= 1, JSON.stringify(closed));
    checks.check('a rail icon still names its notebook', closed?.titled === true, `named=${closed?.titled}`);

    await ui.click('Show notebooks');
    const reopened = await ui.until(`
      return document.querySelector('.sidebar .list .item') ? ${WIDTH} : null;
    `);
    checks.check('reopening restores the width it collapsed at', reopened === 208, `width=${reopened}`);

    await ui.reload(app);
    await ui.settle();
    const remembered = await ui.eval(`
      return { width: ${WIDTH}, open: !!document.querySelector('.sidebar .list .item') };
    `);
    checks.check(
      'the width and the open state survive a reload',
      remembered.width === 208 && remembered.open,
      JSON.stringify(remembered)
    );

    await ui.click('Hide notebooks');
    await ui.reload(app);
    await ui.settle();
    checks.check(
      'a collapsed sidebar stays collapsed across a reload',
      (await ui.eval(`return { width: ${WIDTH}, rail: !!document.querySelector('.sidebar .rail') }`)).rail === true
    );
    await ui.eval(`localStorage.clear(); return true`);

    // ── breadcrumb ───────────────────────────────────────────────
    const crumbTexts = `
      return [...document.querySelectorAll('.crumbs .crumb')].map((c) => c.textContent.trim());
    `;

    await ui.click('Show notebooks');
    await ui.eval(`
      [...document.querySelectorAll('.sidebar .list .item')]
        .find((b) => b.textContent.includes('Renamed Notes'))?.click();
      return true;
    `);
    await ui.settle(200);

    const crumbs0 = await ui.eval(crumbTexts);
    checks.check(
      'breadcrumb reads libris / notebook / panel',
      crumbs0.join(' / ') === 'libris / Renamed Notes / Sources',
      crumbs0.join(' / ')
    );
    checks.check(
      'the header has no New button',
      (await ui.eval(`return [...document.querySelectorAll('.header button')].some((b) => b.textContent.trim() === 'New')`)) === false
    );

    // ── the panel: Sources and Activity share one place ──────────
    const segment = (label) => ui.eval(`
      const b = [...document.querySelectorAll('.sidepanel .seg-btn')]
        .find((x) => x.textContent.trim().startsWith(${JSON.stringify(label)}));
      if (!b) throw new Error('no ' + ${JSON.stringify(label)} + ' segment');
      b.click();
      return true;
    `);
    const crumbs = () => ui.eval(`
      return [...document.querySelectorAll('.crumbs .crumb')].map((x) => x.textContent.trim());
    `);

    await segment('Activity');
    checks.check(
      'Activity is a view inside the panel, and the breadcrumb follows',
      (await ui.until(`const c = [...document.querySelectorAll('.crumbs .crumb')].map((x) => x.textContent.trim());
        return c.join('/') === 'libris/Renamed Notes/Activity' ? true : null;`)) === true
    );
    checks.check(
      'Activity says what is missing rather than showing a stub',
      (await ui.eval(`return document.querySelector('.sidepanel .empty h2')?.textContent`)) === 'Nothing recorded yet'
    );

    await segment('Sources');
    checks.check(
      'Sources comes back with its list',
      (await ui.until(`
        const c = [...document.querySelectorAll('.crumbs .crumb')].map((x) => x.textContent.trim());
        return c.join('/') === 'libris/Renamed Notes/Sources' && document.querySelector('.sidepanel .dropzone')
          ? true
          : null;
      `)) === true
    );

    // The panel is the second resizable column, so it drags, stores and shrinks the
    // breadcrumb exactly the way the notebook list on the left does.
    const panelWidth = () => ui.eval(`return Math.round(document.querySelector('.sidepanel').getBoundingClientRect().width)`);

    const saved = await ui.eval(`
      const el = document.querySelector('.sidepanel .resizer');
      const r = el.getBoundingClientRect();
      const at = (x) => ({ bubbles: true, cancelable: true, clientX: x, clientY: r.top + 60,
                           pointerId: 1, isPrimary: true, button: 0, buttons: 1 });
      el.dispatchEvent(new PointerEvent('pointerdown', at(r.left + 3)));
      // Left is wider on this side: the handle is the panel's inner edge, which is
      // the whole point of mirroring the left-hand column rather than copying it.
      window.dispatchEvent(new PointerEvent('pointermove', at(r.left - 77)));
      window.dispatchEvent(new PointerEvent('pointerup', at(r.left - 77)));
      return localStorage.getItem('libris.sidepanel.width');
    `);
    const widened = await panelWidth();
    checks.check('the panel drags wider from its inner edge', widened === 420, `width=${widened}`);
    checks.check('and remembers it', saved === '420', `saved=${saved}`);

    await ui.click('Hide panel');
    const closedPanel = await ui.until(`
      return document.querySelector('.sidepanel.closed')
        ? { width: Math.round(document.querySelector('.sidepanel').getBoundingClientRect().width) }
        : null;
    `);
    const shutCrumbs = await crumbs();
    checks.check(
      'a collapsed panel leaves a rail and shortens the breadcrumb',
      closedPanel?.width === 44 && shutCrumbs.join('/') === 'libris/Renamed Notes',
      `${closedPanel?.width} · ${shutCrumbs.join(' / ')}`
    );

    await ui.click('Show sources');
    const reopenedPanel = await ui.until(
      `return document.querySelector('.sidepanel .seg') ? Math.round(document.querySelector('.sidepanel').getBoundingClientRect().width) : null`
    );
    checks.check('the panel opens back at the width it closed at', reopenedPanel === 420, `width=${reopenedPanel}`);

    // ── what the breadcrumb deliberately does not name ───────────
    // The chat is the page, and a breadcrumb names where you are by climbing out of.
    // A chat has a title and a picker of its own; putting it in the header would put
    // the ground you stand on in the list of places you could go back to.
    const notebook = (await fetch(`${app}api/notebooks`).then((r) => r.json())).find((n) => n.name === 'Renamed Notes');
    const madeChat = await fetch(`${app}api/notebooks/${notebook.id}/chats`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ title: 'A named chat' })
    }).then((r) => r.json());

    await ui.reload(app);
    await ui.settle();
    await segment('Activity');
    await segment('Sources');
    await ui.settle(200);
    checks.check(
      'the open chat is named where it is switched',
      ['A named chat'].includes(
        await ui.eval(`
          const picker = document.querySelector('.chat select.picker');
          return picker
            ? picker.options[picker.selectedIndex]?.textContent?.trim()
            : document.querySelector('.chat .thread .title')?.textContent?.trim();
        `)
      ),
      JSON.stringify(madeChat)
    );
    checks.check(
      'and nowhere in the breadcrumb',
      (await crumbs()).join('/') === 'libris/Renamed Notes/Sources',
      (await crumbs()).join(' / ')
    );

    await ui.eval(`
      [...document.querySelectorAll('.crumbs .crumb.link')]
        .find((b) => b.textContent === 'libris')?.click();
      return true;
    `);
    const home = await ui.until(`
      return document.querySelector('.landing')
        ? [...document.querySelectorAll('.crumbs .crumb')].map((x) => x.textContent.trim())
        : null;
    `);
    checks.check('the libris crumb opens the notebook list', (home ?? []).join('/') === 'libris', JSON.stringify(home));

    // The bug this file was written about: the landing button is a different control
    // from the sidebar's + and the header's New, and it was wired to the wrong thing.
    await ui.eval(`document.querySelector('.landing button')?.click(); return true;`);
    checks.check(
      'the landing New notebook opens the dialog',
      (await ui.eval(`return document.querySelector('dialog.new-notebook')?.open === true`)) === true
    );
    checks.check(
      'a refetch does not re-pick a notebook the user navigated away from',
      (await ui.eval(`return !!document.querySelector('.landing')`)) === true
    );

    // ── the reading measure ──────────────────────────────────────
    // Last, because it resizes the window. The measure only binds when the pane is
    // wider than it, and the pane is widest with the panel shut — the case that used to
    // stretch the composer across a metre of empty field while the answer above it
    // wrapped six inches short.
    await ui.eval(`document.querySelector('dialog.new-notebook')?.close(); return true`);
    await ui.eval(`[...document.querySelectorAll('.sidebar .list .item')][0]?.click(); return true`);
    await ui.settle(500);
    await ui.setViewport(1800, 900);
    await ui.settle(400);
    await ui.eval(`
      [...document.querySelectorAll('.rail-toggle')].find((b) => b.title === 'Hide panel')?.click();
      return true;
    `);
    await ui.settle(400);
    const measure = await ui.eval(`
      const w = (s) => Math.round(document.querySelector(s).getBoundingClientRect().width);
      return { pane: w('.chat'), bar: w('.chat .bar'), composer: w('.composer .entry') };
    `);
    checks.check(
      "the composer is the thread's width, not the pane's",
      measure.bar > 0 && measure.bar === measure.composer && measure.pane - measure.bar >= 100,
      JSON.stringify(measure)
    );

    checks.check('no uncaught page errors', ui.pageErrors.length === 0, ui.pageErrors.slice(0, 2).join(' | '));
    checks.check('no console errors', ui.consoleErrors.length === 0, ui.consoleErrors.slice(0, 2).join(' | '));

    return ui.pageErrors.length + ui.consoleErrors.length === 0 ? 0 : 1;
  }
);

process.exit(checks.report() || code);
