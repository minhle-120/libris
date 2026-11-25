<script lang="ts">
  import { onMount } from 'svelte';
  import * as api from '../../lib/api';
  import { app } from '../../lib/store.svelte';
  import { settingGroups } from '../../lib/config.gen';
  import { errorMessage, formatDate, formatBytes, formatSince } from '../../lib/format';
  import { ackForConfirm } from '../../lib/device';
  import type { DeviceReport, EngineView } from '../../lib/types';
  import Badge from '../../lib/ui/Badge.svelte';
  import Button from '../../lib/ui/Button.svelte';
  import Modal from '../../lib/ui/Modal.svelte';

  /**
   * The machine section: facts and verbs, in the group that names them.
   *
   * Everything else on the settings page is a choice libris can apply. What is installed is a
   * fact, what is new upstream is somebody else's fact, and what this box can run is a fact
   * about the installed engine — so none of it is a setting, and none of it comes from the
   * settings endpoint.
   */
  let { saved, modelsReady }: { saved: number; modelsReady: boolean } = $props();

  let engine = $state<EngineView>();
  let report = $state<DeviceReport>();
  let error = $state('');
  let busy = $state(false);
  let confirming = $state(false);
  let modalError = $state('');
  /** The tag the device report was asked for. The report is re-asked when the installed
   *  build's tag changes, never on a timer — the list is a property of the binary, not of
   *  the minute. */
  let askedFor: string | null = null;
  /** A request outlives the section that sent it whenever the mode switch takes the section
   *  away mid-flight. The signals it would write belong to an effect that no longer exists, so
   *  the answer is thrown away rather than read through a destroyed derived. */
  let alive = true;

  const busyStates = new Set(['downloading', 'verifying', 'unpacking']);
  const polling = $derived(
    !!engine && (busyStates.has(engine.progress?.state ?? '') || engine.checking === true)
  );

  /** A device list exists only once something is installed to print it: a page that offers
   *  "Vulkan0" before anything has asked the hardware is asking someone to type a name they
   *  can only learn from a command line. */
  const installed = $derived(!!engine?.installed);
  const pickable = $derived(installed && (report?.devices.length ?? 0) > 0);
  const cpuOnly = $derived(
    (report?.devices ?? []).filter((d) => d.kind && d.kind !== 'cpu')
  );
  const serve = $derived(engine?.serve);
  const address = $derived(
    serve?.running
      ? `127.0.0.1:${serve.port}`
      : engine?.port
        ? `127.0.0.1:${engine.port}`
        : 'a free port, chosen each time it starts'
  );
  const pct = $derived(
    engine?.progress?.total
      ? Math.min(100, Math.round((engine.progress.done / engine.progress.total) * 100))
      : 0
  );

  async function load(check = false) {
    try {
      const got = await api.getEngine(check);
      if (!alive) return;
      engine = got;
      error = '';
    } catch (e) {
      if (alive) error = errorMessage(e);
    }
  }

  async function loadDevices() {
    try {
      const got = await api.getDevices(false);
      if (alive) report = got;
    } catch {
      // An unavailable device list is not a failure worth shouting about; the row goes quiet.
      if (alive) report = undefined;
    }
  }

  async function askDevices() {
    try {
      const got = await api.getDevices(true);
      if (alive) report = got;
    } catch (e) {
      if (alive) error = errorMessage(e);
    }
  }

  onMount(() => {
    void load();
    void loadDevices();
    return () => {
      alive = false;
      if (timer) clearInterval(timer);
    };
  });

  let timer: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    const go = polling;
    void saved;
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
    // Once a second while there is a job, and not once afterwards: an open settings page must
    // not quietly ask the server forever.
    if (go) timer = setInterval(() => void load(), 1000);
  });

  $effect(() => {
    const tag = engine?.installed?.tag ?? null;
    if (askedFor === null) {
      askedFor = tag;
      return;
    }
    if (tag !== askedFor) {
      askedFor = tag;
      void loadDevices();
    }
  });

  async function act(fn: () => Promise<unknown>, thenLoad = true) {
    busy = true;
    error = '';
    try {
      await fn();
      if (thenLoad) await load();
    } catch (e) {
      if (alive) error = errorMessage(e);
    } finally {
      if (alive) busy = false;
    }
  }

  /** The name and the key that records it are taken together, and the key comes from the
   *  endpoint that computes it rather than from whatever the first poll carried — see
   *  lib/device.ts. A choice recorded against a blank key is a choice the server never heard. */
  async function pick(name: string) {
    try {
      const got = await ackForConfirm(engine);
      if (!alive) return;
      engine = got.engine;
      app.settings.engineDevice = name;
      app.settings.engineDeviceAck = got.ack;
    } catch (e) {
      if (alive) error = errorMessage(e);
    }
  }

  // The label and the help are the generated field's. Nothing here defines a setting, not even
  // a label for one.
  const deviceLabel = $derived(
    settingGroups.flatMap((g) => g.fields).find((f) => f.key === 'engineDevice')
  );
