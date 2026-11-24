<script lang="ts">
  // The first run, and every run after that which cannot answer a question yet.
  //
  // It replaces the app rather than sitting inside it, because the app has nothing to show
  // until it ends: a question and an ingest both need the embedder, and an answer needs the
  // chat model, so a user allowed in is a user invited to watch a failure they could have
  // avoided. The steps are the facts the server reports, not a script to be followed — if the
  // models are already on disk the wizard opens on the third row.
  import {
    getSettings,
    installEngine,
    installModel,
    putSettings,
    startEngineServe,
  } from './api';
  import type { EngineView, ModelsView, DeviceReport, Device } from './types';
  import { DEVICE_ACK, type Setup } from './setup';
  import { remaining } from './setup';
  import { formatBytes } from './format';

  let {
    setup,
    engine,
    models,
    devices,
    onchange,
  }: {
    setup: Setup;
    engine: EngineView | null;
    models: ModelsView | null;
    devices: DeviceReport | null;
    /** Re-read the three facts. The caller polls as well, so a bar has an owner either way. */
    onchange: () => void;
  } = $props();

  // Both models, one after the other, from a single ask. The fetcher takes one download at a
  // time on purpose — two 1.4 GB writes to one disk are slower together, and one bar cannot
  // describe two runs without lying about one (see ErrBusy in internal/models) — so the queue
  // lives here, where what a person asked for is "the models", and the server's rule stays.
  const todo = $derived([setup.chat, setup.embed].filter((e) => e && !e.complete));
  const running = $derived(
    models?.progress
      ? models.progress.state === 'downloading' || models.progress.state === 'verifying'
      : false,
  );
  // The engine's download is the same kind of job as the models': started by a request that
  // returns at once, then polled. `busy` therefore goes true for a moment and the button stays
  // live for the whole 40 MB after it — same mistake as the models' button, one step earlier.
  const engineRunning = $derived(
    engine?.progress
      ? ['downloading', 'verifying', 'unpacking'].includes(engine.progress.state)
      : false,
  );
  // Which models the one button was told to fetch. Page state would be lost on a reload, and a
  // reload does not cancel the download that is running — the fetcher keeps going on purpose —
  // so a queue that lives only in the component comes back as one model installed and the other
  // reading "waiting" forever. It goes beside the other per-user view state (§7), and it is not
  // re-derived from "what is missing": that would fetch gigabytes nobody asked for on a page
  // load.
  const QUEUE_KEY = 'libris.model-queue';
  let queue = $state<string[]>(readQueue());
  let started = $state('');

  function readQueue(): string[] {
    try {
      const raw = JSON.parse(localStorage.getItem(QUEUE_KEY) ?? '[]');
      return Array.isArray(raw) ? raw.filter((x) => typeof x === 'string') : [];
    } catch {
      return [];
    }
  }

  $effect(() => {
    try {
      if (queue.length) localStorage.setItem(QUEUE_KEY, JSON.stringify(queue));
      else localStorage.removeItem(QUEUE_KEY);
    } catch {
      // A locked profile loses the queue on reload, which is the older and smaller bug.
    }
  });

  $effect(() => {
    const head = queue[0];
    if (!head) return;
    // Nothing is fetched while an earlier step is what the screen is showing. The queue outlives
    // the page now, and that is how a stale one from a previous session fired the moment the
    // first poll arrived — while the user was pressing "Download llama.cpp", with no engine and
    // the models not even on screen. A button that appears to fetch gigabytes it never mentioned
    // is worse than a queue that has to be started again.
    if (setup.step !== 'models') {
      if (queue.length) queue = [];
      return;
    }
    const entry = models?.entries.find((e) => e.id === head);
    // Advancing comes before every "nothing to do right now" guard, and that ordering was the
    // bug: `started === head` is a reason not to *start* the head, but it also returned before
    // the check that says the head is finished — so the moment a download completed, the queue
    // was stuck on it. The button then needed a second click to do the second half of what the
    // first click asked for, which is the entire reason there is one button.
    if (entry?.complete) {
      queue = queue.slice(1);
      started = '';
      return;
    }
    if (!entry || busy !== '' || running || started === head) return;
    if (models?.progress?.state === 'failed' && models.progress.id === head) {
      // The reason is already on the page as a sentence. Starting it again would only fail
      // the same way, and a queue that retries on its own looks like a download that never ends.
      queue = [];
      return;
    }
    started = head;
    void act('model:' + head, () => installModel(head)).then((ok) => {
      if (ok) return;
      // A refused request — a busy fetcher, a failed write — is not a download in progress.
      // Without this the queue keeps a head it could not start, the button keeps saying
      // "downloading", and the row keeps saying "waiting": two sentences about a thing nobody
      // is doing. The reason is already in the note under the button.
      queue = [];
      started = '';
    });
  });

  let busy = $state('');
  let note = $state('');

  const step = $derived(setup.step);
  const rows = $derived([
    {
      n: 1,
      key: 'engine' as const,
      title: 'Engine',
      done: setup.engine,
      detail: engine?.installed
        ? `llama.cpp ${engine.installed.tag}`
        : engine?.latest
          ? `llama.cpp ${engine.latest.tag} · ${formatBytes(engine.asset?.size ?? 0)}`
          : 'llama.cpp, downloaded from upstream',
    },
    {
      n: 2,
      key: 'device' as const,
      title: 'Inference device',
      done: setup.device,
      // "Two available" is not the same sentence as "not chosen yet", and the row is the only
      // place the difference shows before the step is open.
      detail: devices?.devices.length
        ? setup.device
          ? `${devices.devices.length} available`
          : 'not chosen'
        : setup.engine
          ? 'asking the engine'
          : 'waiting for the engine',
    },
    {
      n: 3,
      key: 'models' as const,
      title: 'Models',
      done: setup.models,
      detail: setup.models
        ? setup.serving
          ? 'ready'
          : 'on disk, server stopped'
        : remaining(setup)
          ? `${formatBytes(remaining(setup))} left to download`
          : 'waiting',
    },
  ]);

  const progress = $derived(
    step === 'engine' ? engine?.progress : (models?.progress ?? undefined),
  );
  const bar = $derived(
    progress && progress.total > 0
      ? Math.min(100, Math.round((progress.done / progress.total) * 100))
      : 0,
  );

  /** Whether the action worked, so the queue can tell a download from a refusal. */
  async function act(what: string, fn: () => Promise<unknown>): Promise<boolean> {
    busy = what;
    note = '';
    try {
      await fn();
      onchange();
      return true;
    } catch (e) {
      note = e instanceof Error ? e.message : String(e);
      return false;
    } finally {
      busy = '';
    }
  }

  async function pick(device: Device | null) {
    // One field, saved into the settings as they are now rather than as the wizard guesses
    // they were: a draft the page keeps is a second copy of a value with an owner (§6).
    const current = await getSettings();
    await putSettings({ ...current, engineDevice: device ? device.name : '' });
    // A blank setting means libris picks, so choosing that leaves nothing behind to show the
    // step was answered — without this the wizard would ask again after every reload.
    try {
      localStorage.setItem(DEVICE_ACK, '1');
    } catch {
      // Private mode, a locked profile: the step asks again next time, which is the older and
      // gentler bug rather than a reason to fail the save that just worked.
    }
  }
