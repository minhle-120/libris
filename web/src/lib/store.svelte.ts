// The app's state, in one place.
//
// Two rules run through all of it, because both were got wrong twice before:
//
//   * Nothing here is authoritative. Every field is the server's answer rendered as it
//     arrives — no optimistic row, no client-side "ready", no cached copy of a fact the
//     backend owns (ADR 0005).
//   * Jobs are polled; requests are not. A timer runs only while something is in flight, and
//     never re-issues a request to learn what an earlier one already answered.
import * as api from './api';
import { defaultSettings, type Settings } from './config.gen';
import { errorMessage } from './format';
import {
  NOTEBOOKS,
  SIDE,
  clampW,
  narrowRule,
  readOpen,
  readW,
  writeOpen,
  type PanelSpec
} from './panel';
import type {
  Chat,
  Citation,
  Message,
  Notebook,
  Panel,
  Segment,
  SetupView,
  Source,
  ToolsReport
} from './types';

export type Live = {
  question: string;
  status: string;
  reasoning: string;
  /** The disclosure holding the thinking, open while it is still arriving. The caller of the
   *  disclosure could hold this itself, but then the close would live in the view — and the
   *  thing that knows a sentence has begun is the stream, three levels away. */
  thinkingOpen: boolean;
  answer: string;
  citations: Citation[];
  /** The stream is open. Off once the row comes down, so the caret stops. */
  streaming: boolean;
};

export const app = $state({
  /** The gate: the server's own answer about whether this machine can answer yet. */
  gated: false,
  /** Null until the first read; a screen must assert nothing before an answer arrives. */
  ready: null as boolean | null,

  view: 'notebook' as 'notebook' | 'settings',
  /** Where the gear leaves settings from: the notebook the reader came out of. */
  crumbNotebook: null as string | null,
  error: '',

  notebooks: [] as Notebook[],
  notebookId: null as string | null,
  chats: [] as Chat[],
  chatId: null as string | null,
  thread: '',

  sources: [] as Source[],
  loading: true,

  /** What this machine provides to read a file, and whether the download brought it. Null
   *  until asked, and a row blaming a missing program waits for the answer rather than
   *  guessing: "install it" and "the folder came with libris" describe two machines. */
  tools: null as ToolsReport | null,

  messages: [] as Message[],
  messagesLoading: false,
  live: null as Live | null,
  /** Half-typed questions, kept per conversation so a rename or a switch does not eat one. */
  drafts: {} as Record<string, string>,

  /** The one new-notebook dialog. Two of them would mean two answers to `dialog.new-notebook`. */
  newNotebook: false,

  panel: 'sources' as Panel,
  /** The source whose chunks are open in the panel, and the chunk a citation pointed at. */
  viewer: null as string | null,
  viewerSeg: '',
  viewerError: '',
  /** The chunks of the source the viewer has open. They arrive a fetch after the viewer does,
   *  so the heading renders from `sources` and the list fills in behind it. */
  viewerSegments: [] as Segment[],
  /** What the file holds of each kind, as the server counted it. A long source's chunks reach
   *  the page a window at a time, and a heading that counted the window would tell a reader
   *  halfway down a nine-hundred-page PDF that the file contains no pictures. */
  viewerKinds: {} as Record<string, number>,
  /** A later window is on its way. The scroll handler runs on every tick of the wheel, and
   *  one answer in flight is all the source needs. */
  viewerLoading: false,
  /** The server answered with nothing new. Its count said there was more — a source being
   *  indexed answers with a number that was true a moment ago — and asking again would ask
   *  forever, so the end of the chunks is believed when it is seen. */
  viewerEnd: false,

  sidebarW: 0,
  sidebarOpen: true,
  sideW: 0,
  sideOpen: true,

  theme: 'light' as 'light' | 'dark',

  /** The settings exactly as the server has them, plus whatever has been typed since. */
  settings: defaultSettings() as Settings,
  settingsLoading: false,
  settingsSaving: false,
  settingsError: ''
});

// ── settings ──────────────────────────────────────────────────────

