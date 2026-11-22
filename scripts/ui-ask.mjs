// The answer path through the real UI: ask a question against an ingested PDF, watch
// the turn stream, open a citation, and check the turn survived a reload.
//
// Requires a running server and the fake llama.cpp (both started by `make ui`).
// Standalone:
//   APP=http://127.0.0.1:8099/ EMBED=http://127.0.0.1:8124 node scripts/ui-ask.mjs
import { Checks, withBrowser, UPLOAD_FIXTURE, CRUMB_NOTEBOOK } from './cdp.mjs';

const llama = process.env.EMBED;
if (!llama) {
  console.log('SKIP: needs the fake llama.cpp server (run it through `make ui`)');
  process.exit(0);
}

/** Script what the model says next, or take it back to the derived default. */
const control = (cmd) =>
  fetch(`${llama}/control`, {
    method: 'POST',
    body: JSON.stringify(cmd),
    headers: { 'content-type': 'application/json' }
  });
const stats = () => fetch(`${llama}/stats`).then((r) => r.json());

const app = process.env.APP ?? 'http://127.0.0.1:8099/';
const cdpPort = Number(process.env.CDP_PORT ?? 9323);

const api = app.replace(/\/$/, '');
const checks = new Checks();

/** A research turn that searched, and one that refused, in the shape llama.cpp sends. */
const searched = (query) => ({
  choices: [
    {
      message: {
        role: 'assistant',
        content: null,
        tool_calls: [
          {
            id: 'c_search',
            type: 'function',
            function: {
              name: 'query_knowledge_base',
              arguments: JSON.stringify({ keywords: query.split(/\s+/).slice(0, 4).join(' '), query })
            }
          }
        ]
      },
      finish_reason: 'tool_calls'
    }
  ]
});

const refused = (reason) => ({
  choices: [
    {
      message: {
        role: 'assistant',
        content: null,
        tool_calls: [
          {
            id: 'c_refuse',
            type: 'function',
            function: { name: 'no_info_found', arguments: JSON.stringify({ reason }) }
          }
        ]
      },
      finish_reason: 'tool_calls'
    }
  ]
});

/**
 * Waits for the chat to be on screen. It used to be a tab that had to be opened
 * first; the chat is the page now, so there is nothing to click and this is only
 * the "has it rendered yet" half of what it was.
 */
const openAskTab = (ui) => ui.until(`return document.querySelector('.chat') ? true : null`);

/**
 * Selects a notebook in the sidebar by name and confirms the view switched to it.
 * Reports what the header actually says, because "the wrong notebook is open" and
 * "nothing has rendered yet" need telling apart when a run fails on a shared server.
 */
async function openNotebook(ui, name) {
  const clicked = await ui.until(`
    const row = [...document.querySelectorAll('.sidebar .list .item')]
      .find((i) => i.textContent.includes(${JSON.stringify(name)}));
    if (!row) return null;
    row.click();
    return true;
  `);
  if (!clicked) return { ok: false, seen: 'no such row in the sidebar' };

  const seen = await ui.until(`
    const title = ${CRUMB_NOTEBOOK};
    // Empty means the view has not rendered yet, so keep waiting; any other name is
    // a real answer and an immediate failure.
    return title === ${JSON.stringify(name)} ? true : (title || null);
  `);
  return { ok: seen === true, seen };
}

const ask = (ui) =>
  ui.eval(`
    const btn = document.querySelector('.composer button[title="Ask"]:not([disabled])');
    if (!btn) throw new Error('the Ask button is disabled');
    btn.click();
    return true;
  `);