</script>

<div class="setup">
  <div class="bar">
    <span class="mark">libris</span>
    <span class="title">Set up Libris</span>
    <span class="count">
      {rows.filter((r) => r.done).length} of {rows.length}
    </span>
  </div>

  <div class="body">
    <ol class="steps">
      {#each rows as r (r.key)}
        <li class:active={r.key === step} class:done={r.done}>
          <span class="dot" aria-hidden="true">{r.done ? '✓' : r.n}</span>
          <span class="what">
            <span class="name">{r.title}</span>
            <span class="detail">{r.detail}</span>
          </span>
        </li>
      {/each}
    </ol>

    <section class="work">
      {#if step === 'engine'}
        <h2>Get the engine</h2>
        <p>
          Libris runs one local model server, and it is not something to install by hand — it is
          fetched from upstream when needed and checked against the digest upstream publishes.
        </p>
        <button
          class="btn btn-primary"
          disabled={busy !== '' || engineRunning}
          onclick={() => act('engine', () => installEngine(engine?.latest?.tag))}
        >
          {engineRunning ? 'downloading…' : 'Download llama.cpp'}
        </button>
      {:else if step === 'device'}
        <h2>Where does it run?</h2>
        <p>
          These are the slots the engine itself reported. Libris picks one when you do not, and
          you can change it later in Settings.
        </p>
        <ul class="devices">
          {#each devices?.devices ?? [] as d (d.name)}
            <li>
              <span class="dev">
                <span class="slot">{d.name}</span>
                <span class="desc">{d.description || d.kind}</span>
                {#if d.total_bytes}<span class="size">{formatBytes(d.total_bytes)}</span>{/if}
              </span>
              <button class="btn" disabled={busy !== ''} onclick={() => act('device:' + d.name, () => pick(d))}>
                Use this
              </button>
            </li>
          {/each}
        </ul>
        <button class="btn" disabled={busy !== ''} onclick={() => act('device:auto', () => pick(null))}>
          {busy === 'device:auto' ? 'Saving…' : 'Let Libris choose'}
        </button>
      {:else}
        <h2>
          {setup.models && !setup.serving ? 'Start the server' : 'Get the models'}
        </h2>
        <p>
          One model answers, one embeds. Both are pinned downloads — the same files every time,
          checked as they arrive — because a different embedder would silently invalidate every
          vector already stored.
        </p>
        {#if todo.length}
          <button
            class="btn btn-primary"
            disabled={busy !== '' || running || queue.length > 0}
            onclick={() => {
              started = '';
              queue = todo.map((e) => e!.id);
            }}
          >
            {running || queue.length
              ? 'downloading…'
              : todo.length > 1
                ? `Download both models · ${formatBytes(todo.reduce((n, e) => n + (e?.bytes ?? 0), 0))}`
                : `Download ${todo[0]!.label} · ${formatBytes(todo[0]!.bytes)}`}
          </button>
          {#if !running && !queue.length}
            <p>a stopped download keeps its bytes, so this resumes where it left off</p>
          {/if}
        {/if}

        <ul class="models">
          {#each [setup.chat, setup.embed].filter(Boolean) as m (m!.id)}
            <li class:done={m!.complete}>
              <span class="what">
                <span class="name">{m!.label}</span>
                <span class="detail">{m!.quant} · {formatBytes(m!.bytes)}</span>
              </span>
              {#if m!.complete}
                <span class="ok">installed</span>
              {:else}
                <span class="wait">
                  {running && models?.progress?.id === m!.id
                    ? models.progress.state === 'verifying' && 'checking…' || 'downloading…'
                    : 'waiting'}
                </span>
              {/if}
            </li>
          {/each}
        </ul>

        {#if setup.models && !setup.serving}
          <p>Everything is on disk. The server loads a model when a question first asks for one.</p>
          <button class="btn btn-primary" disabled={busy !== ''} onclick={() => act('serve', () => startEngineServe())}>
            {busy === 'serve' ? 'Starting…' : 'Start the server'}
          </button>
        {/if}
      {/if}

      {#if progress && (progress.state === 'downloading' || progress.state === 'verifying')}
        <div class="progress" role="progressbar" aria-valuenow={bar} aria-valuemin={0} aria-valuemax={100}>
          <div class="fill" style="width: {bar}%"></div>
        </div>
        <p class="where">
          {progress.state === 'verifying' ? 'checking the digest…' : `downloading ${bar}%`}
        </p>
      {/if}

      {#if progress?.error || note}
        <p class="bad" role="alert">{progress?.error || note}</p>
      {/if}
    </section>
  </div>
</div>

<style>
  .setup {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    background: var(--canvas);
    overflow: auto;
  }
  .bar {
    height: 56px;
    flex: none;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 20px;
    background: var(--canvas-subtle);
    border-bottom: 1px solid var(--border);
  }
  .mark {
    font-family: var(--font-mono);
    font-weight: 600;
  }
  .mark::before {
    content: '';
    display: inline-block;
    width: 3px;
    height: 12px;
    margin-right: 8px;
    vertical-align: -1px;
    background: var(--accent);
  }
  .title {
    color: var(--fg-muted);
  }
  .count {
    margin-left: auto;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg-muted);
  }
  .body {
    width: min(100%, 42rem);
    margin: 0 auto;
    padding: 32px 20px 48px;
    display: flex;
    flex-direction: column;
    gap: 28px;
  }
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .steps li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border-radius: var(--radius);
  }
  .steps li.active {
    background: var(--elevated);
    box-shadow: var(--shadow-sm);
  }
  .dot {
    flex: none;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    font-size: 12px;
    font-family: var(--font-mono);
    color: var(--fg-muted);
    background: var(--canvas-subtle);
    border: 1px solid var(--border);
  }
  .steps li.done .dot {
    color: var(--success);
    border-color: var(--success);
  }
  .steps li.active .dot {
    border-color: var(--accent);
    color: var(--fg);
  }
  .what {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .name {
    font-weight: 600;
  }
  .detail {
    font-size: 13px;
    color: var(--fg-muted);
  }
  .work {
    background: var(--elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 20px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .work h2 {
    margin: 0;
    font-size: 16px;
  }
  .work p {
    margin: 0;
    font-size: 14px;
    color: var(--fg-muted);
    line-height: 1.5;
  }
  .devices,
  .models {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .devices li,
  .models li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 10px;
    border-radius: var(--radius);
    background: var(--canvas-subtle);
  }
  .models li.done .what {
    color: var(--fg-muted);
  }
  /* The rule was written for `.what` and the markup says `.dev`, which is how a device row
     lost its row layout: a scoped selector nobody matches fails silently, exactly like a
     `var()` naming a token that does not exist. */
  .devices .dev {
    display: flex;
    flex-direction: row;
    align-items: baseline;
    gap: 8px;
    flex-wrap: wrap;
    min-width: 0;
  }
  .slot {
    font-family: var(--font-mono);
    font-size: 13px;
  }
  .desc,
  .size {
    font-size: 12px;
    color: var(--fg-muted);
  }
  .devices button,
  .models .ok,
  .models .wait {
    margin-left: auto;
  }
  .ok {
    font-size: 12px;
    color: var(--success);
  }
  /* Green is a fact about this file, not a layout slot: a model that has not been fetched
   yet must not wear the same badge as one that has. */
  .wait {
    font-size: 12px;
    color: var(--fg-muted);
  }
  .progress {
    height: 6px;
    border-radius: 999px;
    background: var(--canvas-subtle);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
    transition: width 0.3s;
  }
  .where {
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .bad {
    color: var(--danger);
  }
</style>
