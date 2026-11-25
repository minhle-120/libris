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
  // Deliberate, not incidental: headless Chrome defaults to 800x600, which is narrow
  // enough that the panel is now closed by the narrow rule (web/src/lib/panel.ts). A
  // harness that inherits a window size is a harness that silently tests one layout.
  await ui.setViewport(1400, 900);

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

  // A conversation begins at its first question, so an unanswered notebook has none —
  // and no "New chat" row waiting to be abandoned into the list.
  const beforeFirst = await ui.eval(`
    return {
      rows: [...document.querySelectorAll('.sidebar .chat-open')].map((b) => b.textContent.trim()),
      newChat: !!document.querySelector('.sidebar .chat-new')
    };
  `);
  checks.check(
    'a notebook nobody has asked in lists no conversations',
    beforeFirst.rows.length === 0 && beforeFirst.newChat === false,
    () => JSON.stringify(beforeFirst)
  );

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
  // Both calls get the page. The researcher is the one that decides what to cite, and it
  // used to be handed a line saying an image exists somewhere — which is nothing to
  // decide with, and is no longer what goes out.
  checks.check(
    'the research call was shown the page itself, in its tool result',
    (wire.lastResearchMedia?.tool ?? 0) > 0,
    () => `media=${JSON.stringify(wire.lastResearchMedia)}`
  );
  checks.check(
    'and so was the call that wrote the answer',
    (wire.lastAnswerMedia?.user ?? 0) > 0,
    () => `media=${JSON.stringify(wire.lastAnswerMedia)}`
  );
  // At least one searched and exactly one did not: the call that writes the answer is
  // never given tools, which is what keeps the research trace out of the prose.
  checks.check(
    'the calls that searched outnumber the one that answered by more than none',
    wire.researchCalls >= 1 && wire.chatRequests === wire.researchCalls + 1,
    () => `research=${wire.researchCalls} of ${wire.chatRequests}`
  );
  checks.check('the answer call streamed', wire.lastStream === true, () => `stream=${wire.lastStream}`);

  // The switch that lets a research model think before it searching is only a setting if it
  // reaches the wire. Off is the default, and the answer turn is expected to say nothing
  // about thinking either way: what the reader is shown stays the server's to offer.
  checks.check(
    'the research call carried the thinking switch, off by default',
    wire.lastResearchKwargs?.enable_thinking === false,
    () => JSON.stringify(wire.lastResearchKwargs)
  );
  checks.check(
    'the answer call said nothing about thinking',
    wire.lastAnswerKwargs === null,
    () => JSON.stringify(wire.lastAnswerKwargs)
  );

  // The row appeared because the question did, and it is the server's title — the
  // question, one line — not a string the browser made up on the way past.
  const thread = await ui.until(`
    const rows = [...document.querySelectorAll('.sidebar .chat-open')];
    const row = rows.find((b) => b.textContent.includes('What does the paper measure'));
    return row && {
      count: rows.length,
      title: row.textContent.trim(),
      active: row.closest('.chat-row')?.classList.contains('active') === true
    };
  `);
  checks.check(
    'the question started the conversation, and the list shows it',
    thread?.count === 1 && thread?.title === 'What does the paper measure?' && thread?.active === true,
    () => JSON.stringify(thread)
  );
  const crumbsAfter = await ui.crumbs();
  checks.check(
    'and the breadcrumb ends with it',
    crumbsAfter.at(-1) === 'What does the paper measure?',
    () => crumbsAfter.join(' / ')
  );

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
      title: document.querySelector('.sidebar .chat-row.active .chat-open')?.textContent.trim()
    };
  `);
  checks.check('the answer survived a reload', /4\.2 K/.test(afterReload?.text ?? ''), () => `text=${afterReload?.text}`);
  // The number the live turn showed, whatever it was: a turn rests on the evidence it
  // cited, and a reload that lost one of them would quietly rewrite what the answer claims.
  checks.check(
    'the citations survived too',
    afterReload?.chips > 0 && afterReload.chips === answered?.chips?.length,
    () => `chips=${afterReload?.chips}, live=${answered?.chips?.length}`
  );
  checks.check(
    'the thread is named after the first question',
    /paper measure/.test(afterReload?.title ?? ''),
    () => `title=${afterReload?.title}`
  );

  // ── the thinking behind an answer ───────────────────────────
  // A model that thinks out loud used to have it shown for a moment and thrown away, so the
  // only reader who could see it was one who happened to be looking. It goes into a
  // disclosure now: open while the thinking arrives, closed by the first sentence, and still
  // openable after a reload, because the thinking is stored with the answer.
  const THOUGHT =
    'The question asks for a figure. Page two holds one, and the registry numbers it first, ' +
    'so the answer is a sentence with one label in it. '.repeat(8);
  // Long on purpose, both halves: each phase has to last longer than a poll takes, or the
  // harness asserts on a race it happens to win.
  const SAID =
    'It reports 4.2 K again. ' +
    'The figure is repeated on the second page for the reader of the table. '.repeat(12) +
    ' [[Chunk 1]]';
  await control({ answer: SAID, think: THOUGHT });
  await send(ui, 'And the figure again?');
  await ask(ui);

  const thinking = await ui.until(
    `
    const row = document.querySelector('.turn.live .think');
    return row && row.classList.contains('open') && {
      label: row.querySelector('.label')?.textContent.trim(),
      shown: row.querySelector('.scroll')?.textContent.trim().length ?? 0,
      hidden: getComputedStyle(row.querySelector('.panel')).display !== 'none'
    };
    `,
    200,
    10
  );
  checks.check(
    'the thinking is on screen while it arrives',
    thinking?.label === 'Thinking…' && thinking?.hidden && (thinking?.shown ?? 0) > 0,
    () => JSON.stringify(thinking)
  );

  const midAnswer = await ui.until(
    `
    const turn = document.querySelector('.turn.live');
    const row = turn && turn.querySelector('.think');
    return row && !row.classList.contains('open') && turn.querySelector('.answer') && {
      expanded: row.querySelector('.row').getAttribute('aria-expanded'),
      hidden: getComputedStyle(row.querySelector('.panel')).display === 'none'
    };
    `,
    200,
    10
  );
  checks.check(
    'the first sentence closes it',
    midAnswer?.expanded === 'false' && midAnswer?.hidden,
    () => JSON.stringify(midAnswer)
  );

  const stored = await ui.until(`
    const last = [...document.querySelectorAll('.turn.assistant')].at(-1);
    const row = last && !last.classList.contains('live') && last.querySelector('.think');
    return row && {
      expanded: row.querySelector('.row').getAttribute('aria-expanded'),
      controls: row.querySelector('.row').getAttribute('aria-controls'),
      label: row.querySelector('.label').textContent.trim(),
      hidden: getComputedStyle(row.querySelector('.panel')).display === 'none'
    };
  `);
  checks.check(
    'the row it leaves behind is closed, and says so',
    stored?.expanded === 'false' && stored?.hidden,
    () => JSON.stringify(stored)
  );
  checks.check(
    'and the row says how long the model thought',
    /^Thought for (\d|under a second)/.test(stored?.label ?? ''),
    () => `label=${stored?.label}`
  );

  // Read separately from the click: the click writes state, and the row it re-renders on
  // arrives a microtask later — read in the same breath and you see the old attribute.
  const read = (sel) =>
    ui.eval(`
      const row = ${sel};
      const panel = document.getElementById(row.getAttribute('aria-controls'));
      const turn = row.closest('.turn');
      return {
        expanded: row.getAttribute('aria-expanded'),
        shown: getComputedStyle(panel).display !== 'none',
        text: panel.textContent.trim(),
        answer: turn.querySelector('.answer')?.textContent.trim() ?? ''
      };
    `);

  const open = async (sel) => {
    await ui.eval(`${sel}.click(); return true`);
    await ui.settle(80);
    return read(sel);
  };

  const seen = await open(`[...document.querySelectorAll('.turn.assistant .think .row')].at(-1)`);
  checks.check(
    'a click opens it',
    seen?.expanded === 'true' && seen?.shown,
    () => JSON.stringify({ expanded: seen?.expanded, shown: seen?.shown })
  );
  checks.check(
    'every piece of the thinking arrived, not only the last one',
    (seen?.text ?? '').startsWith(THOUGHT.slice(0, 40)) &&
      (seen?.text ?? '').length > THOUGHT.length - 60,
    () => `${seen?.text?.length} chars, want ${THOUGHT.length}`
  );
  checks.check(
    'none of it leaked into the answer',
    /4\.2 K/.test(seen?.answer ?? '') && !/Page two holds one/.test(seen?.answer ?? ''),
    () => `answer=${seen?.answer?.slice(0, 90)}`
  );

  // Stored, not remembered: the page forgets the turn and reads it back from the database.
  await control({ think: '' });
  await ui.reload(app);
  checks.check('the notebook is still the one on screen', (await openNotebook(ui, name)).ok);
  checks.check('and the Ask tab is reachable again', await openAskTab(ui));
  await ui.settle(300);
  const again = await open(`[...document.querySelectorAll('.turn.assistant .think .row')].at(-1)`);
  checks.check(
    'an answer read back from the database still opens',
    again?.expanded === 'true' && (again?.text ?? '').startsWith(THOUGHT.slice(0, 40)),
    () => `${again?.expanded} · ${(again?.text ?? '').slice(0, 40)}`
  );

  // ── a rendered answer ──────────────────────────────────────
  // The synthesiser writes markdown, LaTeX and — if a source provoked it — HTML. This one
  // answer exercises the whole surface, including the two things it must never do: run
  // what it was given, or fetch anything.
  const RICH = `## Two pages, briefly

