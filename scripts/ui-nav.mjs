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
    await ui.click('New');
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
      return row ? { row: true, title: document.querySelector('.title h1')?.textContent } : null;
    `);
    checks.check(
      'the typed name is the notebook name',
      created?.row === true && created?.title === 'Nav Notes',
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
      return row ? { row: true, editing: !!document.querySelector('.sidebar input.rename'),
                     title: document.querySelector('.title h1')?.textContent } : null;
    `);
    checks.check(
      'Enter commits the new name',
      committed?.row === true && committed?.editing === false && committed?.title === 'Renamed Notes',
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

    checks.check('no uncaught page errors', ui.pageErrors.length === 0, ui.pageErrors.slice(0, 2).join(' | '));
    checks.check('no console errors', ui.consoleErrors.length === 0, ui.consoleErrors.slice(0, 2).join(' | '));

    return ui.pageErrors.length + ui.consoleErrors.length === 0 ? 0 : 1;
  }
);

process.exit(checks.report() || code);
