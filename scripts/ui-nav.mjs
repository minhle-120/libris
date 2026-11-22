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
        files: /no other notebook uses goes with it/.test(d?.textContent ?? ''),
        focused: document.activeElement?.textContent.trim(),
        danger: [...(d?.querySelectorAll('button') ?? [])]
          .some((b) => b.textContent.trim() === 'Delete notebook' && b.className.includes('btn-danger'))
      };
    `);
    checks.check('delete asks before it does anything', confirm.open && confirm.names, JSON.stringify(confirm));
    checks.check(
      'the confirmation says which files go and which stay',
      confirm.files,
      'the promise changed: a shared file stays, an unshared one goes, and the box must say both'
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
      'breadcrumb reads libris / notebook while no chat is open',
      crumbs0.join(' / ') === 'libris / Renamed Notes',
      crumbs0.join(' / ')
    );
    // Sources is open on the right and the header says nothing about it: a view you
    // open beside the chat is not a place you are, and the segment control already
    // says which one is showing.
    checks.check(
      'the open panel is not a breadcrumb level',
      crumbs0.length === 2 &&
        (await ui.eval(`
          return document.querySelector('.sidepanel .seg-btn[aria-selected="true"]')
            ?.textContent.trim();
        `))?.startsWith('Sources'),
      JSON.stringify(crumbs0)
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

    const tab = () => ui.eval(`
      return document.querySelector('.sidepanel .seg-btn[aria-selected="true"]')
        ?.textContent.trim().replace(/\\s+\\d+$/, '');
    `);

    await segment('Activity');
    checks.check(
      'Activity is a view inside the panel, and the breadcrumb ignores it',
      (await ui.until(`return document.querySelector('.sidepanel .empty h2')?.textContent === 'Nothing recorded yet'
        ? true : null;`)) === true && (await tab()) === 'Activity' && (await crumbs()).join(' / ') === 'libris / Renamed Notes',
      `${await tab()} · ${(await crumbs()).join(' / ')}`
    );
    checks.check(
      'Activity says what is missing rather than showing a stub',
      (await ui.eval(`return document.querySelector('.sidepanel .empty h2')?.textContent`)) === 'Nothing recorded yet'
    );

    await segment('Sources');
    checks.check(
      'Sources comes back with its list',
      (await ui.until(`return document.querySelector('.sidepanel .dropzone') && document.querySelector('.sidepanel .seg-btn[aria-selected="true"]')?.textContent.trim().startsWith('Sources')
        ? true
        : null;`)) === true
    );

    // The panel is the second resizable column, so it drags and stores the width
    // exactly the way the notebook list on the left does.
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
      'a collapsed panel leaves a rail and leaves the breadcrumb alone',
      closedPanel?.width === 44 && shutCrumbs.join(' / ') === 'libris / Renamed Notes',
      `${closedPanel?.width} · ${shutCrumbs.join(' / ')}`
    );

    await ui.click('Show sources');
    const reopenedPanel = await ui.until(
      `return document.querySelector('.sidepanel .seg') ? Math.round(document.querySelector('.sidepanel').getBoundingClientRect().width) : null`
    );
    checks.check('the panel opens back at the width it closed at', reopenedPanel === 420, `width=${reopenedPanel}`);

    // ── the notebook list is a tree: the notebook, then its conversations ──
    // A conversation lives where the breadcrumb says it does, so the two are the same
    // fact twice rather than a copy that can drift. Chats are made over the API here
    // because asking is ui-ask's job; the server titles a chat from its first question,
    // which is the rule the titles below are standing in for.
    const chatRows = () =>
      ui.eval(`return [...document.querySelectorAll('.sidebar .chat-open')].map((b) => b.textContent.trim());`);
    const openRow = () =>
      ui.eval(`return document.querySelector('.sidebar .chat-row.active .chat-open')?.textContent.trim() ?? '';`);
    const crumbTail = () =>
      ui.eval(`return [...document.querySelectorAll('.crumbs .crumb')].map((x) => x.textContent.trim());`);

    const notebook = (await fetch(`${app}api/notebooks`).then((r) => r.json())).find((n) => n.name === 'Renamed Notes');

    checks.check(
      'a conversation is born from a question, not from a button',
      (await chatRows()).length === 0 &&
        (await ui.eval(`return !!document.querySelector('.sidebar .chat-new')`)) === false,
      JSON.stringify(await chatRows())
    );

    await fetch(`${app}api/notebooks/${notebook.id}/chats`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ title: 'A named chat' })
    });
    await ui.reload(app);
    await ui.settle(300);

    checks.check(
      'a conversation appears under its notebook, and opens as the newest',
      (await chatRows()).join(' | ') === 'A named chat' && (await openRow()) === 'A named chat',
      `${(await chatRows()).join(' | ')} · open=${await openRow()}`
    );
    const nesting = await ui.eval(`
      const lists = [...document.querySelectorAll('.sidebar .chats')];
      return {
        lists: lists.length,
        under: lists.length === 1 ? lists[0].closest('li')?.querySelector('.row.active .item')?.textContent.trim() : ''
      };
    `);
    checks.check(
      'only the notebook you are in expands',
      nesting.lists === 1 && (nesting.under ?? '').startsWith('Renamed Notes'),
      JSON.stringify(nesting)
    );
    checks.check(
      'and the breadcrumb reads the same tree',
      (await crumbTail()).join(' / ') === 'libris / Renamed Notes / A named chat',
      (await crumbTail()).join(' / ')
    );
    const shape = await ui.eval(`
      const c = [...document.querySelectorAll('.crumbs .crumb')];
      return {
        links: c.filter((x) => x.tagName === 'BUTTON').map((x) => x.textContent.trim()),
        last: c.at(-1)?.tagName
      };
    `);
    checks.check(
      'only libris climbs out; the notebook and the chat are where you are',
      shape.links.join(',') === 'libris' && shape.last === 'SPAN',
      JSON.stringify(shape)
    );

    await fetch(`${app}api/notebooks/${notebook.id}/chats`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ title: 'Second chat' })
    });
    await ui.reload(app);
    await ui.settle(300);
    checks.check(
      'newest first, and the newest is the one that opens',
      (await chatRows()).join(' | ') === 'Second chat | A named chat' && (await openRow()) === 'Second chat',
      `${(await chatRows()).join(' | ')} · open=${await openRow()}`
    );

    await ui.until(`
      const row = [...document.querySelectorAll('.sidebar .chat-open')]
        .find((b) => b.textContent.trim() === 'A named chat');
      if (!row) return null;
      row.click();
      return true;
    `);
    checks.check(
      'clicking a row opens it, and the breadcrumb follows',
      (await openRow()) === 'A named chat' && (await crumbTail()).at(-1) === 'A named chat',
      `open=${await openRow()} · ${(await crumbTail()).join(' / ')}`
    );

    // A first question is a poor label by the time it is one of five. Renaming is
    // inline, like a notebook, and blur commits it — a click elsewhere means "that is
    // what it is called".
    await ui.until(`
      const btn = [...document.querySelectorAll('.chat-rename-open')]
        .find((b) => b.getAttribute('aria-label') === 'Rename A named chat');
      if (!btn) return null;
      btn.click();
      return true;
    `);
    await ui.settle(150);
    // Committed with a key, not a blur: the input is a rendered control and Enter is
    // its documented commit, and a synthetic blur only fires if something had actually
    // focused it — which a click driven from the debugger is not obliged to do.
    const edited = await ui.eval(`
      const el = document.querySelector('.sidebar input.chat-rename');
      if (!el) return null;
      el.value = 'Why the tail is long';
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
      return true;
    `);
    checks.check(
      'a chat renames in place, and the header follows',
      edited === true &&
        (await ui.until(`return [...document.querySelectorAll('.sidebar .chat-open')]
          .map((b) => b.textContent.trim()).includes('Why the tail is long') ? true : null`)) === true &&
        (await openRow()) === 'Why the tail is long' &&
        (await crumbTail()).at(-1) === 'Why the tail is long',
      `${(await chatRows()).join(' | ')} · ${(await crumbTail()).join(' / ')}`
    );

    await ui.click('Hide notebooks');
    await ui.settle(200);
    checks.check(
      'the rail is notebooks only, and the header still says which chat is open',
      (await ui.eval(`return document.querySelectorAll('.sidebar .chat-open, .sidebar .chats').length`)) === 0 &&
        (await ui.eval(`return document.querySelectorAll('.sidebar .rail-item').length`)) > 0 &&
        (await crumbTail()).at(-1) === 'Why the tail is long',
      `${await ui.eval(`return document.querySelectorAll('.sidebar .rail-item').length`)} rail items · ${(await crumbTail()).join(' / ')}`
    );
    await ui.click('Show notebooks');
    await ui.settle(200);

    await ui.until(`
      const btn = [...document.querySelectorAll('.chat-delete-open')]
        .find((b) => b.getAttribute('aria-label') === 'Delete Why the tail is long');
      if (!btn) return null;
      btn.click();
      return true;
    `);
    const confirmChat = await ui.eval(`
      const d = document.querySelector('dialog.delete-chat');
      return {
        open: d?.open === true,
        names: /Why the tail is long/.test(d?.querySelector('h2')?.textContent ?? ''),
        keeps: /sources it cites are untouched/.test(d?.textContent ?? ''),
        focused: document.activeElement?.textContent.trim(),
        danger: [...(d?.querySelectorAll('button') ?? [])]
          .some((b) => b.textContent.trim() === 'Delete chat' && b.className.includes('btn-danger'))
      };
    `);
    checks.check(
      'deleting a conversation asks first, and names it',
      confirmChat.open && confirmChat.names,
      JSON.stringify(confirmChat)
    );
    checks.check(
      'the box says the sources survive',
      confirmChat.keeps,
      JSON.stringify(confirmChat)
    );
    checks.check(
      'the destructive button is red and Cancel has the focus',
      confirmChat.danger && confirmChat.focused === 'Cancel',
      JSON.stringify(confirmChat)
    );

    await ui.click('Delete chat');
    checks.check(
      'confirming drops the row, and the next conversation opens',
      (await ui.until(`return [...document.querySelectorAll('.sidebar .chat-open')]
        .map((b) => b.textContent.trim()).join(' | ') === 'Second chat' ? true : null`)) === true &&
        (await openRow()) === 'Second chat' &&
        (await crumbTail()).at(-1) === 'Second chat',
      `${(await chatRows()).join(' | ')} · ${(await crumbTail()).join(' / ')}`
    );
    checks.check(
      'the panel still says nothing up there',
      (await crumbTail()).length === 3,
      (await crumbTail()).join(' / ')
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
    // A notebook nobody has asked in opens on the landing, and the landing has no
    // composer to measure — so get a conversation on screen before measuring it.
    await ui.until(`
      const open = document.querySelector('.sidebar .chat-open');
      if (open) { open.click(); return true; }
      const draft = [...document.querySelectorAll('button')]
        .find((b) => b.textContent.trim() === 'New conversation');
      if (draft) { draft.click(); return true; }
      return null;
    `);
    await ui.settle(300);
    await ui.setViewport(1800, 900);
    await ui.settle(400);
    await ui.eval(`
      [...document.querySelectorAll('.rail-toggle')].find((b) => b.title === 'Hide panel')?.click();
      return true;
    `);
    await ui.settle(400);
    // Measured against the composer column, not a header row: the header is gone, and
    // the rule the CSS makes is that the column holding the thread and the column
    // holding the box are the same width, and neither is the pane.
    const measure = await ui.eval(`
      const w = (s) => Math.round(document.querySelector(s).getBoundingClientRect().width);
      return { pane: w('.chat'), column: w('.chat .composer'), entry: w('.composer .entry') };
    `);
    checks.check(
      "the composer is the thread's measure, not the pane's",
      measure.column === measure.entry && measure.pane - measure.column >= 100,
      JSON.stringify(measure)
    );

    // ── the settings action bar ──────────────────────────────────
    // Settings is several screens of fields, so the bar that commits them belongs
    // where the page is, not under its last row. These checks stay read-only: the
    // first field is dirtied and then Discarded, so nothing another harness reads
    // from the server is ever saved.
    await ui.eval(
      `return [...document.querySelectorAll('button')].find((b) => b.title === 'Settings').click()`
    );
    await ui.settle();

    const bar = `
      const a = document.querySelector('.panels > .actions');
      if (!a) return { buttons: 'no bar' };
      const r = a.getBoundingClientRect();
      return {
        buttons: [...a.querySelectorAll('button')]
          .map((b) => b.textContent.trim() + (b.disabled ? ' off' : ' on'))
          .join(', '),
        status: a.querySelector('.status')?.textContent.trim().slice(0, 40),
        top: Math.round(r.top),
        scrollerTop: Math.round(document.querySelector('main').getBoundingClientRect().top),
        onScreen: r.bottom <= innerHeight && r.top >= 0
      };
    `;
    const clean = await ui.eval(bar);
    checks.check('the settings bar carries both buttons', clean.buttons === 'Discard off, Save changes off', `${clean.buttons}`);
    checks.check('a clean page says where its values came from', /Saved settings loaded/.test(clean.status), clean.status);

    await ui.eval(type('.panels input[type=text]:not([disabled])', 'unsaved-probe'));
    await ui.settle(200);
    const dirty = await ui.eval(bar);
    checks.check('a change enables both', dirty.buttons === 'Discard on, Save changes on', `${dirty.buttons}`);
    checks.check('and the status changes with it', /Unsaved changes/.test(dirty.status), dirty.status);

    await ui.click('Discard');
    await ui.settle(400);
    const afterDiscard = await ui.eval(bar);
    checks.check('Discard reloads and disables both', afterDiscard.buttons === 'Discard off, Save changes off', `${afterDiscard.buttons}`);

    // The trap this file exists to catch: <main> is the scroller, not the window, and
    // the bar sticks to *its* top edge. Reaching for the header's height as the offset —
    // the obvious guess — leaves a band of fields sliding through in the gap above it.
    await ui.eval(`const m = document.querySelector('main'); m.scrollTop = m.scrollHeight; return 1`);
    await ui.settle(200);
    const stuck = await ui.eval(bar);
    checks.check('deep in the page the bar is still on screen', stuck.onScreen, `top=${stuck.top}`);
    checks.check('flush with the scroller, not a header-height below it', stuck.top === stuck.scrollerTop, `top=${stuck.top} scroller=${stuck.scrollerTop}`);

    await ui.setViewport(640, 900);
    await ui.settle(200);
    const narrow = await ui.eval(bar);
    checks.check('at 640px the bar wraps instead of overflowing', narrow.onScreen && narrow.buttons.split(', ').length === 2, `top=${narrow.top}`);
    await ui.clearViewport();

    checks.check('no uncaught page errors', ui.pageErrors.length === 0, ui.pageErrors.slice(0, 2).join(' | '));
    checks.check('no console errors', ui.consoleErrors.length === 0, ui.consoleErrors.slice(0, 2).join(' | '));

    return ui.pageErrors.length + ui.consoleErrors.length === 0 ? 0 : 1;
  }
);

process.exit(checks.report() || code);