/** The settings as they were when the server last answered. Comparing JSON rather than
 *  diffing fields is the whole point: a setting is anything in this record, and a record
 *  grows without this line changing. */
let pristine = $state('');

// Returned by a function rather than exported as `$derived`: a module may not export derived state,
// and reading `app.settings` here is what makes the caller re-run.
export function settingsDirty() {
  return JSON.stringify(app.settings) !== pristine;
}

export async function loadSettings() {
  app.settingsLoading = true;
  app.settingsError = '';
  try {
    const loaded = await api.getSettings();
    app.settings = loaded;
    pristine = JSON.stringify(loaded);
  } catch (e) {
    app.settingsError = errorMessage(e);
  } finally {
    app.settingsLoading = false;
  }
}

/**
 * Saving takes the server's answer as the new truth rather than keeping what was sent.
 * Validation and defaults live on the server; the page that just wrote a blank port should
 * show the port the server decided on, not the blank it typed.
 */
export async function saveSettings(): Promise<boolean> {
  app.settingsSaving = true;
  app.settingsError = '';
  try {
    const saved = await api.putSettings(app.settings);
    app.settings = saved;
    pristine = JSON.stringify(saved);
    return true;
  } catch (e) {
    app.settingsError = errorMessage(e);
    return false;
  } finally {
    app.settingsSaving = false;
  }
}

// ── boot ──────────────────────────────────────────────────────────

export function initPanels() {
  app.sidebarW = readW(NOTEBOOKS);
  app.sideW = readW(SIDE);
  app.sidebarOpen = readOpen(NOTEBOOKS);
  app.sideOpen = readOpen(SIDE);
}

/** The tab's own "not yet" for the setup wizard.
 *
 *  It lives in the browser and not in a setting because it changes nothing about the
 *  machine: no server is configured, nothing is started, nothing is promised. The gate
 *  stays the server's rule — this only steps aside from it for as long as the tab is
 *  open, so reloading during a look-around does not put the wizard back in front of
 *  the thing being looked at, and the next opening asks again. */
const SETUP_SKIPPED = 'libris.setupSkipped';

function setupSkipped(): boolean {
  try {
    return sessionStorage.getItem(SETUP_SKIPPED) === '1';
  } catch {
    return false;
  }
}

/** Walks past the wizard into an app that cannot answer a question yet.
 *
 *  Deliberately does the bare minimum: the shell reads the settings and the sources it
 *  can read, and asking anything fails with the server's own reason. The way back to
 *  the setup screen is Settings → Servers, which is where the same choices live. */
export function skipSetup() {
  try {
    sessionStorage.setItem(SETUP_SKIPPED, '1');
  } catch {
    // A browser that refuses storage loses only the reload-proof, not the skip.
  }
  app.gated = false;
  void enterApp();
}

/** Walking into the app the gate cut short.
 *
 *  boot() stops at the gate, so a machine that opens on the wizard never runs the tail that
 *  reads the notebooks and ends the loading page. When the last step lands, the shell mounts
 *  onto that half-finished boot and holds "Loading…" until the person reloads the page — the
 *  wizard is gone and nothing answers for it. Both ways out of the wizard come through here,
 *  the last step landing and walking past it, so neither can leave the shell waiting.
 *
 *  Not the newest-notebook open a cold boot does: whoever leaves the wizard is either at a
 *  machine with no notebooks yet or at one they just chose to skip, and Landing is where both
 *  belong. Idempotent, because the poll that sees the gate open can answer again before the
 *  wizard is off the screen. */
let entered = false;
async function enterApp(): Promise<void> {
  if (entered) return;
  entered = true;
  app.loading = false;
  await refreshNotebooks();
}

/** Paints the gate from the server's answer, which is the only copy of that rule the
 *  frontend holds: both places that read the gate call this, so neither can decide on its
 *  own when the wizard is the screen.
 *
 *  `starting` opens the gate. The machine has nothing left to ask and the model server is a
 *  process libris's own start-up is already bringing up, so a wizard here is a screen
 *  describing a wait — and on a cold start the model takes longer to load than the page takes
 *  to ask, which is how a set-up machine kept meeting the wizard at every boot. A question
 *  asked in the meantime fails with the server's own reason, the same way the walked-past
 *  setup behaves.
 *
 *  It is also the one place that sees the wizard let go, so the walk into the app hangs off
 *  it: whoever opens the gate finishes the boot the gate stopped, and a new caller cannot
 *  forget to. */
