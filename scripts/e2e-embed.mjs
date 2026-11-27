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
const AUDIO = 'scripts/testdata/one-second.wav';
const VIDEO = 'scripts/testdata/four-second.mp4';

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
    // External mode: the fake answers at these URLs. Managed mode would derive both the
    // address and the model names from a router libris runs itself (ADR 0020).
    engineMode: 'external',
    chatURL: embedURL, // required by the settings schema; unused by this slice
    embedURL,
    embedModel: 'fake-embed-2'
  })
);

const port = 8123;
const base = `http://127.0.0.1:${port}`;

/** One window of a source's stored chunks. The endpoint answers with a window plus what the
 *  file holds of each kind — a viewer asks for a screen of a source that may hold thousands —
 *  and these assertions want every chunk, so they ask for the largest window there is. */
const storedChunks = async (id) =>
  ((await (await fetch(`${base}/api/sources/${id}/segments?limit=2000`)).json())?.segments) ?? [];
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

  const stored = await storedChunks(first.id);
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

  // ── a recording ───────────────────────────────────────────────
  // The same path with a modality that has no words anywhere on it: ffmpeg cuts it into
  // windows, the embedder is handed sound, and what comes back is a row that cites a range
  // of seconds and a file a browser will play. Every one of those is a place the pipeline
  // could quietly decide there was nothing to do, because nothing it does with a clip has a
  // string in it.
  const wav = readFileSync(AUDIO);
  const recording = await upload(base, nb.id, 'standup.wav', wav, 'audio/wav');
  const sliced = await poll(`${base}/api/sources/${recording.id}`, (x) => x.status !== 'processing');
  checks.check(
    'a recording with no text in it reaches ready',
    sliced.status === 'ready',
    `status=${sliced.status} detail=${sliced.detail ?? ''}`
  );

  const windows = await storedChunks(recording.id);
  checks.check('and was cut into clips rather than read as text', windows.length > 0, `${windows.length} segments`);
  checks.check(
    'every clip is audio, with a window and a file',
    windows.length > 0 &&
      windows.every((w) => w.kind === 'audio' && w.asset && w.span?.t0 !== undefined && w.span?.t1 > w.span?.t0),
    windows.map((w) => `${w.kind}@${w.span?.t0}-${w.span?.t1}`).join(' ')
  );
  checks.check(
    'clips are numbered from one, without holes',
    windows.every((w, i) => w.asset?.id?.endsWith(`/clip-${String(i + 1).padStart(4, '0')}.wav`)),
    windows.map((w) => w.asset?.id).join(' ')
  );

  // The embedder was handed sound, not a path and not a transcript of something it cannot
  // hear. A clip sent as text would still get a vector, and the source would still say
  // ready, so nothing else in the path can see this go wrong.
  const stats = await (await fetch(`${embedURL}/stats`)).json();
  checks.check(
    'the clips reached the embedding model as audio',
    (stats.inputsByType?.input_audio ?? 0) >= windows.length,
    `input_audio=${stats.inputsByType?.input_audio ?? 0}, ${windows.length} clips stored`
  );

  // And the file behind the citation is the file: the route the player in the viewer points
  // at, answering with bytes a decoder accepts.
  const clipURL = `${base}/api/assets/${windows[0].asset.id.split('/').map(encodeURIComponent).join('/')}`;
  const served = await fetch(clipURL);
  const bytes = Buffer.from(await served.arrayBuffer());
  checks.check(
    'a clip is served as audio, and is a WAV',
    served.status === 200 &&
      (served.headers.get('content-type') ?? '').startsWith('audio/wav') &&
      bytes.subarray(0, 4).toString() === 'RIFF' &&
      bytes.subarray(8, 12).toString() === 'WAVE',
    `status=${served.status} type=${served.headers.get('content-type')} bytes=${bytes.length}`
  );

  // ── a video ───────────────────────────────────────────────────
  // Two things at once: pictures that run, and a soundtrack nobody said out loud. One file
  // goes in and comes back as two documents with the same windows, because a search that has
  // to choose between a frame and a sentence from the same second has already lost the
  // answer. Both halves are checked because either can go missing on its own — the frames
  // stored and the sound dropped, or the sound stored and the pictures sent as a path.
  const mov = await upload(base, nb.id, 'trip.mp4', readFileSync(VIDEO), 'video/mp4');
  const cut = await poll(`${base}/api/sources/${mov.id}`, (x) => x.status !== 'processing');
  checks.check(
    'a video reaches ready, with its pictures and its soundtrack both stored',
    cut.status === 'ready',
    `status=${cut.status} detail=${cut.detail ?? ''}`
  );

  const halves = await storedChunks(mov.id);
  const frames = halves.filter((s) => s.kind === 'video');
  const sound = halves.filter((s) => s.kind === 'audio');
  checks.check(
    'it was cut into clips of the pictures and clips of the soundtrack',
    frames.length > 0 && sound.length > 0,
    `${frames.length} video + ${sound.length} audio of ${halves.length} chunks`
  );
  checks.check(
    'the two halves are separate documents, and the same moment has one window',
    new Set(halves.map((s) => s.doc_id)).size === 2 &&
      frames[0]?.span?.t0 === sound[0]?.span?.t0 &&
      frames[0]?.span?.t1 === sound[0]?.span?.t1,
    halves.map((s) => `${s.kind}:${s.doc_id}@${s.span?.t0}-${s.span?.t1}`).join(' ')
  );

  // Each half reached the model as the file it is. A frame sent as a path, or a soundtrack
  // sent as a guess at what was said, still gets a vector of the right length — every count
  // in the pipeline stays green while the thing being searched for stops being the video.
  const moved = await (await fetch(`${embedURL}/stats`)).json();
  checks.check(
    'the clips reached the embedding model as video and as audio',
    (moved.inputsByType?.input_video ?? 0) >= frames.length &&
      (moved.inputsByType?.input_audio ?? 0) >= sound.length,
    `input_video=${moved.inputsByType?.input_video ?? 0}/${frames.length} input_audio=${moved.inputsByType?.input_audio ?? 0}/${sound.length}`
  );

  // And the file behind a citation of a moment is the moment: a clip of the pictures, which
  // is a container a player opens rather than a stream a browser downloads.
  const frameURL = `${base}/api/assets/${frames[0].asset.id.split('/').map(encodeURIComponent).join('/')}`;
  const servedClip = await fetch(frameURL);
  const clipBytes = Buffer.from(await servedClip.arrayBuffer());
  checks.check(
    'a clip of the pictures is an mp4, and the player page is a video element',
    servedClip.status === 200 &&
      (servedClip.headers.get('content-type') ?? '').startsWith('video/mp4') &&
      clipBytes.subarray(4, 8).toString() === 'ftyp' &&
      /<video /.test(await (await fetch(`${base}/api/sources/${mov.id}/player`)).text()),
    `status=${servedClip.status} type=${servedClip.headers.get('content-type')} bytes=${clipBytes.length}`
  );

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
  const segs = await storedChunks(second.id);
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
  // A file another notebook still uses survives a deletion, and nothing does when the
  // last one lets go. Asserted on the filesystem, because "the rows went" is not what
  // anyone cares about.
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