</script>

<section class="engine">
  {#if error}
    <p class="line error" role="alert">{error}</p>
  {/if}

  <dl class="facts">
    <div>
      <dt>On this machine</dt>
      <dd>
        {#if engine?.installed}
          {engine.installed.tag} · {formatBytes(engine.installed.bytes)} ·{' '}
          {engine.installed.backend} · installed {formatDate(engine.installed.installed_at)}
        {:else}
          Nothing downloaded yet.
        {/if}
      </dd>
    </div>

    <div>
      <dt>Latest build</dt>
      <dd>
        {#if engine?.checking}
          Checking…
        {:else if engine?.latest}
          {engine.latest.tag}
          {#if engine.asset}
            · this machine's file {engine.asset.name}, {formatBytes(engine.asset.size)}
          {/if}
          {#if engine.update_available}
            — an update is available
          {:else}
            — this is the current build
          {/if}
        {:else if engine?.check_error}
          Not known right now: {engine.check_error}
        {:else}
          Unknown.
        {/if}
      </dd>
    </div>

    <div>
      <dt>Stored in</dt>
      <dd title={engine?.dir}>{engine?.dir ?? ''}</dd>
    </div>

    <div>
      <dt>Model server</dt>
      <dd>
        {#if !installed}
          Not started — there is nothing to run until the engine is downloaded, and no port is
          held before a process exists to hold one.
        {:else}
          <span class="inline-flex">
            <Badge
              tone={serve?.running ? 'success' : serve?.error ? 'danger' : 'neutral'}
              title={serve?.pid ? `Process ${serve.pid}, up since ${serve.started_at}` : undefined}
            >
              {serve?.running ? 'running' : serve?.error ? 'it did not start' : 'stopped'}
            </Badge>
            <span title="The address a request would reach">{address}</span>
            {#if serve?.running && serve.device}
              <span title="The device the running process is on">on {serve.device}</span>
            {/if}
          </span>
          <span class="inline-flex verbs">
            {#if serve?.running}
              <Button
                size="sm"
                title="Stop the server"
                disabled={busy}
                onclick={() => act(api.stopEngineServe)}
              >
                Stop the server
              </Button>
            {:else}
              <Button
                variant="primary"
                size="sm"
                title={modelsReady
                  ? 'Start the server'
                  : 'Start the server — download a model first'}
                disabled={busy || !modelsReady}
                onclick={() => act(api.startEngineServe)}
              >
                Start the server
              </Button>
            {/if}
          </span>
          {#if serve?.running}
            {#if serve.models?.length}
              <ul class="loaded">
                {#each serve.models as m (m)}
                  <li title={m}>{m}</li>
                {/each}
              </ul>
            {:else}
              <p class="line">
                Nothing loaded yet — the server reads a model the first time a question asks for
                one. See <a href="#models">Models</a>.
              </p>
            {/if}
            {#if app.settings.engineDevice && serve.device && app.settings.engineDevice !== serve.device}
              <p class="line">
                The form is holding {app.settings.engineDevice}; the process is on{' '}
                {serve.device}. Saving and restarting switches it.
              </p>
            {/if}
            <p class="line">Applies on the next start, and is saved with the rest of this page.</p>
          {/if}
          {#if serve?.error}
            <p class="line error" role="alert">{serve.error}</p>
            {#if serve.log}<p class="line">Its own log: <code>{serve.log}</code></p>{/if}
          {/if}
        {/if}
      </dd>
    </div>

    {#if pickable}
      <div>
        <dt>{deviceLabel?.label ?? 'Run models on'}</dt>
        <dd>
          <p class="label">{deviceLabel?.help}</p>
          <div class="slots">
            {#each report?.devices ?? [] as d (d.name)}
              <button
                class="slot"
                class:on={app.settings.engineDevice === d.name}
                onclick={() => pick(d.name)}
              >
                <span class="name">{d.name}</span>
                <span class="desc">
                  {d.description ?? d.kind}
                  {#if d.total_bytes}· {formatBytes(d.total_bytes)}{/if}
                </span>
              </button>
            {/each}
          </div>
        </dd>
      </div>
    {/if}

    <div>
      <dt>Detected</dt>
      <dd>
        {#if !installed}
          Nothing has been downloaded yet, so this machine has never been asked what it can run
          on. Download the build below, and it will be.
        {:else if !report}
          The device report is not available right now.
        {:else if cpuOnly.length === 0}
          No graphics device in this box, so models run on the CPU.
        {:else}
          <span class="chips">
            {#each cpuOnly as d (d.name)}
              <Badge
                chip
                title={d.free_bytes ? `${formatBytes(d.free_bytes)} free of ${formatBytes(d.total_bytes ?? 0)}` : undefined}
              >
                {d.description ?? d.name} · {formatBytes(d.total_bytes ?? 0)}
              </Badge>
            {/each}
          </span>
        {/if}
        {#if report?.devices.length}
          <span class="inline-flex">
            <Button size="sm" title="Ask again, skipping the cache" onclick={() => void askDevices()}>
              Probe again
            </Button>
            <span class="line">
              {report?.cached ? 'Read from cache' : 'Asked just now'} ·{' '}
              {formatSince(report?.checked_at ? String(report.checked_at) : undefined)}
            </span>
          </span>
        {/if}
      </dd>
    </div>
  </dl>

  {#if report?.warnings?.length}
    <ul class="warnings">
      {#each report.warnings as w (w)}<li>{w}</li>{/each}
    </ul>
  {/if}

  {#if polling}
    <div
      class="bar"
      role="progressbar"
      aria-valuemin="0"
      aria-valuemax="100"
      aria-valuenow={pct}
      aria-label={engine?.progress?.state}
    >
      <span style="width: {pct}%"></span>
    </div>
    <p class="line">
      {engine?.progress?.state === 'verifying'
        ? 'Checking the digest'
        : engine?.progress?.state === 'unpacking'
          ? 'Unpacking'
          : `Downloading ${engine?.progress?.tag ?? ''}`}
      {#if engine?.progress?.asset} · {engine.progress.asset}{/if}
      {#if engine?.progress?.resumed} · continuing where it stopped{/if}
      {#if engine?.progress?.total}
        · {formatBytes(engine.progress.done)} of {formatBytes(engine.progress.total)} ({pct}%)
      {/if}
    </p>
  {:else if engine?.progress?.state === 'failed' && engine.progress.error}
    <p class="line error" role="alert">{engine.progress.error}</p>
  {/if}

  <div class="actions">
    {#if polling}
      <Button
        size="sm"
        title="Stop the download. The bytes so far are kept, and the next attempt resumes where they stopped."
        onclick={() => act(api.stopEngine)}
      >
        Stop
      </Button>
      {#if engine?.parts_bytes}
        <p class="line">{formatBytes(engine.parts_bytes)} downloaded so far.</p>
      {/if}
    {/if}

    {#if !polling && engine?.parts_bytes}
      <Button
        size="sm"
        title="Throw the partial download away and free the space"
        onclick={() => act(api.discardEngineParts)}
      >
        Discard {formatBytes(engine.parts_bytes)}
      </Button>
    {/if}

    {#if !polling}
      {#if engine?.checking}
        <Button size="sm" disabled>Checking…</Button>
      {:else if engine?.update_available}
        <Button variant="primary" size="sm" onclick={() => act(() => api.installEngine(engine?.latest?.tag))}>
          Update to {engine?.latest?.tag}
        </Button>
      {:else if installed}
        <Button size="sm" onclick={() => act(() => api.getEngine(true))}>Check for updates</Button>
        <p class="line">Up to date{engine?.installed ? ` · ${formatDate(engine.installed.installed_at)}` : ''}.</p>
      {:else}
        <Button variant="primary" size="sm" onclick={() => act(() => api.installEngine())}>
          <!-- Named by what it downloads: a button that says only "Download" hides how many
             megabytes the click is about to ask for. -->
          Download{engine?.latest ? ` ${engine.latest.tag}` : ''}
        </Button>
      {/if}
    {/if}

    {#if installed && !polling}
      <Button
        variant="danger"
        size="sm"
        title="Delete the engine from this machine"
        onclick={() => (confirming = true)}
      >
        Remove
      </Button>
    {/if}
  </div>

  <p class="footnote">
    The model server is a separate program, libris's own llama.cpp, downloaded from upstream and
    run here. Its sampling and context sizes are fixed by that build, not by these settings.
  </p>
</section>

<Modal
  klass="remove-engine"
  open={confirming}
  onclosed={() => (confirming = false)}
  title="Remove llama.cpp from this machine?"
  confirm="Remove"
  variant="danger"
  error={modalError}
  onconfirm={async () => {
    try {
      await api.removeEngine();
      confirming = false;
      modalError = '';
      await load();
      await loadDevices();
    } catch (e) {
      modalError = errorMessage(e);
    }
  }}
  keeps="Nothing else is touched: your notebooks, sources and answers stay. Re-downloading takes about a minute."
>
  <p class="lead">
    The engine files are deleted from this disk, and questions stop until it is downloaded again.
  </p>
</Modal>

<style>
  .engine {
    border-top: 1px solid var(--border);
    padding: 4px 16px 14px;
  }

  .facts {
    margin: 0;
    display: grid;
    gap: 2px;
  }

  /* min-width: 0 on both columns: the values are paths and device names, one unbreakable token
     long, and a grid track's minimum is its content's minimum — one long path once made the row
     wider than the card and clipped every value in it. */
  .facts > div {
    display: grid;
    grid-template-columns: 11rem minmax(0, 1fr);
    gap: 4px 14px;
    align-items: baseline;
    min-width: 0;
    padding: 8px 0;
    border-bottom: 1px solid var(--border-soft);
  }

  .facts > div:last-child {
    border-bottom: 0;
  }

  dt {
    font-size: var(--fs-xs);
    font-weight: 600;
    color: var(--muted);
  }

  dd {
    min-width: 0;
    margin: 0;
    font-size: var(--fs-sm);
    overflow-wrap: anywhere;
  }

  .inline-flex {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }

  .verbs {
    margin-left: auto;
  }

  .line {
    font-size: var(--fs-xs);
    color: var(--muted);
    padding-top: 3px;
  }

  .line.error,
  .error {
    color: var(--danger);
  }

  .loaded {
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 2px;
  }

  .loaded li {
    font-size: var(--fs-xs);
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .label {
    font-size: var(--fs-xs);
    color: var(--muted);
    padding-bottom: 6px;
  }

  .slots {
    display: grid;
    gap: 4px;
  }

  .slot {
    display: grid;
    gap: 1px;
    padding: 6px 9px;
    border: 1px solid var(--border);
    border-radius: var(--r);
    background: var(--raised);
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

  .slot .name {
    font-size: var(--fs-sm);
    font-weight: 500;
  }

  .slot .desc {
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .chips {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 5px;
  }

  .warnings {
    margin: 6px 0 0;
    padding: 0;
    list-style: none;
    display: grid;
    gap: 2px;
  }

  .warnings li {
    font-size: var(--fs-xs);
    color: var(--attention);
  }

  .bar {
    height: 4px;
    margin-top: 10px;
    border-radius: 2px;
    background: var(--chrome);
    overflow: hidden;
  }

  .bar > span {
    display: block;
    height: 100%;
    background: var(--accent);
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    padding-top: 10px;
  }

  .footnote {
    padding-top: 10px;
    font-size: var(--fs-xs);
    color: var(--muted);
    line-height: 1.55;
  }
</style>