export function applyGate(setup?: SetupView): void {
  app.ready = setup?.ready ?? true;
  const gated = !(app.ready || (setup?.starting ?? false));
  if (app.gated && !gated) void enterApp();
  app.gated = gated;
}

export async function boot() {
  initPanels();
  applyNarrow(true);

  // Loaded before the gate, because the wizard writes a setting too — the device it picks —
  // and it cannot save what it has never read.
  void loadSettings();

  // Which place a program came from, asked once before anything can blame one for being
  // missing. It is not part of the gate: a machine that cannot read a PDF can still answer
  // questions about everything else, and the row says so on its own.
  try {
    app.tools = await api.getTools();
  } catch {
    app.tools = null;
  }

  // The gate is the server's rule, read once at boot and then re-read only while the wizard
  // is on screen. A settings change made inside the app must not tear the app away.
  try {
    const engine = await api.getEngine();
    applyGate(engine.setup);
  } catch {
    applyGate(undefined);
  }
  if (app.gated && setupSkipped()) app.gated = false;
  // The page shell painted the theme before this ran, so the stylesheet never flashes; this is
  // the other half, the state the toggle button reads. Without it the button offers the theme
  // the page is already in, and the first press appears to do nothing.
  initTheme();

  if (app.gated) return;

  await refreshNotebooks();
  if (app.notebookId === null && app.notebooks.length) {
    // The app opens on the newest notebook and its newest conversation.
    await openNotebook(app.notebooks[app.notebooks.length - 1].id);
  }
  app.loading = false;
}

// ── notebooks and conversations ───────────────────────────────────

export async function refreshNotebooks(): Promise<Notebook[]> {
  const list = await api.listNotebooks();
  app.notebooks = list;
  return list;
}

/**
 * Selecting a notebook mounts the notebook view freshly: its conversations, its sources, and
 * the newest conversation inside it.
 */
export async function openNotebook(id: string) {
  const seq = ++navSeq;
  app.view = 'notebook';
  app.notebookId = id;
  app.chats = [];
  app.chatId = null;
  app.messages = [];
  app.sources = [];
  app.viewer = null;
  app.viewerSeg = '';
  app.crumbNotebook = id;
  app.error = '';
  clearLive();
  stopSourcesPoll();

  const [chats, sources] = await Promise.all([api.listChats(id), api.listSources(id)]);
  if (seq !== navSeq) return;
  app.chats = chats;
  app.sources = sortSources(sources);
  app.chatId = chats[0]?.id ?? null;
  app.panel = 'sources';
  pollSources();
  if (app.chatId) void loadMessages(app.chatId);
}

export function openSettings() {
  app.view = 'settings';
  // The list has nothing to select here, so it unmounts — but the way out is the gear as much
  // as the crumb, and leaving settings returns to the notebook the reader came out of.
  app.notebookId = null;
  app.viewer = null;
  clearLive();
  stopSourcesPoll();
  app.error = '';
}

export function leaveSettings() {
  app.view = 'notebook';
  // A window narrowed while settings were open: the panel is not mounted there, so nothing
  // else was watching, and coming back must not put two fixed columns over a reading column
  // too narrow to hold a sentence.
  applyNarrow();
  if (!app.notebookId && app.crumbNotebook) void openNotebook(app.crumbNotebook);
}

/** The brand tile and the `libris` crumb: nothing is selected, and a refetch must not
 *  re-pick a notebook the reader navigated away from. */
export function goHome() {
  app.view = 'notebook';
  app.notebookId = null;
  // The crumb is what the header shows; a notebook that is no longer open must not stay in
  // it, and the way back out of settings has nothing left to return to.
  app.crumbNotebook = null;
  app.chatId = null;
  app.chats = [];
  app.messages = [];
  app.sources = [];
  app.viewer = null;
  clearLive();
  stopSourcesPoll();
  app.error = '';
}

