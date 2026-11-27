<script lang="ts">
  import { onMount } from 'svelte';
  import * as api from '../lib/api';
  import { app, applyGate, saveSettings, skipSetup } from '../lib/store.svelte';
  import { errorMessage, formatBytes } from '../lib/format';
  import { ackForConfirm } from '../lib/device';
  import type { DeviceReport, EngineView, Fact, ModelsView } from '../lib/types';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';

  /**
   * The app before the app. The steps are the facts the server reports, not a script: a
   * machine that already has the models on disk opens on the third row, and a ready machine
   * never sees this screen at all.
   */
  const NAMES: Record<Fact, string> = {
    engine: 'Engine',
    device: 'Inference device',
    models: 'Models',
    serving: 'Models'
  };

  let engine = $state<EngineView>();
  let models = $state<ModelsView>();
  let report = $state<DeviceReport>();
  let error = $state('');
  let busy = $state(false);

  const setup = $derived(engine?.setup);
  const steps = ['Engine', 'Inference device', 'Models'] as const;
  // The server names the step for the three facts it cannot fix itself, and leaves it empty
  // when the one thing missing is the model-server process — which it starts on its own, and
  // which can still fail to come up. Reading the empty step as "engine" would send that person
  // back to a step they finished, with nothing left to click, so the blocking list decides
  // where they are. The fallback name is the first step, not a blank panel.
  // Where the wizard lands by itself: the first step the server cannot get past on its own.
  const blocking = $derived(NAMES[setup?.step ?? setup?.blocking?.[0] ?? 'engine']);
  /**
   * A step the person clicked back to. A finished step stays changeable — a device picked by
   * mistake is still the wrong device once the models step is in front of them — so which step
   * is showing is theirs to decide, and nothing about looking back at one undoes it. It is
   * dropped the moment the blocking step moves, because then there is a new one to land on.
   */
  let chosen = $state<string | null>(null);
  let landed = $state<string | null>(null);
  $effect(() => {
    if (landed !== blocking) {
      landed = blocking;
      chosen = null;
    }
  });
  const active = $derived(chosen ?? blocking);
  const stepIndex = $derived(steps.indexOf(active as (typeof steps)[number]));
  // Behind the blocking step is revisitable; ahead of it is a step whose prerequisite is
  // still missing, so there is nothing there to do yet.
  const reach = (name: string) =>
    steps.indexOf(name as (typeof steps)[number]) <=
    steps.indexOf(blocking as (typeof steps)[number]);

  const entries = $derived(models?.entries ?? []);
  const wanted = $derived(entries.filter((e) => !e.complete));
  const missingBytes = $derived(wanted.reduce((n, e) => n + e.bytes - e.partialBytes, 0));
  const downloaded = $derived(!!engine?.installed);
  const modelsOnDisk = $derived(entries.length > 0 && entries.every((e) => e.complete));
  const devices = $derived(report?.devices ?? []);
  /** A download in flight is a fact from the engine view, not a promise held by the button
   *  that started it: the POST answers as soon as it is underway, so the request is long
   *  gone while the bytes are still arriving. */
  const BUSY = new Set(['downloading', 'verifying', 'unpacking']);
  const downloading = $derived(BUSY.has(engine?.progress?.state ?? ''));
  const failed = $derived(engine?.progress?.state === 'failed');

  /** A model transfer in flight, from the server's facts rather than from the button that
   *  started it: the POST is long gone while the bytes are still arriving. */
  const modelRunning = $derived(
    entries.some((e) => e.downloading) ||
      models?.progress?.state === 'downloading' ||
      models?.progress?.state === 'verifying'
  );

  /**
   * One bar for the step, not one per transfer. A model download reports the bytes of the file
   * it is carrying, so a bar built on it sweeps to 100% and starts again for the second model;
   * across the models step the fraction is every model's bytes instead, which crosses once.
   */
  const progress = $derived.by(() => {
    if (modelRunning) {
      return {
        state: models?.progress?.state ?? 'downloading',
        total: entries.reduce((n, e) => n + e.bytes, 0),
        done: entries.reduce((n, e) => n + (e.complete ? e.bytes : e.partialBytes), 0)
      };
    }
    const p = engine?.progress;
    return p && ['downloading', 'verifying', 'unpacking'].includes(p.state)
      ? { state: p.state, done: p.done, total: p.total }
      : undefined;
  });
  const pct = $derived(
    progress?.total
      ? Math.min(100, Math.round((progress.done / progress.total) * 100))
      : 0
  );

  const detail = $derived.by(() => {
    const e = setup;
    if (!e) return { Engine: 'Unknown yet.', 'Inference device': 'Unknown yet.', Models: 'Unknown yet.' };
    return {
      Engine: engine?.installed
        ? `Installed · ${engine.installed.tag}`
        : engine?.latest
          ? `${engine.latest.tag} · ${formatBytes(engine.asset?.size ?? 0)} · not downloaded`
          : 'A local model server, fetched from upstream.',
      'Inference device': !e.engine
        ? 'Waits for the engine.'
        : !report
          ? 'Asking the engine what it can run on…'
          : app.settings.engineDevice
            ? `${app.settings.engineDevice} · ${devices.length} choices`
            : `${devices.length} ${devices.length === 1 ? 'slot' : 'slots'}, none chosen`,
      Models: e.models && e.serving
        ? 'Both on disk.'
        : e.models
          ? 'On disk · nothing serving them'
          : missingBytes
            ? `${formatBytes(missingBytes)} left to fetch`
            : 'Waits for the engine.'
    } as Record<string, string>;
  });

  // ── the queue of models one button asked for ────────────────────
  // It lives in localStorage, not in this component. A reload does not cancel the download —
  // the fetcher keeps going on purpose — so a queue held in component state would come back as
  // one model installed and the other waiting forever. It is never re-derived from "what is
  // missing": that would fetch gigabytes nobody asked for on a page load.
  const KEY = 'libris.setup.queue';
  let queue = $state<string[]>(read());

  function read(): string[] {
    try {
      const raw = localStorage.getItem(KEY);
      const parsed = raw ? (JSON.parse(raw) as unknown) : [];
      return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : [];
    } catch {
      return [];
    }
  }

  function write(next: string[]) {
    queue = next;
    try {
      localStorage.setItem(KEY, JSON.stringify(next));
    } catch {
      // A view state that cannot be remembered is a view state that has to be started again.
    }
  }

  let installing = false;
  /**
   * Whether the queue is allowed to ask for anything: set when a download is stopped or a
   * request fails, cleared by the button. Without it the poll restarts a download the person
   * just stopped, and a server that keeps refusing one gets asked again every two seconds.
   */
  let paused = false;

  /**
   * One button means both models, so something has to ask for the second. The queue runs only
   * while the screen is showing the step that started it — a stale queue from a previous session
   * once fired while the person was still downloading the engine. What it waits for is the end
   * of the transfer in flight, not the end of the request that started it: the POST answers as
   * soon as the bytes are underway, so a resolved request means nothing yet, and asking for the
   * second model early is a refusal the server is obliged to make.
   */
  function drain() {
    if (!queue.length || active !== 'Models' || installing || paused || modelRunning) return;
    const next = queue.find((id) => entries.some((e) => e.id === id && !e.complete));
    if (!next) {
      write([]);
      return;
    }
    installing = true;
    void api
      .installModel(next)
      .then((view) => {
        models = view;
        write(queue.filter((id) => id !== next));
      })
      .catch((e) => {
        // A refusal is not a cancelled plan: what is queued is still missing, and throwing the
        // queue away would make one busy answer the end of the second model. It stops the queue
        // and says so, and the button starts it again.
        error = errorMessage(e);
        paused = true;
      })
      .finally(() => {
        installing = false;
      });
  }

  /** Stop is "not now": the bytes stay, and the same button that started the download is what
   *  resumes it — the queue still holds whatever is missing. */
  async function stopModels() {
    paused = true;
    await act(api.stopModelDownload);
  }

  /** The wizard unmounts the moment the gate opens, which is while this function is still
   *  running: the rest of its answers arrive with no wizard left to write them on. */
  let alive = true;

  async function load() {
    try {
      const e = await api.getEngine();
      if (!alive) return;
      engine = e;
      // The gate is the server's rule. When the last missing fact lands, the app is let in.
      applyGate(e.setup);
    } catch (e) {
      if (alive) error = errorMessage(e);
    }
    if (!alive) return;
    try {
      const m = await api.getModels();
      if (alive) models = m;
    } catch {
      // The models card says nothing rather than denying a catalogue it could not read.
    }
    // The device list is a property of the installed binary, so it follows the engine's tag
    // and never the clock.
    const tag = engine?.installed?.tag ?? null;
    if (tag !== askedFor) {
      askedFor = tag;
      try {
        const d = tag ? await api.getDevices(false) : undefined;
        if (alive) report = d;
      } catch {
        if (alive) report = undefined;
      }
    }
    if (alive) drain();
  }

  let askedFor: string | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;

  onMount(() => {
    void load();
    // Short and cheap, and only while the wizard is the screen: panels elsewhere use
    // job-scoped timers instead.
    timer = setInterval(() => void load(), 2000);
    return () => {
      alive = false;
      if (timer) clearInterval(timer);
    };
  });

  async function act(fn: () => Promise<unknown>) {
    busy = true;
    error = '';
    try {
      await fn();
      await load();
    } catch (e) {
      if (alive) error = errorMessage(e);
    } finally {
      if (alive) busy = false;
    }
  }

  /** Choosing records the pick *and* the acknowledgement the server said it would accept,
   *  sent back unchanged. What "confirmed for this engine on this machine" means is the
   *  server's rule, and a page that computes it is a second copy of it. The key is asked for
   *  when the screen does not hold one: the slots render as soon as a device list exists, and
   *  the engine view can be a poll older than the fingerprint — a click in that window saved a
   *  blank key, the server accepted it, and the step never moved. */
  async function choose(name: string) {
    busy = true;
    error = '';
    try {
      const got = await ackForConfirm(engine);
      if (alive) engine = got.engine;
      app.settings.engineDevice = name;
      app.settings.engineDeviceAck = got.ack;
      await saveSettings();
      await load();
    } catch (e) {
      if (alive) error = errorMessage(e);
    } finally {
      if (alive) busy = false;
    }
  }

  /** A device list that failed to arrive once does not arrive again on its own — the poll
   *  asks for one when the installed build's tag changes, and the tag has not. So the dead
   *  end carries the asking. */
  async function askDevices() {
    try {
      const got = await api.getDevices(true);
      if (alive) report = got;
    } catch (e) {
      if (alive) error = errorMessage(e);
    }
  }

  function getModels() {
    paused = false;
    write(wanted.map((e) => e.id));
    return act(() =>
      api
        .installModel(wanted[0]?.id ?? '')
        .then((view) => {
          models = view;
          write(queue.filter((id) => id !== wanted[0]?.id));
        })
        .catch((e) => {
          error = errorMessage(e);
          paused = true;
        })
    );
  }