**Bold**, *italic*, \`inline $ sign\`, and a [link](https://example.com/a).

- first
- second, citing [[Chunk 1]]
  - nested under it

1. one
2. two

| where | value |
| --- | ---: |
| page two | 4.2 K [[Chunk 1]] |
| page one | $5 and $10 |

> quoted from page two

\`\`\`go
func main() { fmt.Println("measured") }

const measured = "4.2 K, measured twice, with a citation that keeps on going"
\`\`\`

Inline math $E=mc^2$ and display math:

$$\\frac{\\Delta S}{k_B} = \\ln W$$

![a figure that will not load](https://example.com/fig.png)

<img src="https://example.com/probe.png" onerror="window.__xss = 1"><script>window.__xss2 = 1<\/script>

An invented [[Chunk 99]] and a malformed [[chunk 3]] label.`;

  await control({ reset: true, answer: RICH });
  await send(ui, 'Summarise it with a table');
  await ui.settle(60);
  await ask(ui);

  // Rendered as it arrives, not only once the server has the finished row: a body that
  // appears as plain text and then reflows at the end is the same bug as no markdown.
  const live = await ui.until(`
    const md = document.querySelector('.turn.live .answer .md');
    return !!md?.textContent.length;
    `, 200, 5);
  checks.check('the streaming answer is markdown too', live === true, () => `live=${live}`);

  const done = await ui.until(
    `
    const turn = [...document.querySelectorAll('.turn.assistant')].at(-1);
    if (!turn || turn.classList.contains('live')) return null;
    const answer = turn.querySelector('.answer');
    const md = answer?.querySelector('.md');
    if (!md) return null;
    const cell = (sel) => md.querySelector(sel);
    return {
      text: answer.textContent.replace(/\\s+/g, ' ').trim(),
      h2: !!cell('h2'),
      strong: !!cell('strong'),
      em: !!cell('em'),
      codeInline: [...md.querySelectorAll('p code, li code')].map((c) => c.textContent),
      list: md.querySelectorAll('ul > li').length,
      nested: !!cell('ul ul li'),
      ordered: !!cell('ol li'),
      th: md.querySelectorAll('table th').length,
      scroller: !!cell('.table-wrap') || !!md.querySelector('table')?.closest('.table-wrap'),
      overflow: getComputedStyle(md.querySelector('.table-wrap') ?? md).overflowX,
      // align survives the sanitizer only because markdown.ts tells DOMPurify it is not an
      // address; every other allowed attribute's value is run through the URI regexp.
      align: [...(md.querySelector('tr')?.children ?? [])].map((c) => getComputedStyle(c).textAlign),
      // A bar is only honest when something genuinely cannot fit. Table cells rearrange to
      // the width the column has, so at this width the answer's table needs no bar.
      tableBar: (() => {
        const w = md.querySelector('.table-wrap') ?? md.querySelector('table');
        const t = w.querySelector('table');
        return {
          over: Math.round(w.scrollWidth - w.clientWidth),
          chat: Math.round(document.querySelector('.chat').getBoundingClientRect().width),
          wrap: w.clientWidth,
          table: Math.round(t.getBoundingClientRect().width),
          rows: [...t.querySelectorAll('tr')].map((r) =>
            [...r.children].map((c) => c.clientWidth + ':' + c.textContent.trim().slice(0, 10))
          ),
        };
      })(),
      // And the bar that is left is styled chrome, not the operating system's.
      bar: (() => {
        const scrollers = [...document.querySelectorAll('*')].filter((n) =>
          /auto|scroll/.test(getComputedStyle(n).overflowY)
        );
        const scroller = scrollers.sort(
          (a, b) => b.scrollHeight - b.clientHeight - (a.scrollHeight - a.clientHeight)
        )[0];
        const cs = getComputedStyle(scroller ?? document.documentElement);
        return { w: cs.scrollbarWidth, c: cs.scrollbarColor, sel: scroller ? scroller.className.split(' ')[0] : 'root' };
      })(),
      quote: !!cell('blockquote'),
      lang: cell('pre code')?.dataset.lang ?? '',
      tokens: md.querySelectorAll('pre .hljs-keyword, pre .hljs-title').length,
      math: md.querySelectorAll('.katex').length,
      dollars: [...md.querySelectorAll('code')].some((c) => c.textContent.includes('$')),
      images: md.querySelectorAll('img').length,
      imageLink: !!cell('a.image'),
      scripts: md.querySelectorAll('script').length,
      link: cell('a[href="https://example.com/a"]')?.target ?? '',
      rel: cell('a[href="https://example.com/a"]')?.rel ?? '',
      inline: [...answer.querySelectorAll('.answer .inline')].map((b) => b.textContent.trim()),
      widthFits: answer.getBoundingClientRect().width <=
        document.querySelector('.chat').getBoundingClientRect().width + 1,
      xss: [window.__xss, window.__xss2].filter((v) => v !== undefined).length,
      // Anything the page actually tried to fetch that is not its own origin. A blocked
      // image never appears here at all, because no element ever asked for it.
      remote: performance
        .getEntriesByType('resource')
        .map((e) => e.name)
        .filter((n) => /^https?:/.test(n) && !n.startsWith(location.origin))
    };
    `,
    400,
    100
  );

  checks.check('the answer is a document now, not a paragraph', !!done?.h2 && !!done?.strong && !!done?.em, () => JSON.stringify({ h2: done?.h2, strong: done?.strong }));
  checks.check('a heading is a heading', done?.h2 === true, () => `h2=${done?.h2}`);
  checks.check('lists nest and ordered lists count', (done?.list ?? 0) >= 3 && done?.nested === true && done?.ordered === true, () => `list=${done?.list} nested=${done?.nested} ol=${done?.ordered}`);
  checks.check('a table is a table, in something that can scroll', done?.th === 2 && done?.scroller === true && done?.overflow === 'auto', () => `th=${done?.th} wrap=${done?.scroller} overflow=${done?.overflow}`);
  // The writer's `|---:|` is the only alignment in the grammar, and it is the one thing
  // that can be deleted between the parser and the page without anything else noticing.
  checks.check('and the column it aligned stays aligned', (done?.align ?? []).join() === 'left,right', () => `align=${JSON.stringify(done?.align)}`);
  checks.check(
    'a scrollbar is styled chrome, not the operating system\'s',
    done?.bar?.w === 'thin' && done?.bar?.sel === 'messages' && done?.bar?.c === 'rgba(0, 0, 0, 0) rgba(0, 0, 0, 0)',
    () => JSON.stringify(done?.bar)
  );
  checks.check('a quoted line reads as a quote', done?.quote === true, () => `blockquote=${done?.quote}`);
  checks.check('a fence is coloured by the language it names', done?.lang === 'go' && (done?.tokens ?? 0) > 0, () => `lang=${done?.lang} tokens=${done?.tokens}`);
  checks.check('math renders, inline and display', (done?.math ?? 0) >= 2, () => `katex=${done?.math}`);
  checks.check('a $ inside code is still a $', done?.dollars === true, () => `codes=${JSON.stringify(done?.codeInline)}`);
  checks.check('money is not math', /\$5 and \$10/.test(done?.text ?? ''), () => `text=${done?.text?.slice(0, 200)}`);
  checks.check('an image loads nothing and becomes a link', done?.images === 0 && done?.imageLink === true, () => `img=${done?.images} link=${done?.imageLink}`);
  checks.check('a link out says it is going out', done?.link === '_blank' && /noopener/.test(done?.rel ?? ''), () => `target=${done?.link} rel=${done?.rel}`);
  checks.check('the answer cannot reach past its column', done?.widthFits === true, () => `fits=${done?.widthFits}`);
  checks.check(
    'the chips inside it still cite, twice',
    (done?.inline ?? []).length === 2 && (done?.inline ?? []).every((t) => /^\d+$/.test(t)),
    () => `inline=${JSON.stringify(done?.inline)}`
  );
  checks.check(
    'a label with no citation behind it is gone, and a malformed one is left alone',
    !/Chunk 99|\\[\\[Chunk/.test(done?.text ?? '') && /\\[\\[chunk 3\\]\\]/.test(done?.text ?? ''),
    () => `text=${done?.text?.slice(-160)}`
  );

  // The one check here that is about safety rather than shape. Both halves of the payload
  // arrive in the answer body, exactly as a PDF could put them there, and neither the
  // script tag nor the image handler runs — the sanitizer, not the model's manners, is
  // what the answer path rests on.
  checks.check('an answer cannot execute anything', done?.scripts === 0 && done?.xss === 0, () => `scripts=${done?.scripts} ran=${done?.xss}`);
  checks.check('and cannot make a request of its own', (done?.remote ?? ['?']).length === 0, () => `remote=${JSON.stringify(done?.remote)}`);

  // What the renderer owns, at the narrowest width the layout claims: the body it built
  // must not exceed the thread, and the three things in it that can be wider than a
  // reading column — a code fence, a table, a display equation — must scroll inside
  // themselves. It deliberately does not assert that the thread itself has no horizontal
  // overflow at 640px: with two fixed columns either side, the chat is already 12px wide
  // at that width before any markdown exists, and that is AGENTS.md's known sharp edge,
  // not this renderer's.
  for (const width of [640, 900, 1180]) {
    await ui.setViewport(width);
    const fits = await ui.eval(`
      const s = document.querySelector('.messages');
      const md = [...document.querySelectorAll('.turn.assistant')].at(-1)
        ?.querySelector('.answer .md');
      const pre = md?.querySelector('pre');
      const parts = [pre, md?.querySelector('.table-wrap'), md?.querySelector('.math')]
        .filter(Boolean);
      const head = md?.querySelector('th');
      const range = head ? new Range() : null;
      if (range) range.selectNodeContents(head);
      return {
        // One rect per line box: overflow-wrap: anywhere on the body reached into the
        // cells and let the column shrink to four characters, breaking every word in it.
        headLines: range ? range.getClientRects().length : 0,
        tableBar: (() => {
          const w = md?.querySelector('.table-wrap') ?? md;
          const t = w?.querySelector('table');
          return {
            over: Math.round((w?.scrollWidth ?? 0) - (w?.clientWidth ?? 0)),
            chat: Math.round(document.querySelector('.chat')?.getBoundingClientRect().width ?? -1),
            cols: t ? [...(t.querySelectorAll('tr')?.[0]?.children ?? [])].map((c) => c.clientWidth) : [],
          };
        })(),
        bar: (() => {
          const el = document.querySelector('.messages');
          return el ? getComputedStyle(el).scrollbarWidth + '/' + getComputedStyle(el).scrollbarColor : '';
        })(),
        body: Math.round(md?.getBoundingClientRect().width ?? 0),
        client: s.clientWidth,
        fits: (md?.getBoundingClientRect().width ?? 1e9) <= s.clientWidth + 1,
        scrollers: parts.map((el) => getComputedStyle(el).overflowX),
        codeScrolls: pre ? pre.scrollWidth > pre.clientWidth + 1 : null,
        parts: parts.length
      };
    `);
    await ui.clearViewport();
    checks.check(
      `the answer fits its column at ${width}px`,
      fits?.fits === true,
      () => `body=${fits?.body} client=${fits?.client}`
    );
    // Only where there is a chat to measure: at 640px the two fixed columns have already
    // taken the middle one away, which is AGENTS.md's sharp edge and not this renderer's.
    if (width >= 900) {
      checks.check(
        `no word inside the table is broken at ${width}px`,
        fits?.headLines === 1,
        () => `headLines=${fits?.headLines}`
      );
    }
    // The scroller is a last resort, so what gets asserted is that it stays unused: at any
    // width where the chat has room, nothing in the answer scrolls sideways.
    if (width >= 1180) {
      checks.check(
        `the table needs no bar of its own at ${width}px`,
        (fits?.tableBar?.over ?? 99) <= 1,
        () => JSON.stringify({ ...fits?.tableBar, bar: fits?.bar })
      );
    }
    checks.check(
      `whatever still cannot fit stays inside the answer at ${width}px`,
      fits?.parts === 3 &&
        (fits?.scrollers ?? []).every((o) => o === 'auto') &&
        fits?.codeScrolls === true,
      () => `scrollers=${JSON.stringify(fits?.scrollers)} codeScrolls=${fits?.codeScrolls}`
    );
  }

  // Stop in the middle of an equation and what is stored is not valid markdown. Two
  // different failures, and the honest pair of expectations: an *unclosed* `$$` is not
  // drawn at all — the scanner will not guess where it ends, so the reader keeps the text
  // the model wrote — while a closed-but-broken `$…$` is drawn in red, because that one
  // was a complete thought the model got wrong. Neither takes the prose with it.
  const half = [
    'It begins normally.',
    '',
    '$$\\frac{1}{2',
    '',
    'A closed one that is broken: $\\begin{aligned}$.',
    '',
    '```go',
    'func half(',
    ''
  ].join('\n');
  await control({ reset: true, answer: half });
  const broken = await askAndRead(ui, 'Stop halfway through the maths');
  checks.check(
    'an answer that ends mid-expression keeps its prose',
    /It begins normally/.test(broken?.text ?? ''),
    () => `text=${broken?.text}`
  );
  checks.check(
    'an unclosed $$ stays the text it is',
    (broken?.text ?? '').includes(String.raw`$$\frac{1}{2`),
    () => `text=${broken?.text}`
  );
  const drawn = await ui.eval(`
    const md = [...document.querySelectorAll('.turn.assistant')].at(-1)?.querySelector('.answer .md');
    return {
      // The .math slot is what our scanner makes, so this counts the expressions it
      // agreed were expressions. A KaTeX error span is not wrapped in .katex.
      slots: md?.querySelectorAll('.math').length ?? -1,
      errors: md?.querySelectorAll('.katex-error').length ?? -1,
      pre: md?.querySelectorAll('pre').length ?? -1
    };
  `);
  checks.check('only the expression that closed became math', drawn?.slots === 1, () => `slots=${drawn?.slots}`);
  checks.check('and a broken one is drawn as itself, in red', drawn?.errors === 1, () => `katex-error=${drawn?.errors}`);
  checks.check('an unclosed fence is still a code block', drawn?.pre === 1, () => `pre=${drawn?.pre}`);

  await control({ reset: true });

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

  // ── the thinking switch, from the row in Settings to the request body ──
  // Off by default and proven on the wire above. This is the other half: that the row
  // Settings generates is clickable, that the save lands on the server, and that the next
  // turn's research call carries what was clicked. The answer call stays silent throughout.
  await control({ reset: true });
  await ui.eval(
    `const b = [...document.querySelectorAll('button')].find((x) => x.title === 'Settings'); if (b) b.click(); return !!b;`
  );
  await ui.settle(400);

  const thinkRow = `
    const row = [...document.querySelectorAll('.row')].find((r) =>
      r.querySelector('.label span')?.textContent.trim() === 'Think before searching');
    const sw = row?.querySelector('button.switch');
    return sw
      ? { checked: sw.getAttribute('aria-checked'), label: sw.getAttribute('aria-label') }
      : null;
  `;
  const offRow = await ui.until(thinkRow);
  checks.check(
    'Settings renders the thinking switch, off',
    offRow?.checked === 'false' && offRow?.label === 'Think before searching',
    () => JSON.stringify(offRow)
  );

  await ui.eval(`
    const row = [...document.querySelectorAll('.row')].find((r) =>
      r.querySelector('.label span')?.textContent.trim() === 'Think before searching');
    row.querySelector('button.switch').click();
    return true;
  `);
  await ui.settle(200);
  const flipped = await ui.eval(`
    const sw = [...document.querySelectorAll('button.switch')]
      .find((s) => s.getAttribute('aria-label') === 'Think before searching');
    const save = [...document.querySelectorAll('.panels button')]
      .find((b) => b.textContent.trim() === 'Save changes');
    return { checked: sw?.getAttribute('aria-checked'), saveDisabled: save?.disabled === true };
  `);
  checks.check(
    'the click flips it and marks the page unsaved',
    flipped.checked === 'true' && flipped.saveDisabled === false,
    () => JSON.stringify(flipped)
  );

  await ui.click('Save changes');
  const savedAgain = await ui.until(`
    const save = [...document.querySelectorAll('.panels button')]
      .find((b) => b.textContent.trim() === 'Save changes');
    return save && save.disabled ? { clean: true } : null;
  `);
  checks.check('and it saves', savedAgain?.clean === true, () => JSON.stringify(savedAgain));

  const savedOnServer = await fetch(`${api}/api/settings`).then((r) => r.json());
  checks.check(
    'the server kept the new value',
    savedOnServer.researchThinking === true,
    () => JSON.stringify(savedOnServer).slice(0, 160)
  );

  // Reloaded rather than navigated back: it is what a person who just changed a setting
  // does next, and it lets the row be read again from the server's copy rather than from
  // the click that produced it.
  await ui.reload(app);
  await ui.eval(
    `const b = [...document.querySelectorAll('button')].find((x) => x.title === 'Settings'); if (b) b.click(); return !!b;`
  );
  const onRow = await ui.until(thinkRow);
  checks.check(
    'after a reload the row is drawn from what was saved, not from the click',
    onRow?.checked === 'true',
    () => JSON.stringify(onRow)
  );

  await ui.reload(app);
  checks.check(
    'the notebook is back after the detour',
    (await openNotebook(ui, name)).ok
  );
  const withThinking = await askAndRead(ui, 'What does the paper measure now?');
  checks.check('that turn answered', !!withThinking?.text, () => JSON.stringify(withThinking));
  const afterSwitch = await stats();
  checks.check(
    'with the switch on, the research call says enable_thinking true',
    afterSwitch.lastResearchKwargs?.enable_thinking === true,
    () => JSON.stringify(afterSwitch.lastResearchKwargs)
  );
  checks.check(
    'the answer call still says nothing about thinking',
    afterSwitch.lastAnswerKwargs === null,
    () => JSON.stringify(afterSwitch.lastAnswerKwargs)
  );

  // `make ui` runs every harness against one server and one data directory, so the switch
  // is put back the way it was found rather than left on for whoever runs next.
  await ui.eval(
    `const b = [...document.querySelectorAll('button')].find((x) => x.title === 'Settings'); if (b) b.click(); return !!b;`
  );
  await ui.settle(400);
  await ui.eval(`
    const sw = [...document.querySelectorAll('button.switch')]
      .find((s) => s.getAttribute('aria-label') === 'Think before searching');
    if (!sw) throw new Error('the thinking switch is gone');
    if (sw.getAttribute('aria-checked') === 'true') sw.click();
    return true;
  `);
  await ui.settle(200);
  const backOff = await ui.until(`
    const sw = [...document.querySelectorAll('button.switch')]
      .find((s) => s.getAttribute('aria-label') === 'Think before searching');
    return sw ? { checked: sw.getAttribute('aria-checked') } : null;
  `);
  await ui.click('Save changes');
  const cleanAgain = await ui.until(`
    const save = [...document.querySelectorAll('.panels button')]
      .find((b) => b.textContent.trim() === 'Save changes');
    return save && save.disabled ? { clean: true } : null;
  `);
  checks.check(
    'the switch is saved back off, as the run found it',
    backOff?.checked === 'false' && cleanAgain?.clean === true,
    () => JSON.stringify({ ...backOff, ...cleanAgain })
  );

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