export async function createNotebook(name: string): Promise<Notebook> {
  const nb = await api.createNotebook(name);
  await refreshNotebooks();
  await openNotebook(nb.id);
  return nb;
}

export async function renameNotebook(id: string, name: string) {
  await api.renameNotebook(id, name);
  app.notebooks = app.notebooks.map((n) => (n.id === id ? { ...n, name } : n));
}

export async function deleteNotebook(id: string) {
  await api.deleteNotebook(id);
  const rest = await refreshNotebooks();
  if (app.notebookId !== id) return;
  // Another notebook is open in its place, and the rail forgets the one that went with it.
  const next = rest[rest.length - 1];
  if (next) await openNotebook(next.id);
  else goHome();
}

export async function openChat(id: string) {
  if (id === app.chatId) return;
  // A streaming answer belongs to the conversation it was asked in, not the one now on screen.
  clearLive();
  app.chatId = id;
  await loadMessages(id);
}

/** `+` creates nothing. A conversation begins at its first question. */
export function newConversation() {
  clearLive();
  app.chatId = null;
  app.messages = [];
  app.error = '';
}

let navSeq = 0;

/** A response for a selection the reader left is discarded. */
async function loadMessages(chatId: string) {
  const seq = ++msgSeq;
  const nb = app.notebookId;
  if (!nb) return;
  app.messagesLoading = true;
  try {
    const rows = await api.listMessages(nb, chatId);
    if (seq !== msgSeq || app.chatId !== chatId) return;
    app.messages = rows;
    app.error = '';
  } catch (e) {
    if (seq !== msgSeq || app.chatId !== chatId) return;
    app.error = errorMessage(e);
  } finally {
    if (seq === msgSeq) app.messagesLoading = false;
  }
}

let msgSeq = 0;

export async function refreshChats() {
  if (!app.notebookId) return;
  app.chats = await api.listChats(app.notebookId);
}

export async function renameChat(id: string, title: string) {
  if (!app.notebookId) return;
  await api.renameChat(app.notebookId, id, title);
  // The list and the breadcrumb come from the same object, so they cannot disagree about
  // which conversation is open.
  app.chats = app.chats.map((c) => (c.id === id ? { ...c, title } : c));
}

export async function deleteChat(id: string) {
  if (!app.notebookId) return;
  await api.deleteChat(app.notebookId, id);
  app.chats = await api.listChats(app.notebookId);
  if (app.chatId !== id) return;
  const next = app.chats[0];
  if (next) await openChat(next.id);
  else {
    app.chatId = null;
    app.messages = [];
  }
}

// ── sources ───────────────────────────────────────────────────────

/** Newest first: a list of sources is read top down, and the last thing added is the top thing. */
function sortSources(list: Source[]): Source[] {
  return [...list].sort((a, b) => b.added_at - a.added_at || (a.id < b.id ? 1 : -1));
}

export async function refreshSources() {
  if (!app.notebookId) return;
  try {
    const list = await api.listSources(app.notebookId);
    app.sources = sortSources(list);
    app.error = '';
  } catch (e) {
    app.error = errorMessage(e);
  }
  pollSources();
}

let sourcesTimer: ReturnType<typeof setInterval> | null = null;
let uploading = 0;

/** The loop exists only while something is busy and stops the moment it is not. */
function pollSources() {
  const busy =
    uploading > 0 || app.sources.some((s) => s.status === 'processing');
  if (!busy) {
    stopSourcesPoll();
    return;
  }
  if (sourcesTimer) return;
  sourcesTimer = setInterval(async () => {
    if (!app.notebookId) return stopSourcesPoll();
    try {
      app.sources = sortSources(await api.listSources(app.notebookId));
    } catch {
      // A failed refresh of something already on screen keeps the screen.
    }
    if (!uploading && !app.sources.some((s) => s.status === 'processing')) {
      stopSourcesPoll();
      void refreshNotebooks();
    }
  }, 1200);
}