</script>

<div class="setup">
  <header>
    <span class="mark" aria-hidden="true"><Icon name="book" /></span>
    <span class="word">libris</span>
    <h1>Set up Libris</h1>
    <span class="stepcount">{stepIndex + 1} of {steps.length}</span>
  </header>

  <div class="column">
    <ol class="steps">
      {#each steps as name (name)}
        {@const done =
          name === 'Engine'
            ? setup?.engine
            : name === 'Inference device'
              ? setup?.device
              : setup?.models && setup?.serving}
        {@const back = reach(name)}
        <li class:active={active === name} class:done={done === true}>
          <button class="step" disabled={!back} onclick={() => (chosen = name)}>
            <span class="step-dot" aria-hidden="true">
              {#if done}<Icon name="check" size={12} />{:else}{steps.indexOf(name) + 1}{/if}
            </span>
            <span class="name">{name}</span>
            <span class="detail">{detail[name]}</span>
          </button>
        </li>
      {/each}
    </ol>

    <section class="work">
      {#if active === 'Engine'}
        <h2>Get the model server</h2>
        <p>
          Answers run on llama.cpp, on this machine. Libris downloads the official build, verifies
          it, and starts it when needed.
        </p>
        <div class="verbs">
          <Button
            variant="primary"
            disabled={busy || downloading || (!downloaded && !engine?.latest)}
            onclick={() =>
              act(() => api.installEngine(engine?.update_available ? engine?.latest?.tag : undefined))}
          >
            {downloading
              ? 'Downloading…'
              : engine?.update_available
                ? `Update to ${engine.latest?.tag}`
                : 'Download llama.cpp'}
          </Button>
          {#if downloading}
            <Button size="sm" title="Stop the download. The bytes so far are kept, and the next attempt resumes where they stopped."
              onclick={() => act(api.stopEngine)}>
              Stop
            </Button>
          {:else if downloaded}
            <span class="quiet">Installed · {engine?.installed?.tag}</span>
          {:else if engine?.checking}
            <span class="quiet">Checking what the newest build is…</span>
          {:else if engine?.latest}
            <span class="quiet">{formatBytes(engine.asset?.size ?? 0)}</span>
          {/if}
        </div>

        {#if failed}
          <p class="error" role="alert">
            {engine?.progress?.error || 'The download failed.'}
          </p>
          {#if engine?.dir}
            <p class="quiet">Nothing was installed. The files go in {engine.dir}.</p>
          {/if}
        {:else if engine?.check_error}
          <p class="quiet">The newest build is not known right now: {engine.check_error}</p>
        {:else if !downloaded && !engine?.latest && !downloading}
          <p class="quiet">Asking llama.cpp what its newest build is…</p>
        {/if}

      {:else if active === 'Inference device'}
        <h2>Which device should run the models?</h2>
        <p>
          Pick your graphics card if one is listed, otherwise CPU. You can change this later in
          Settings.
        </p>
        {#if !report}
          <p class="quiet">Asking the engine what it can run on…</p>
          <div class="verbs">
            <Button size="sm" disabled={busy} onclick={() => act(askDevices)}>Try again</Button>
          </div>
        {:else if !devices.length}
          <!-- The step has to stay escapable, and a slot list with nothing in it escapes
               nowhere. So this asks again rather than offering a choice the engine never
               named: the CPU is a row in every report, so a report with no rows at all is a
               probe that did not answer, not a machine without a processor. -->
          <p class="quiet">The engine reported no device at all.</p>
          <div class="verbs">
            <Button size="sm" disabled={busy} onclick={() => act(askDevices)}>Try again</Button>
          </div>
        {:else}
          <div class="devices">
            {#each devices as d (d.name)}
              <button class="slot" class:on={app.settings.engineDevice === d.name} onclick={() => choose(d.name)}>
                <span class="slot-name">{d.name}</span>
                <span class="slot-desc">
                  {d.description ?? d.kind}{#if d.total_bytes} · {formatBytes(d.total_bytes)}{/if}
                </span>
              </button>
            {/each}
          </div>
        {/if}

      {:else}
        <h2>Get the models</h2>
        {#if !modelsOnDisk}
          <div class="verbs">
            <Button
              variant="primary"
              disabled={busy || (!modelRunning && !wanted.length)}
              title={modelRunning
                ? 'Stop. Progress is kept and the download resumes where it stopped.'
                : undefined}
              onclick={modelRunning ? stopModels : getModels}>
              {modelRunning ? 'Stop' : 'Download'}
            </Button>
            {#if modelRunning}
              <span class="quiet">{formatBytes(missingBytes)} left to fetch</span>
            {:else if !wanted.length && entries.length}
              <span class="quiet">Both are on disk.</span>
            {/if}
          </div>
          <ul class="models">
            {#each entries as e (e.id)}
              <li>
                <span class="role">{e.role}</span>
                <span class="mname">{e.label}</span>
                <span class="quiet">{e.quant} · {formatBytes(e.bytes)}</span>
                <span class="state">
                  {e.complete
                    ? 'installed'
                    : e.downloading
                      ? 'in flight'
                      : engine?.progress?.state === 'verifying'
                        ? 'verifying'
                        : e.partialBytes
                          ? 'waiting'
                          : 'waiting'}
                </span>
              </li>
            {/each}
          </ul>
          <p class="quiet">A stopped download keeps its bytes and resumes where it left off.</p>
        {:else}
          <p>Both models are on disk. The server loads one the first time a question needs it.</p>
          <div class="verbs">
            <Button variant="primary" disabled={busy} onclick={() => act(api.startEngineServe)}>
              Start the server
            </Button>
          </div>
        {/if}
      {/if}

      {#if progress}
        <div
          class="bar"
          role="progressbar"
          aria-valuemin="0"
          aria-valuemax="100"
          aria-valuenow={pct}
          aria-label={progress.state}
        >
          <span style="width: {pct}%"></span>
        </div>
        <p class="quiet">
          {progress.state === 'verifying'
            ? 'Checking the digest'
            : progress.state === 'unpacking'
              ? 'Unpacking'
              : `${pct}%`}
          {#if progress.total} · {formatBytes(progress.done)} of {formatBytes(progress.total)}{/if}
        </p>
      {/if}

      {#if error}
        <p class="error" role="alert">{error}</p>
      {/if}

      <div class="escape">
        <Button
          klass="skip"
          variant="ghost"
          size="sm"
          title="Go in without a model server. This configures nothing and starts nothing: asking a question will fail until a server answers, and the setup screen is here again the next time libris opens."
          onclick={skipSetup}>Skip for now</Button>
      </div>
    </section>
  </div>
</div>

<style>
  .setup {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
  }

  header {
    display: flex;
    align-items: center;
    gap: 8px;
    height: var(--header-h);
    flex: none;
    padding: 0 14px;
    border-bottom: 1px solid var(--border);
    background: var(--chrome);
  }

  /* The way out sits under the step's own verbs rather than in the header, where it read as
     app chrome next to the step counter. Here it is one more thing you can do about this
     screen — the least inviting of them. */
  .escape {
    display: flex;
    margin-block-start: 2px;
  }

  .mark {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    color: var(--accent);
  }

  .word {
    font-weight: 600;
    font-size: var(--fs);
  }

  h1 {
    flex: 1;
    font-size: var(--fs-sm);
    font-weight: 500;
    color: var(--muted);
    padding-left: 6px;
  }

  /* Not the shared `.count` pill: a step counter is a sentence, not a number in a chip. */
  .stepcount {
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .column {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    width: min(100%, 46rem);
    margin-inline: auto;
    padding: 22px 24px 40px;
    display: grid;
    gap: 18px;
    align-content: start;
  }

  .steps {
    margin: 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 4px;
  }

  /* The whole row is the control, since the row is what you go back to. */
  .steps li {
    display: block;
  }

  .step {
    display: grid;
    grid-template-columns: 22px 1fr auto;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 9px 12px;
    border: 1px solid transparent;
    border-radius: var(--r);
    background: none;
    color: inherit;
    font: inherit;
    text-align: start;
    cursor: pointer;
  }

  /* Ahead of the blocking step, a row does not answer a click. */
  .step:disabled {
    cursor: default;
    opacity: 0.55;
  }

  .step:not(:disabled):hover {
    background: var(--hover);
  }

  /* The active step is raised; a finished one is a tick and nothing else. */
  .steps li.active .step {
    border-color: var(--border);
    background: var(--raised);
    box-shadow: var(--shadow);
  }

  /* Named apart from the global `.dot` in app.css. That one is the 7px amber panel indicator,
     and it painted these circles: neither rule set a background, so the amber showed through
     whatever border and glyph this one asked for. */
  .step-dot {
    display: grid;
    place-items: center;
    width: 20px;
    height: 20px;
    border: 1px solid var(--border);
    border-radius: 50%;
    background: transparent;
    font-size: 11px;
    color: var(--muted);
  }

  /* Three states, told apart without reading a word: done is a filled tick, current is the
     accent on the raised row, and a step not reached is an outline around a number. */
  .done .step-dot {
    border-color: var(--success);
    background: var(--success-bg);
    color: var(--success);
  }

  .active .step-dot {
    border-color: var(--accent);
    color: var(--accent-text);
  }

  .name {
    font-size: var(--fs-sm);
    font-weight: 500;
  }

  .detail {
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .work {
    display: grid;
    gap: 8px;
    padding: 16px;
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    background: var(--raised);
  }

  .work h2 {
    font-size: var(--fs);
    font-weight: 600;
  }

  .work p {
    font-size: var(--fs-sm);
    line-height: 1.6;
    color: var(--muted);
  }

  .verbs {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }

  .quiet {
    font-size: var(--fs-xs) !important;
    color: var(--muted) !important;
  }

  .error {
    color: var(--danger);
  }

  .devices {
    display: grid;
    gap: 4px;
  }

  .slot {
    display: grid;
    gap: 1px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--r);
    background: var(--bg);
    text-align: left;
    color: var(--text);
    font: inherit;
  }

  .slot:hover {
    border-color: var(--muted);
  }

  .slot.on {
    border-color: var(--accent);
    box-shadow: inset 0 0 0 1px var(--accent);
  }

  .slot-name {
    font-size: var(--fs-sm);
    font-weight: 600;
  }

  .slot-desc {
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .models {
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 4px;
  }

  .models li {
    display: flex;
    align-items: baseline;
    gap: 8px;
    font-size: var(--fs-xs);
  }

  /* `.role` is the shared rule in app.css — one pill, spelled once. */

  .mname {
    font-size: var(--fs-sm);
  }

  .state {
    margin-left: auto;
    color: var(--muted);
  }

  .bar {
    height: 4px;
    border-radius: 2px;
    background: var(--chrome);
    overflow: hidden;
  }

  .bar > span {
    display: block;
    height: 100%;
    background: var(--accent);
  }
</style>