const code = await withBrowser({ port: cdpPort, app, profile: '/tmp/libris-ask-profile' }, async (ui) => {
  // ── a notebook with one ready source ───────────────────────────
  // Created on the server with a name nobody else would type, then opened by that
  // name. With a shared server and notebooks from earlier runs, "the last row" and
  // "the drop zone" are both silent breakage: the previous notebook's panel is already
  // on screen, the upload lands in it, and a later click wins the race.
  const name = `ask ${Date.now()}`;
  const created = await fetch(`${api}/api/notebooks`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ name })
  }).then((r) => r.json());
  checks.check('a notebook exists to ask in', created.name === name, JSON.stringify(created).slice(0, 120));

  await ui.reload(app);
  const opened = await openNotebook(ui, name);
  checks.check('it is the notebook on screen', opened.ok, () => JSON.stringify(opened));

  await ui.setFiles('input[type=file]', [UPLOAD_FIXTURE]);
  const ready = await ui.until(
    `
    const row = document.querySelector('.source');
    const badge = row && row.querySelector('.badge');
    return badge && badge.textContent.trim() !== 'processing' &&
      { status: badge.textContent.trim(), chunks: row.querySelector('.facts')?.textContent };
    `,
    200,
    100
  );
  checks.check('the source is ready, so it is searchable', ready?.status === 'ready', () => JSON.stringify(ready));

  await control({ reset: true });

  // ── the Ask tab ────────────────────────────────────────────────
  checks.check('the Ask tab is reachable', await openAskTab(ui));
  const panel = await ui.until(`
    const box = document.querySelector('.composer textarea');
    return box && {
      placeholder: box.placeholder,
      askDisabled: document.querySelector('.composer button[title="Ask"]')?.disabled
    };
  `);
  checks.check('the ask tab opens a composer', !!panel?.placeholder, JSON.stringify(panel));
  checks.check('Ask is disabled with nothing typed', panel?.askDisabled === true, () => `disabled=${panel?.askDisabled}`);

  await send(ui, 'What does the paper measure?');
  await ui.settle(60); // a $state write lands on a microtask; the button is re-rendered after it
  const enabled = await ui.eval(`return document.querySelector('.composer button[title="Ask"]')?.disabled`);
  checks.check('typing enables Ask', enabled === false, () => `disabled=${enabled}`);

  await control({ answer: 'It reports 4.2 K in section two. [[Chunk 1]]' });
  await ask(ui);

  // The question is on screen before the model has done anything.
  const optimistic = await ui.eval(`
    const turn = document.querySelector('.turn.user');
    return turn && {
      text: turn.textContent.trim(),
      pending: !!document.querySelector('.turn.live')
    };
  `);
  checks.check('the question appears immediately', optimistic?.text === 'What does the paper measure?', () => JSON.stringify(optimistic));
  checks.check('a live turn is shown while it runs', optimistic?.pending === true, () => JSON.stringify(optimistic));

  const answered = await ui.until(`
    const turns = [...document.querySelectorAll('.turn.assistant')];
    const last = turns[turns.length - 1];
    return last && !last.classList.contains('live') && last.querySelector('.answer') &&
      { text: last.querySelector('.answer').textContent.trim(),
        chips: [...last.querySelectorAll('.chips .cite')].map((c) => c.textContent.trim()),
        inline: [...last.querySelectorAll('.answer .inline')].map((b) => b.textContent.trim()),
        note: last.querySelector('.note')?.textContent.replace(/\\s+/g, ' ').trim() ?? '' };
  `);

  checks.check(
    'the streamed answer is the model’s sentence',
    /It reports 4.2 K in section two/.test(answered?.text ?? ''),
    () => `answer=${answered?.text}`
  );
  checks.check(
    'a citation label never reaches the reader as text',
    !/Chunk\s*\d|\[\[/.test(answered?.text ?? ''),
    () => `answer=${answered?.text}`
  );
  checks.check('the answer carries a clickable citation', (answered?.chips.length ?? 0) > 0, () => JSON.stringify(answered?.chips));
  checks.check(
    'a citation says where inside the source it points',
    /Chunk 1 · pages? \d/.test(answered?.chips?.[0] ?? ''),
    () => `chip=${answered?.chips?.[0]}`
  );
  checks.check('the label is also a button inside the sentence', (answered?.inline.length ?? 0) > 0, () => JSON.stringify(answered?.inline));
  checks.check('an answered turn carries no warning', answered?.note === '', () => `note=${answered?.note}`);

  // ── the two calls the turn is made of ──────────────────────────
  const wire = await stats();
  checks.check('the turn made a research call and a synthesis call', wire.chatRequests >= 2, () => JSON.stringify(wire));
  checks.check(
    'the research model was given exactly the three tools it is promised',
    (wire.lastTools ?? []).join(',') === 'query_knowledge_base,finalize_research_response,no_info_found',
    () => `tools=${wire.lastTools}`
  );
  // At least one searched and exactly one did not: the call that writes the answer is
  // never given tools, which is what keeps the research trace out of the prose.
  checks.check(
    'the calls that searched outnumber the one that answered by more than none',
    wire.researchCalls >= 1 && wire.chatRequests === wire.researchCalls + 1,
    () => `research=${wire.researchCalls} of ${wire.chatRequests}`
  );
  checks.check('the answer call streamed', wire.lastStream === true, () => `stream=${wire.lastStream}`);

  // ── opening a citation ─────────────────────────────────────────
  await ui.eval(`
    document.querySelector('.turn.assistant:last-of-type .chips .cite').click();
    return true;
  `);
  // The viewer is on screen before its chunks are — the heading renders from the
  // source, the list arrives a fetch later. Measuring the ring any earlier reports a
  // working feature as broken.
  const crumbsBefore = await ui.crumbs();
  const viewer = await ui.until(`
    const dlg = document.querySelector('.viewer');
    if (!dlg || !dlg.querySelector('.chunk, .tile')) return null;
    return {
      heading: dlg.querySelector('h2')?.textContent,
      highlighted: dlg.querySelectorAll('.chunk.highlighted, .tile.highlighted').length
    };
  `);
  checks.check('a citation opens the source it cites', /two-page\.pdf/.test(viewer?.heading ?? ''), () => `heading=${viewer?.heading}`);
  checks.check('it opens on the cited chunk, ringed', viewer?.highlighted === 1, () => `highlighted=${viewer?.highlighted}`);

  // No Escape any more: the viewer is a step off Sources, not a top layer, so Back is
  // the only way out — and the answer that cited it never left. Leaving it must not
  // move the header either: the viewer is a step inside a panel, not a place, so the
  // crumbs before and after are the same list.
  await ui.eval(`document.querySelector('.viewer [aria-label="Back to sources"]').click(); return true;`);
  const closed = await ui.until(`
    return !document.querySelector('.viewer') && document.querySelector('.chat .turn.assistant')
      ? [...document.querySelectorAll('.crumbs .crumb')].map((x) => x.textContent.trim())
      : null;
  `);
  checks.check(
    'the chat is still there, answer and all, when the source closes',
    closed !== null && closed.join(' / ') === crumbsBefore.join(' / '),
    () => `${crumbsBefore.join(' / ')} → ${JSON.stringify(closed)}`
  );

  // ── the turn is the server's, not the page's ───────────────────
  await ui.reload(app);
  const reopened = await openNotebook(ui, name);
  checks.check('the notebook is still the one on screen after a reload', reopened.ok, () => JSON.stringify(reopened));
  checks.check('and the Ask tab is reachable again', await openAskTab(ui));
  const afterReload = await ui.until(`
    const turn = document.querySelector('.turn.assistant');
    return turn && {
      text: turn.querySelector('.answer')?.textContent.trim(),
      chips: turn.querySelectorAll('.chips .cite').length,
      title: document.querySelector('.thread .title, .thread .picker')?.textContent.trim()
    };
  `);
  checks.check('the answer survived a reload', /4\.2 K/.test(afterReload?.text ?? ''), () => `text=${afterReload?.text}`);
  checks.check('the citations survived too', afterReload?.chips === 1, () => `chips=${afterReload?.chips}`);
  checks.check(
    'the thread is named after the first question',
    /paper measure/.test(afterReload?.title ?? ''),
    () => `title=${afterReload?.title}`
  );

  // ── the other ways a turn ends ─────────────────────────────────
  // A model that says the library cannot answer, after searching: its reason *is* the
  // answer, and the turn is labelled no-evidence rather than rendered as if grounded.
  await control({ reset: true, script: [searched('how tall is the author'), refused('No measurements in these pages')] });
  const noInfo = await askAndRead(ui, 'How tall is the author?');
  checks.check(
    'the model’s own reason becomes the answer',
    noInfo?.text === 'No measurements in these pages',
    () => `text=${noInfo?.text}`
  );
  checks.check('and says so in the answer', /No evidence/.test(noInfo?.note ?? ''), () => `note=${noInfo?.note}`);

  // The gate, from the model's side: a refusal offered before searching anything is
  // not accepted, so the loop keeps asking, searches on its own terms, and the turn
  // ends at the depth limit with nothing found — never with a refusal-shaped answer
  // the user cannot tell apart from an empty library.
  await control({ reset: true, hold: true, script: [refused('I am not going to search')] });
  const gated = await askAndRead(ui, 'Is anything in here at all?');
  const gateWire = await stats();
  checks.check('a refusal before searching does not end the turn', /maximum research depth/.test(gated?.text ?? ''), () => `text=${gated?.text}`);
  checks.check('and the model was made to search first', gateWire.researchCalls >= 2, () => `research calls=${gateWire.researchCalls}`);

  // A model that ignores tool_choice and answers in prose. The turn is kept, but it is
  // labelled uncited rather than dressed up as a grounded answer.
  await control({
    reset: true,
    hold: true,
    script: [{ choices: [{ message: { role: 'assistant', content: 'Off the top of my head, about four kelvin.' }, finish_reason: 'stop' }] }]
  });
  const prose = await askAndRead(ui, 'And roughly how cold was it?');
  checks.check('prose instead of tools is kept', /four kelvin/.test(prose?.text ?? ''), () => `text=${prose?.text}`);
  checks.check('and labelled uncited', /Uncited/.test(prose?.note ?? ''), () => `note=${prose?.note}`);

  // A model that is not listening at all.
  await control({ reset: true, chatDown: true });
  const down = await askAndRead(ui, 'Is the model up?');
  checks.check('a dead server produces a banner, not a stall', down?.banner !== '', () => `banner=${down?.banner}`);
  await control({ reset: true });

  // ── stop means stop ────────────────────────────────────────────
  // A long answer in a sentence nothing else in this file says, streamed slowly enough
  // that the button is reachable mid-sentence. The marker is what proves the row read
  // below is *this* turn and not one still on screen from earlier.
  const marker = 'Nine zero four seven two six one eight. ';
  const long = marker.repeat(90) + '[[Chunk 1]]';
  await control({ reset: true, answer: long });
  const turnsBeforeStop = await ui.eval(`return document.querySelectorAll('.turn.assistant').length`);
  await send(ui, 'Tell me everything about it');
  await ui.settle(60);
  await ask(ui);
  // `:not(.pulse)` matters. While the loop is still searching, the live bubble shows
  // its status in the same slot, and "Searching library for …" satisfies any length
  // test — so clicking Stop there stops a turn that has written nothing yet.
  const streaming = await ui.until(`
    const live = document.querySelector('.turn.live .answer:not(.pulse)');
    const text = (live?.textContent ?? '').trim();
    return text.startsWith(${JSON.stringify(marker.slice(0, 16))}) && text.length > 40 ? text : null;
  `);
  checks.check('the answer arrives piece by piece', !!streaming, () => `streamed=${streaming?.slice(0, 40)}`);

  await ui.eval(`
    const btn = document.querySelector('.composer button[title^="Stop"]');
    if (!btn) throw new Error('no Stop button: ' +
      [...document.querySelectorAll('.composer button')].map((b) => b.title).join('|'));
    btn.click();
    return true;
  `);
  const stopped = await ui.until(`
    const all = [...document.querySelectorAll('.turn.assistant')];
    // The count guard again: without it this returns the previous turn the instant
    // Stop is clicked, because that turn is also a finished, non-live row.
    if (all.length <= ${turnsBeforeStop}) return null;
    const last = all.at(-1);
    if (last.classList.contains('live')) return null;
    return {
      text: last.querySelector('.answer')?.textContent.trim(),
      note: last.querySelector('.note')?.textContent.replace(/\\s+/g, ' ').trim() ?? '',
      banner: document.querySelector('.banner')?.textContent.replace(/\\s+/g, ' ').trim() ?? '',
      live: !!document.querySelector('.turn.live'),
      // Which rows are on screen, when a check about "the last one" is not enough.
      rows: all.map((t) => [...t.classList].join('.') + '=' +
        (t.querySelector('.answer')?.textContent ?? 'no .answer').trim().slice(0, 12))
    };
  `, 600, 100);
  // The row has to be the turn that was just stopped — the marker is what says so.
  // Without it, a stale row from earlier in the run satisfies "a finished turn with a
  // warning", and the check passes on somebody else's cancel.
  const where = () => (stopped?.rows ?? ['no turn at all']).join('  |  ');
  checks.check(
    'the row on screen is the turn that was stopped',
    (stopped?.text ?? '').startsWith(marker.slice(0, 16)),
    where
  );
  checks.check(
    'it kept what was written and no more',
    (stopped?.text.length ?? 0) > 0 && (stopped?.text.length ?? 0) < long.length,
    () => `${stopped?.text.length} of ${long.length} chars`
  );
  checks.check('and says that it was stopped', /Stopped/.test(stopped?.note ?? ''), where);

  // Everything above is a deliberate failure — a dead server, a stopped stream, a
  // model that refuses to search. None of it may look like a bug in the page.
  checks.check(
    'no page or console errors across the whole run',
    ui.pageErrors.length + ui.consoleErrors.length === 0,
    () => JSON.stringify([...ui.pageErrors, ...ui.consoleErrors]).slice(0, 400)
  );

  // The count of failures is the exit status. A suite that prints FAIL and exits 0 is
  // worse than no suite: `make ui` would go green around it.
  return checks.report();
});