function stopSourcesPoll() {
  if (!sourcesTimer) return;
  clearInterval(sourcesTimer);
  sourcesTimer = null;
}

/**
 * Uploads are serial, one file at a time, because the backend ingests one at a time: a second
 * file's row appearing while the first is still being read would be a lie about what has
 * happened.
 */
const queue: File[] = [];

export function uploadFiles(files: File[]) {
  if (!app.notebookId || !files.length) return;
  queue.push(...files);
  void pump();
}

async function pump() {
  if (!app.notebookId || uploading || !queue.length) return;
  const notebook = app.notebookId;
  const file = queue.shift() as File;
  uploading += 1;
  pollSources();
  try {
    const source = await api.uploadSource(notebook, file);
    if (app.notebookId === notebook) {
      const rest = app.sources.filter((s) => s.id !== source.id);
      app.sources = sortSources([source, ...rest]);
      void refreshNotebooks();
    }
  } catch (e) {
    app.error = errorMessage(e);
  } finally {
    uploading -= 1;
    void refreshSources();
    pollSources();
    // One file per pump, and the queue is the reason pump exists at all: a drop of six
    // files is six uploads, and stopping after the first would leave five sitting in an
    // array nothing else reads. Serial on purpose — see the comment above the queue.
    if (queue.length) void pump();
  }
}

/**
 * One endpoint, two meanings: letting go of a file the last notebook lists deletes it, and
 * letting go of one another notebook lists keeps it. Which one it is comes from the server's
 * count, never from a guess here.
 */
export async function removeSource(source: Source) {
  if (!app.notebookId) return;
  await api.detachSource(app.notebookId, source.id);
  // A source taken away takes any open viewer of it with it: the app cannot serve segments
  // for a document the notebook no longer has.
  if (app.viewer === source.id) closeViewer();
  await Promise.all([refreshSources(), refreshNotebooks()]);
}

/** Halt a source's run where it stands; the row goes to `stopped` when the run notices. */
export async function stop(source: Source) {
  try {
    const halted = await api.stopSource(source.id);
    app.sources = app.sources.map((s) => (s.id === halted.id ? halted : s));
    // The stop lands a moment after the answer, so this is the poll that sees it.
    pollSources();
    void refreshSources();
  } catch (e) {
    app.error = errorMessage(e);
  }
}

/** Continue a stopped source: everything already vectorised is kept, the rest is done. */
export async function resume(source: Source) {
  try {
    const again = await api.resumeSource(source.id);
    app.sources = app.sources.map((s) => (s.id === again.id ? again : s));
    pollSources();
    void refreshSources();
  } catch (e) {
    app.error = errorMessage(e);
  }
}

export async function reingest(source: Source) {
  if (!app.notebookId) return;
  try {
    const again = await api.reingestSource(source.id);
    app.sources = app.sources.map((s) => (s.id === again.id ? again : s));
    pollSources();
    void refreshSources();
  } catch (e) {
    app.error = errorMessage(e);
  }
}

// ── the chunk viewer ──────────────────────────────────────────────

let viewerSeq = 0;

export async function openViewer(sourceId: string, segmentId = '') {
  const seq = ++viewerSeq;
  app.panel = 'sources';
  app.viewer = sourceId;
  app.viewerSeg = segmentId;
  app.viewerError = '';
  // Opening a citation forces the panel open even if it was shut.
  if (!app.sideOpen) setOpen(SIDE, true);
  app.viewerSegments = [];
  app.viewerKinds = {};
  app.viewerLoading = false;
  app.viewerEnd = false;
  try {
    const first = await api.listSegments(sourceId);
    if (seq !== viewerSeq) return;
    app.viewerSegments = first.segments;
    app.viewerKinds = first.kinds;
  } catch (e) {
    if (seq !== viewerSeq) return;
    app.viewerError = errorMessage(e);
  }
}

/** The next window of the chunks of whatever source the viewer has open, appended behind what
 *  the reader has already scrolled past. Called by the scroll of the list and by nothing else:
 *  the reader's own speed is the only clock this needs, which is why there is no timer.
 *
 *  It asks for nothing when the file is already here, and asks twice never — a scroll gesture
 *  is a hundred events, and the answer to the first of them is still in the air. */
