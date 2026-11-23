// End-to-end embedding pipeline against a real HTTP server.
//
// The unit tests pin each layer's shape. What they cannot pin is the wiring in
// cmd/libris: that the settings a user saves actually reach the embedder, that
// the probed dimension lands back in the settings the UI shows, and that a source
// whose server vanished is reported rather than silently searchable. This drives
// the real binary against a fake embeddings server that speaks the OpenAI shape.
//
//	node scripts/e2e-embed.mjs            # builds nothing; needs ./libris
// or  make e2e
import { execSync, spawn } from 'node:child_process';
import { mkdtempSync, openSync, readFileSync, existsSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { DIM, EmbedServer } from './fake-embed.mjs';

const BIN = process.env.LIBRIS ?? './libris';
const PDF = 'scripts/testdata/two-page.pdf';

// ── checks ──────────────────────────────────────────────────────

class Checks {
  constructor() {
    this.failed = 0;
    this.passed = 0;
  }

  check(name, ok, detail = '') {
    if (ok) {
      this.passed++;
      console.log(`  ok   ${name}${detail ? ` — ${detail}` : ''}`);
    } else {
      this.failed++;
      console.log(`  FAIL ${name}${detail ? ` — ${detail}` : ''}`);
    }
  }
}

const checks = new Checks();

// ── helpers ─────────────────────────────────────────────────────

async function poll(url, until, { timeout = 90_000, every = 400 } = {}) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await (await fetch(url)).json();
    if (until(last)) return last;
    await new Promise((r) => setTimeout(r, every));
  }
  return last;
}

async function upload(base, notebookID, filename, bytes, type) {
  const form = new FormData();
  form.append('file', new Blob([bytes], { type }), filename);
  const res = await fetch(`${base}/api/notebooks/${notebookID}/sources`, {
    method: 'POST',
    body: form
  });
  if (res.status !== 202 && res.status !== 200) {
    throw new Error(`upload ${filename}: ${res.status} ${await res.text()}`);
  }
  return res.json();
}

// Read the database the way an operator would, rather than trusting the API's
// own claim about it: a status of `ready` has to be backed by rows.
function sqlite(dbPath, sql) {
  return execSync(`sqlite3 ${JSON.stringify(dbPath)} ${JSON.stringify(sql)}`, {
    encoding: 'utf8'
  }).trim();
}

// ── run ─────────────────────────────────────────────────────────

if (!existsSync(BIN)) {
  console.error(`no ${BIN} — run \`make build\` first`);
  process.exit(2);
}

const DIM2 = 512; // a second server, a different length, a different model

const dataDir = mkdtempSync(join(tmpdir(), 'libris-e2e-'));
const embed = new EmbedServer();
let embed2 = null;
const embedURL = await embed.listen();

// The settings a user would type in the UI. Written before the server starts,
// because the point is that a saved file drives the pipeline.
writeFileSync(
  join(dataDir, 'settings.json'),
  JSON.stringify({
    chatURL: embedURL, // required by the settings schema; unused by this slice
    embedURL,
    embedModel: 'fake-embed-2'
  })
);

const port = 8123;
const base = `http://127.0.0.1:${port}`;
const log = openSync('/tmp/libris-e2e.log', 'w');
// -open=false: this is a server under test, not a user who wants to see the UI.
const app = spawn(BIN, ['-open=false', '-addr', `127.0.0.1:${port}`, '-data', dataDir], {
  stdio: ['ignore', 'ignore', log]
});