/**
 * Asks one question and reads *that* turn, not the one before it. The count is taken
 * first for exactly that reason: with a previous answer on screen, asking for "the
 * last assistant turn" returns the old one before the new one has even started.
 */
async function askAndRead(ui, question) {
  const before = await ui.eval(`return document.querySelectorAll('.turn.assistant').length`);
  await send(ui, question);
  await ui.settle(60);
  await ask(ui);
  return ui.until(
    `
    const all = [...document.querySelectorAll('.turn.assistant')];
    const banner = document.querySelector('.banner');
    const turn = all.length > ${before} ? all.at(-1) : null;
    if (turn && turn.classList.contains('live')) return null;
    if (!turn && !banner) return null;
    return {
      text: turn?.querySelector('.answer')?.textContent.trim() ?? '',
      note: turn?.querySelector('.note')?.textContent.replace(/\\s+/g, ' ').trim() ?? '',
      banner: banner?.textContent.replace(/\\s+/g, ' ').trim() ?? ''
    };
    `,
    400,
    100
  );
}

/**
 * Types into the composer the way a keyboard does. The prototype setter, because
 * Svelte tracks the value through the input event — assigning `.value` directly would
 * never reach the component, and the Ask button would stay disabled.
 */
function send(ui, text) {
  return ui.eval(`
    const box = document.querySelector('.composer textarea');
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(box, ${JSON.stringify(text)});
    box.dispatchEvent(new Event('input', { bubbles: true }));
    return box.value;
  `);
}


process.exit(code);