export async function loadMoreViewerChunks(limit?: number) {
  const id = app.viewer;
  if (!id || app.viewerLoading) return;
  const offset = app.viewerSegments.length;
  if (app.viewerEnd || offset >= chunkTotal()) return;
  app.viewerLoading = true;
  const seq = viewerSeq;
  try {
    const page = await api.listSegments(id, offset, limit);
    // A viewer that was closed, or turned to another source, while this was in the air gets
    // no answer — its chunks belong to a file nobody is reading.
    if (seq !== viewerSeq || app.viewer !== id) return;
    // A source being indexed grows while it is read, so the count in the answer is the one
    // to believe going forward.
    app.viewerKinds = page.kinds;
    app.viewerSegments = [...app.viewerSegments, ...page.segments];
    if (!page.segments.length) app.viewerEnd = true;
  } catch (e) {
    if (seq === viewerSeq) app.viewerError = errorMessage(e);
  } finally {
    if (seq === viewerSeq) app.viewerLoading = false;
  }
}

/** How many chunks the open file holds, summed from the per-kind counts the server sent —
 *  rather than from what happens to be on the page. */
function chunkTotal() {
  return Object.values(app.viewerKinds).reduce((n, k) => n + k, 0);
}

/** How many of them have reached the page. */
export function chunkLoaded() {
  return app.viewerSegments.length;
}


export function closeViewer() {
  viewerSeq++;
  app.viewer = null;
  app.viewerSeg = '';
  app.viewerError = '';
  app.viewerLoading = false;
}

/**
 * A citation is an instruction to look. The source is looked up rather than taken on trust
 * from the answer, because it may have been detached from this notebook since the question was
 * asked — and an old citation should still open.
 */
export function openCitation(c: Citation) {
  void openViewer(c.source_id, c.segment_id);
}

export function setPanel(p: Panel) {
  app.panel = p;
  if (p === 'sources') return;
  app.viewer = null;
}

// ── asking ────────────────────────────────────────────────────────

let abort: AbortController | null = null;

export function canAsk(question: string) {
  return (
    !!app.notebookId &&
    !app.live &&
    question.trim().length > 0 &&
    app.sources.some((s) => s.status === 'ready')
  );
}

export function draftKey() {
  return app.chatId ?? '__new__';
}

export function clearLive() {
  abort?.abort();
  abort = null;
  app.live = null;
}

export function stopTurn() {
  abort?.abort();
}

/** One answer delta folded into the live turn.
 *
 * Both halves accumulate: a delta is a piece, not a running total, and assigning the
 * thinking rather than appending it shows the reader the newest fragment and throws the rest
 * away. The first sentence of the answer closes the window on the thinking — the moment the
 * reader stops watching the work and starts reading its result. */
function fold(live: Live, text: string, reasoning: string) {
  if (reasoning) {
    live.reasoning += reasoning;
    live.thinkingOpen = true;
  }
  if (!text) return;
  if (!live.answer) live.thinkingOpen = false;
  live.answer += text;
}