try {
  // Wait for the app rather than sleeping: it is the same condition a user waits on.
  let up = false;
  for (let i = 0; i < 60 && !up; i++) {
    await new Promise((r) => setTimeout(r, 250));
    try {
      up = (await (await fetch(`${base}/api/health`)).ok);
    } catch {
      /* not listening yet */
    }
  }
  checks.check('libris starts with a saved settings.json', up);

  const nb = await (await fetch(`${base}/api/notebooks`, {
    method: 'POST',
    body: JSON.stringify({ name: 'e2e' })
  })).json();

  // ── the happy path ────────────────────────────────────────────
  const pdf = readFileSync(PDF);
  const first = await upload(base, nb.id, 'two-page.pdf', pdf, 'application/pdf');
  checks.check('upload answers before ingest finishes', first.status === 'processing', first.status);

  const done = await poll(`${base}/api/sources/${first.id}`, (s) => s.status !== 'processing');
  checks.check(
    'a reachable server takes the source to ready',
    done.status === 'ready',
    `status=${done.status} detail=${done.detail ?? ''}`
  );

  // The probe has to have run, and written what it learned back where the UI can
  // show it. This is the round trip only an end-to-end check can see.
  const settings = await (await fetch(`${base}/api/settings`)).json();
  checks.check(
    'the probe records the server’s vector length in settings',
    String(settings.embedDimensions) === String(DIM),
    `embedDimensions=${settings.embedDimensions}`
  );

  // A PDF yields two derived documents, so the request count is the proof both
  // were embedded rather than only the cheap one.
  const textRequests = embed.requests.filter((r) => r.model === 'fake-embed-2').length;
  checks.check('the model in the settings is the model on the wire', textRequests > 0);

  // EmbeddingGemma wants its text inputs steered by the document they came from.
  // The prefix is wire-only, so it shows up here and nowhere in the stored text.
  const docPrefix = 'title: two-page.pdf | text: ';
  const steered = embed.requests.some((r) =>
    r.input.some((e) => typeof e === 'string' && e.startsWith(docPrefix))
  );
  checks.check(
    'text inputs carry the model’s document prompt',
    steered,
    `expected the prefix ${JSON.stringify(docPrefix)}`
  );

  const mediaUnprefixed = embed.requests.every((r) =>
    r.input.every((e) =>
      typeof e === 'string' ? true : (e.content ?? []).every((p) => p.type !== 'text' || !p.text.startsWith('title: '))
    )
  );
  checks.check('media inputs are sent without any prefix', mediaUnprefixed);

  const stored = (await (await fetch(`${base}/api/sources/${first.id}/segments`)).json()) ?? [];
  checks.check(
    'the prompt never reaches the stored text a citation quotes',
    stored.length > 0 && stored.every((s) => !s.text.startsWith('title: ')),
    `${stored.length} segments`
  );

  const everyEntryNamed = embed.requests.every((r) =>
    r.input.every((e) => (typeof e === 'string' ? e.length > 0 : (e.content?.length ?? 0) > 0))
  );
  checks.check('nothing empty was sent to the server', everyEntryNamed);

  const inlineImages = embed.requests.some((r) =>
    r.input.some((e) => typeof e === 'object' && e.content?.some((p) => p.image_url?.url?.startsWith('data:')))
  );
  checks.check('page rasters are embedded as images, not as paths', inlineImages);

  // ── the server is not running ─────────────────────────────────
  embed.down = true;
  const second = await upload(base, nb.id, 'offline.txt', Buffer.from(`${'offline body '.repeat(40)}\n`), 'text/plain');
  const broken = await poll(`${base}/api/sources/${second.id}`, (s) => s.status !== 'processing');
  checks.check(
    'an unreachable server leaves the source failed, never ready',
    broken.status === 'failed',
    `status=${broken.status}`
  );
  checks.check(
    'the failure names the server the user has to fix',
    broken.detail.includes(String(embedURL.split(':').pop())),
    `detail=${broken.detail}`
  );

  // Chunks survive, so the source is still readable and the inspector still works.
  // An empty list serialises as null rather than [], which the UI's own reader
  // tolerates; the assertion has to as well.
  const segs = (await (await fetch(`${base}/api/sources/${second.id}/segments`)).json()) ?? [];
  checks.check('the chunks survive an embedding failure', segs.length > 0, `${segs.length} segments`);

  // ── running the file again ────────────────────────────────────
  const stillBroken = await poll(
    `${base}/api/sources/${second.id}`,
    (s) => s.status !== 'processing',
    { timeout: 1000 }
  );
  checks.check(
    'a failed source stays failed until something changes',
    stillBroken.status === 'failed'
  );

  embed.down = false;
  const ack = await fetch(`${base}/api/sources/${second.id}/reingest`, { method: 'POST' });
  checks.check('running it again answers 202', ack.status === 202, `status=${ack.status}`);

  const fixed = await poll(`${base}/api/sources/${second.id}`, (s) => s.status !== 'processing');
  checks.check(
    're-running the file after the server returns takes it to ready',
    fixed.status === 'ready',
    `status=${fixed.status} detail=${fixed.detail ?? ''}`
  );

  // ── the store agrees with the API ─────────────────────────────
  // The API says ready; the database must say vectors. Reading it directly is the
  // only way to catch a status that is not backed by data.
  const rows = sqlite(join(dataDir, 'libris.db'), 'SELECT COUNT(*) FROM segments WHERE embedding IS NOT NULL');
  const total = sqlite(join(dataDir, 'libris.db'), 'SELECT COUNT(*) FROM segments');
  checks.check(
    'every ready segment carries a vector',
    rows > 0 && rows === total,
    `${rows} of ${total} segments have vectors`
  );

  const dims = sqlite(join(dataDir, 'libris.db'), 'SELECT DISTINCT dim FROM segments WHERE embedding IS NOT NULL');
  checks.check('the store holds one dimension, not a mix', dims.trim() === String(DIM), `dims=${dims.trim()}`);

  const models = sqlite(join(dataDir, 'libris.db'), 'SELECT DISTINCT model FROM segments WHERE embedding IS NOT NULL');
  // The model column identifies the vector space, and the prompt scheme is part of
  // that space: a corpus embedded with prefixes must not read as one embedded
  // without them.
  checks.check(
    'every vector records the model and the prompt scheme that produced it',
    models.trim() === 'fake-embed-2+tp1',
    `model=${models.trim()}`
  );

  // ── the user repoints Settings while libris runs ──────────────────
  // The bug this pins: a saved embedding URL reached the error message but not the
  // socket, so the next upload dialled whatever address libris booted with and the
  // failure text blamed a server that had never been asked. Everything below is
  // only true if the setting moved the connection.
  embed2 = new EmbedServer(DIM2);
  const embed2URL = await embed2.listen();

  const saved = await fetch(`${base}/api/settings`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ embedURL: embed2URL, embedModel: 'fake-embed-b' })
  });
  checks.check('the settings API takes a new embedding server', saved.ok, `status=${saved.status}`);

  const askedBefore = embed.requests.length;
  const third = await upload(
    base, nb.id, 'repoint.txt', Buffer.from(`${'repointed body '.repeat(40)}\n`), 'text/plain'
  );
  const repointed = await poll(`${base}/api/sources/${third.id}`, (s) => s.status !== 'processing');
  checks.check(
    'the next upload embeds against the server the settings now name',
    repointed.status === 'ready',
    `status=${repointed.status} detail=${repointed.detail ?? ''}`
  );
  checks.check(
    'the new server is the one that was asked',
    embed2.requests.length > 0 && embed2.requests.every((r) => r.model === 'fake-embed-b'),
    `${embed2.requests.length} requests to the new server`
  );
  checks.check(
    'the old server got nothing after the change',
    embed.requests.length === askedBefore,
    `${embed.requests.length - askedBefore} extra requests to the old one`
  );

  // A new server gets probed on its own merits, and what the UI shows follows it.
  const shown = await (await fetch(`${base}/api/settings`)).json();
  checks.check(
    'the new server is probed and its length replaces the one shown',
    String(shown.embedDimensions) === String(DIM2),
    `embedDimensions=${shown.embedDimensions}`
  );

  // And the vectors land in the store under the new length and model, while the
  // sources embedded earlier keep theirs — the store is allowed to hold both.
  const newDims = sqlite(join(dataDir, 'libris.db'), `SELECT DISTINCT dim FROM segments WHERE source_id = '${third.id}'`);
  checks.check('the new source’s vectors carry the new length', newDims.trim() === String(DIM2), `dims=${newDims.trim()}`);

  const newModels = sqlite(join(dataDir, 'libris.db'), `SELECT DISTINCT model FROM segments WHERE source_id = '${third.id}'`);
  checks.check('and the model and prompt scheme that produced them', newModels.trim() === 'fake-embed-b+tp1', `model=${newModels.trim()}`);

  const oldDims = sqlite(join(dataDir, 'libris.db'), `SELECT DISTINCT dim FROM segments WHERE source_id != '${third.id}'`);
  checks.check('earlier sources keep the length they were embedded at', oldDims.trim() === String(DIM), `dims=${oldDims.trim()}`);
  // ── what a delete actually takes ───────────────────────────────
  // The confirmation dialog promises that a file another notebook uses survives,
  // and that nothing does when the last one lets go. That is asserted on the
  // filesystem, because "the rows went" is not the promise anyone cares about.
  {
    const mk = async (name) =>
      await (
        await fetch(`${base}/api/notebooks`, {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ name })
        })
      ).json();

    const shared = Buffer.from(`shared bytes for the collector\n`.repeat(80));
    const doomed = await mk('GC first');
    const keep = await mk('GC keeper');
    const first = await upload(base, doomed.id, 'shared.txt', shared, 'text/plain');
    const again = await upload(base, keep.id, 'shared.txt', shared, 'text/plain');
    checks.check('the same file in a second notebook is one source', first.id === again.id, `${first.id} vs ${again.id}`);

    const ready = await poll(`${base}/api/sources/${first.id}`, (src) => src.status === 'ready');
    checks.check('the shared source is ready before anything is deleted', ready.status === 'ready', `status=${ready.status}`);

    const original = join(dataDir, 'originals', `${first.id}.txt`);
    const exists = (p) => existsSync(p);
    checks.check('the kept original is on disk', exists(original), original);

    const keptNotebook = sqlite(join(dataDir, 'libris.db'), `SELECT COUNT(*) FROM notebooks WHERE id = '${keep.id}'`);
    checks.check('the keeper notebook exists', keptNotebook.trim() === '1', keptNotebook.trim());
    const firstNotebook = doomed.id;
    checks.check(
      'the file has two notebooks using it',
      sqlite(join(dataDir, 'libris.db'), `SELECT COUNT(*) FROM notebook_sources WHERE source_id = '${first.id}'`).trim() === '2'
    );

    await fetch(`${base}/api/notebooks/${firstNotebook}`, { method: 'DELETE' });
    checks.check(
      'a shared file survives the deletion of one of its notebooks',
      exists(original) &&
        sqlite(join(dataDir, 'libris.db'), `SELECT COUNT(*) FROM sources WHERE id = '${first.id}'`).trim() === '1'
    );

    await fetch(`${base}/api/notebooks/${keep.id}/sources/${first.id}`, { method: 'DELETE' });
    checks.check(
      'the last notebook letting go deletes the file it was keeping',
      !exists(original),
      original
    );
    checks.check(
      'and with it the rows that cited it',
      sqlite(join(dataDir, 'libris.db'), `SELECT COUNT(*) FROM sources WHERE id = '${first.id}'`).trim() === '0' &&
        sqlite(join(dataDir, 'libris.db'), `SELECT COUNT(*) FROM segments WHERE source_id = '${first.id}'`).trim() === '0'
    );
    checks.check(
      'the source’s asset directory is gone too, rasters and all',
      !exists(join(dataDir, 'assets', first.id))
    );
  }
} finally {
  app.kill('SIGTERM');
  embed.close();
  embed2?.close();
}

console.log(`\n${checks.passed} passed, ${checks.failed} failed`);
console.log(`data: ${dataDir} · log: /tmp/libris-e2e.log`);
process.exit(checks.failed ? 1 : 0);