export async function ask(question: string) {
  const notebook = app.notebookId;
  if (!notebook || app.live) return;
  const asked = question.trim();
  const known = app.chatId;
  app.error = '';
  app.drafts[draftKey()] = '';

  // The moment a question is sent it appears on screen; a spinner for half a minute reads as a
  // hang.
  const turn: Live = {
    question: asked,
    status: '',
    reasoning: '',
    thinkingOpen: false,
    answer: '',
    citations: [],
    streaming: true
  };
  app.live = turn;
  // `app.live` is the proxy `$state` made of the turn, and it is a different identity from
  // `turn`. The callbacks below compare against, and write through, the proxy: against the
  // raw object the comparison is never true, and every delta would be dropped in silence.
  const live = app.live as Live;

  const controller = new AbortController();
  abort = controller;
  let saved: Message | null = null;
  let failed = '';

  try {
    await api.ask(
      notebook,
      known ?? '',
      asked,
      {
        onStatus: (text) => {
          if (app.live === live) live.status = text;
        },
        onCitations: (citations) => {
          if (app.live === live) live.citations = citations;
        },
        onDelta: (text, reasoning) => {
          if (app.live === live) fold(live, text, reasoning);
        },
        onDone: (message) => {
          saved = message;
        }
      },
      controller.signal
    );
  } catch (e) {
    failed = errorMessage(e);
  } finally {
    if (abort === controller) abort = null;
  }

  const cancelled = controller.signal.aborted;
  const written = live.answer;

  // The stored row is the last word: it carries the id, the status and the frozen citations,
  // and the turn may have ended in a way the client did not witness. The live answer is the
  // only copy of a stopped answer until it arrives, so the turn comes down all at once,
  // *after* the row — blanking it first blinks the answer out for a frame and back.
  const done = saved as Message | null;
  const chat = done?.chat_id ?? known ?? '';
  let refreshed = false;
  if (chat) {
    try {
      const rows = await api.listMessages(notebook, chat);
      app.messages = rows;
      refreshed = true;
    } catch {
      // Keep what is on screen rather than blanking a real answer over a failed poll.
    }
  }
  if (!known && chat) {
    // The conversation exists now, and its title is the server's.
    app.chats = await api.listChats(notebook).catch(() => app.chats);
    app.chatId = chat;
  }
  app.drafts[chat || '__new__'] = '';

  if (refreshed) app.live = null;
  else if (written || !done) {
    // Nothing arrived, so what is on screen stands, labelled as it ended.
    if (!written && !failed) app.live = null;
    else {
      app.messages = [
        ...app.messages,
        {
          id: -Date.now(),
          chat_id: chat,
          role: 'assistant',
          content: written,
          status: cancelled ? 'canceled' : 'interrupted',
          created_at: Math.floor(Date.now() / 1000)
        }
      ];
      app.live = null;
    }
  }
  if (failed) app.error = failed;
}

// ── panels ────────────────────────────────────────────────────────

export function setWidth(spec: PanelSpec, n: number) {
  const w = clampW(spec, n);
  if (spec === NOTEBOOKS) app.sidebarW = w;
  else app.sideW = w;
}

/** Only a *user* action writes the open flag: a width the window imposed is not a preference. */
export function setOpen(spec: PanelSpec, open: boolean) {
  if (spec === NOTEBOOKS) app.sidebarOpen = open;
  else app.sideOpen = open;
  writeOpen(spec, open);
}

let lastNarrow: boolean | null = null;

/**
 * The narrow rule fires only when its answer changes, never on every resize event: a rule that
 * runs on every event takes the panel away again a second after the user put it back.
 */
export function applyNarrow(initial = false) {
  const { tooNarrow, hasRoom } = narrowRule(
    window.innerWidth,
    { width: app.sidebarW, open: app.sidebarOpen },
    { width: app.sideW, open: app.sideOpen }
  );
  if (initial) {
    lastNarrow = tooNarrow;
    return;
  }
  if (tooNarrow === lastNarrow) return;
  lastNarrow = tooNarrow;
  if (tooNarrow) app.sideOpen = false;
  else if (hasRoom && readOpen(SIDE)) app.sideOpen = true;
}

// ── theme ─────────────────────────────────────────────────────────

export function initTheme() {
  // One stored form — the bare word `dark` or `light`, the same string the page shell reads
  // before it paints. Two shapes here is how a saved theme goes missing on reload.
  const saved = localStorage.getItem('libris.theme');
  const dark = saved
    ? saved === 'dark'
    : matchMedia('(prefers-color-scheme: dark)').matches;
  app.theme = dark ? 'dark' : 'light';
  document.documentElement.dataset.theme = app.theme;
}

export function toggleTheme() {
  app.theme = app.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = app.theme;
  localStorage.setItem('libris.theme', app.theme);
}

// The two panel specs and the clamp, re-exported so a screen never imports geometry from two
// places.
export { NOTEBOOKS, SIDE, clampW, type PanelSpec } from './panel';
