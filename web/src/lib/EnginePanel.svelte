<script lang="ts">
  import { onMount } from 'svelte';
  import {
    discardEngineParts,
    getDevices,
    getEngine,
    installEngine,
    removeEngine,
    startEngineServe,
    stopEngine,
    stopEngineServe
  } from '../lib/api';
  import type { DeviceReport, EngineView } from '../lib/types';
  import { formatBytes, formatDate, formatSince } from '../lib/format';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';
  import ConfirmDialog from '../lib/ConfirmDialog.svelte';

  /**
   * The machine itself, as distinct from the settings above it.
   *
   * Everything else on this page is a choice libris can apply. This is not: what is installed
   * is a fact, what is new is somebody else's fact, and what the box can run is a fact about
   * the installed llama.cpp — the only thing libris asks (ADR 0019). So the section is read
   * from /api/host/* and its controls are verbs —
   * download, stop, remove — with the two confirmation rules the rest of the app already
   * follows: a destructive action asks first, and Cancel is the button Enter lands on.
   */
  let {
    saved = 0
  }: {
    /** Bumped by the parent on every save, which is what makes this refetch: the port and
     *  the mode are settings, so the section must not still be showing the port that was
     *  saved before the one being typed. */
    saved?: number;
  } = $props();

  let view = $state<EngineView | null>(null);
  let devices = $state<DeviceReport | null>(null);
  let loadError = $state('');
  let actionError = $state('');
  let started = $state(false);
  let confirming = $state(false);
  let removing = $state(false);
  // The start request is as long as the first model load, so this is the only thing between
  // "Start" and an answer. The polling effect below keeps asking about the download state,
  // not this one — the process is either up or it is not, and there is no percentage of a
  // model load to show.
  let serving = $state(false);

  const busy = $derived(
    view?.progress.state === 'downloading' ||
      view?.progress.state === 'verifying' ||
      view?.progress.state === 'unpacking'
  );
  const installed = $derived(view?.installed);
  // A check in flight is not a failure, and the page must not say it is: the newest build is
  // on its way and this section asks again. Collapsing the two is how an app teaches people
  // to press "check again" repeatedly while it is already working.
  const checking = $derived(view?.checking === true);
  const latest = $derived(view?.latest);
  const asset = $derived(view?.asset);
  const serve = $derived(view?.serve);
  // The report always carries a CPU row, so "the machine has no GPU" is the *absence* of a
  // non-CPU row rather than an empty list — and the list length cannot be the test.
  const gpus = $derived((devices?.devices ?? []).filter((d) => d.kind !== 'cpu'));

  onMount(() => {
    void refresh(false);
    void refreshDevices(false);
  });

  // The engine is the only thing libris asks about the machine (ADR 0019), so the answer
  // about what this box can run on does not exist until a download lands, and changes when a
  // newer one does. The server drops its cached report on install and on remove; this is the
  // half that asks again, so the row fills in without a reload. The `undefined` guard is what
  // keeps the first paint from being counted as a change: while the view is loading there is
  // no tag to compare, and refetching then would skip a cache that is perfectly good.
  let lastTag: string | undefined;
  $effect(() => {
    const tag = view?.installed?.tag;
    if (tag === undefined) return;
    if (lastTag !== undefined && tag !== lastTag) void refreshDevices(true);
    lastTag = tag;
  });

  // A saved build selection changes what this section should offer, so it reloads. Watching
  // the counter rather than the value is deliberate: the prop is the draft in the form, and
  // refetching on every keystroke would fight the person typing.
  let first = true;
  $effect(() => {
    void saved;
    if (first) {
      first = false;
      return;
    }
    void refresh(false);
  });

  // The download is polled, not streamed: the app already polls a source that is processing,
  // and a bar that reconnects itself to an event stream would be a second way to be out of
  // date. The timer stops the moment the state is final, so an open settings page is not
  // quietly asking the server once a second forever.
  $effect(() => {
    if (!busy && !checking) return;
    const timer = setInterval(() => void refresh(false), 1000);
    return () => clearInterval(timer);
  });

  async function refresh(check: boolean) {
    try {
      view = await getEngine(check);
      if (!check) actionError = '';
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
    }
  }

  async function refreshDevices(refresh: boolean) {
    try {
      devices = await getDevices(refresh);
    } catch {
      // The device list is the least important thing on the page and the most likely to be
      // unavailable on an unusual machine. An empty list with no sentence is the failure; an
      // empty list that says which probe failed is a report.
      devices = null;
    }
  }

  async function download() {
    actionError = '';
    started = true;
    try {
      // No tag: "the newest" is resolved server-side, by the same code that answered the
      // release list this section is showing.
      await installEngine();
      await refresh(false);
    } catch (e) {
      actionError = e instanceof Error ? e.message : String(e);
      started = false;
    }
  }

  async function stop() {
    actionError = '';
    try {
      await stopEngine();
      await refresh(false);
    } catch (e) {
      actionError = e instanceof Error ? e.message : String(e);
    }
  }

  async function startServe() {
    actionError = '';
    serving = true;
    try {
      await startEngineServe();
    } catch (e) {
      // The server already put the router's own complaint in the message — a port somebody
      // else holds, a model that will not load — so it is shown, not paraphrased.
      actionError = e instanceof Error ? e.message : String(e);
    } finally {
      serving = false;
      await refresh(false);
    }
  }

  async function stopServe() {
    actionError = '';
    serving = true;
    try {
      await stopEngineServe();
    } catch (e) {
      actionError = e instanceof Error ? e.message : String(e);
    } finally {
      serving = false;
      await refresh(false);
    }
  }

  async function discard() {
    actionError = '';
    try {
      await discardEngineParts();
      await refresh(false);
    } catch (e) {
      actionError = e instanceof Error ? e.message : String(e);
    }
  }

  async function remove() {
    actionError = '';
    removing = true;
    try {
      await removeEngine();
      confirming = false;
      removing = false;
      await refresh(false);
      // No refreshDevices here: removing the engine changes the tag the effect above
      // watches, and one rule about when the machine is re-asked beats two.
    } catch (e) {
      // 409 arrives here when a llama-server is holding the files. It is shown rather than
      // retried: the fix belongs to the person running the server.
      actionError = e instanceof Error ? e.message : String(e);
      removing = false;
    }
  }

  const percent = $derived(
    view && view.progress.total > 0
      ? Math.min(100, Math.round((view.progress.done / view.progress.total) * 100))
      : 0
  );

  const stateWord = $derived(
    view
      ? {
          idle: '',
          downloading: 'Downloading',
          verifying: 'Verifying the download',
          unpacking: 'Installing',
          done: 'Installed',
          failed: 'Failed'
        }[view.progress.state]
      : ''
  );
</script>

<div class="engine">
  {#if loadError}
    <p class="error" role="alert"><Icon name="alert" size={14} /> {loadError}</p>
  {/if}

  {#if !view && !loadError}
    <!-- While the view is in flight the section must not assert anything. "Nothing downloaded
         yet" is a fact about this machine, and the honest state before the answer arrives is
         that nothing is known — the same reason the app never renders a placeholder count. -->
    <p class="note"><Icon name="clock" size={14} /> Reading this machine…</p>
  {/if}

  {#if view}
  <dl class="facts">
    <div>
      <dt>On this machine</dt>
      <dd>
        {#if installed}
          <span class="tag">{installed.tag}</span>
          <span class="muted">{formatBytes(installed.bytes)}</span>
        {:else}
          <span class="muted">Nothing downloaded yet</span>
        {/if}
      </dd>
    </div>

    <div>
      <dt>Latest build</dt>
      <dd>
        {#if latest}
          <span class="tag">{latest.tag}</span>
          {#if asset}
            <span class="muted">{formatBytes(asset.size)} · {asset.name}</span>
          {/if}
          {#if view?.update_available}
            <span class="badge">update available</span>
          {:else if installed && latest.tag === installed.tag && !checking}
            <span class="muted">up to date</span>
          {/if}
          {#if checking}
            <span class="muted">checking…</span>
          {/if}
        {:else if checking}
          <span class="muted">checking…</span>
        {:else if view?.check_error}
          <span class="muted">not known right now</span>
        {:else}
          <span class="muted">unknown</span>
        {/if}
      </dd>
    </div>

    <div>
      <dt>Stored in</dt>
      <dd class="path" title={view?.dir}>{view?.dir ?? '…'}</dd>
    </div>

    {#if view}
      <div>
        <dt>Model server</dt>
        <dd>
          {#if !installed}
            <span class="muted">nothing to run until the engine is downloaded below</span>
          {:else if !serve}
            <span class="muted">this build has no data directory to run one from</span>
          {:else}
            <span class="tag" title="One llama.cpp in router mode answers all three model roles (ADR 0020)">
              {view.endpoint ?? `http://127.0.0.1:${serve.port}`}
            </span>
            {#if serve.running}
              <span class="badge">running · pid {serve.pid}</span>
            {:else if serve.error}
              <span class="badge warn">did not start</span>
            {:else}
              <span class="muted">stopped</span>
            {/if}
            <p class="models">
              {#each view.models ?? [] as model (model.path)}
                <span class="device" title={model.path}>
                  {model.alias}
                  <span class="muted">{formatBytes(model.bytes)}</span>
                </span>
              {:else}
                <span class="muted">
                  no model in {view.dir.replace(/\/bin$/, '')}/models yet — a chat model named
                  Qwen3.5-9B…-Q8_0.gguf
                </span>
              {/each}
            </p>
            {#if serve.error}
              <p class="error" role="alert"><Icon name="alert" size={14} /> {serve.error}</p>
            {/if}
            {#if serve.log}
              <p class="muted asof">the server writes what it did to {serve.log}</p>
            {/if}
          {/if}
        </dd>
      </div>
    {/if}

    {#if devices}
      <div>
        <dt>Detected</dt>
        <dd>
          {#if !installed}
            <span class="muted">asks llama.cpp once it is downloaded below</span>
          {:else if gpus.length === 0}
            <span class="muted">No graphics device — libris will run on the CPU</span>
          {:else}
            {#each devices.devices as device (device.name)}
              <span
                class="device"
                title={device.free_bytes
                  ? `${device.name} · ${formatBytes(device.free_bytes)} free`
                  : device.name}
              >
                {device.description || device.name}
                {#if device.total_bytes}
                  <span class="muted">{formatBytes(device.total_bytes)}</span>
                {/if}
              </span>
            {/each}
          {/if}
          <button
            class="link"
            type="button"
            onclick={() => void refreshDevices(true)}
            title="Ask the machine again, skipping the cache"
          >
            check again
          </button>
          <span class="muted asof">
            {#if devices.cached}
              from the last check
            {:else}
              just now
            {/if}
            {formatSince(devices.checked_at)} · {devices.os}/{devices.arch}
          </span>
        </dd>
      </div>
    {/if}
  </dl>
  {/if}

  {#if devices?.warnings?.length}
    <ul class="warnings">
      {#each devices.warnings as warning (warning)}
        <li><Icon name="alert" size={14} /> {warning}</li>
      {/each}
    </ul>
  {/if}

  {#if view?.check_error}
    <p class="note">
      <Icon name="alert" size={14} />
      {view.check_error}
      <button class="link" type="button" onclick={() => void refresh(true)}>check again</button>
    </p>
  {/if}

  {#if view && (busy || view.progress.state === 'failed' || view.progress.state === 'done')}
    <div class="progress" class:failed={view.progress.state === 'failed'}>
      <div class="line">
        <span>
          {stateWord}
          {#if view.progress.tag}
            <span class="tag">{view.progress.tag}</span>
          {/if}
          {#if view.progress.resumed && busy}
            <span class="muted">— continuing where it stopped</span>
          {/if}
        </span>
        {#if view.progress.total > 0}
          <span class="muted">
            {formatBytes(view.progress.done)} / {formatBytes(view.progress.total)}
            {#if percent}<span class="pct">{percent}%</span>{/if}
          </span>
        {/if}
      </div>
      <div
        class="bar"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={busy ? percent : view.progress.state === 'failed' ? 0 : 100}
      >
        <span style="width: {view.progress.state === 'failed' ? 0 : percent}%"></span>
      </div>
      {#if view.progress.error}
        <p class="error" role="alert"><Icon name="alert" size={14} /> {view.progress.error}</p>
      {/if}
    </div>
  {/if}

  {#if actionError}
    <p class="error" role="alert"><Icon name="alert" size={14} /> {actionError}</p>
  {/if}

  {#if view}
  <div class="actions">
    {#if installed && !busy}
      {#if serve?.running}
        <Button onclick={stopServe} disabled={serving} title="Stops libris's own llama-server">
          <Icon name="stop" size={14} /> Stop the server
        </Button>
      {:else}
        <Button
          variant="primary"
          onclick={startServe}
          disabled={serving || !(view.models?.length)}
          title={view.models?.length
            ? `Run one llama.cpp on port ${view.port} with ${view.models?.length} model(s)`
            : 'There is no model to run yet'}
        >
          <Icon name="pulse" size={14} />
          {#if serving}Starting…{:else}Start the server{/if}
        </Button>
      {/if}
    {/if}

    {#if busy}
      <Button onclick={stop} title="Keeps the bytes downloaded so far; the next attempt resumes">
        <Icon name="stop" size={14} /> Stop
      </Button>
      <span class="muted hint">Downloaded {formatBytes(view?.progress.done ?? 0)}</span>
    {:else}
      {#if !installed || (latest && installed && latest.tag !== installed.tag)}
        <Button
          variant="primary"
          onclick={download}
          disabled={!latest || started}
          title={latest
            ? `Fetch ${latest.tag} (${formatBytes(asset?.size ?? 0)}) into ${view?.dir ?? ''}`
            : checking
              ? 'Waiting for the answer about which build is newest'
              : 'The newest build could not be determined'}
        >
          <Icon name="upload" size={14} />
          {#if installed && latest}
            Update to {latest.tag}
          {:else if latest}
            Download {latest.tag}
          {:else if checking}
            Checking…
          {:else}
            Download
          {/if}
        </Button>
      {:else if installed}
        <span class="muted hint">
          Up to date, installed {formatDate(installed.installed_at)}
        </span>
        <Button onclick={() => void refresh(true)} title="Ask GitHub whether a newer build exists">
          <Icon name="retry" size={14} /> Check for updates
        </Button>
      {/if}

      {#if view?.parts_bytes}
        <Button onclick={discard} title="Throw away the bytes a stopped download is holding">
          <Icon name="trash" size={14} /> Discard {formatBytes(view.parts_bytes)}
        </Button>
      {/if}

      {#if installed}
        <Button variant="danger" onclick={() => (confirming = true)}>
          <Icon name="trash" size={14} /> Remove
        </Button>
      {/if}
    {/if}
  </div>
  {/if}

  <p class="muted footnote">
    The engine is a separate program libris downloads and runs; sampling and context size are
    fixed by it, not by these settings.
  </p>
</div>

<ConfirmDialog
  bind:open={confirming}
  cls="engine-remove"
  label="Remove engine"
  title="Remove this engine build?"
  lead="The downloaded llama.cpp files are deleted from this machine."
  note="Nothing else is touched: your notebooks, sources and answers stay. Re-downloading takes a minute."
  confirmLabel="Remove"
  busy={removing}
  onconfirm={remove}
/>

<style>
  /* Inside a settings card already, so this section is separated by rules rather than by a
     second frame: a box inside a box is the thing the rest of the app stopped doing. */
  .engine {
    border-top: 1px solid var(--border-muted);
    margin-top: 4px;
    padding-top: 16px;
  }

  .facts {
    display: grid;
    gap: 10px;
    margin: 0;
  }
  .facts > div {
    display: grid;
    grid-template-columns: 140px 1fr;
    align-items: baseline;
    gap: 12px;
  }
  .facts dt {
    color: var(--fg-muted);
    font-size: 12px;
    font-weight: 500;
  }
  .facts dd {
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 8px;
    font-size: 13px;
  }

  .tag {
    font-family: var(--font-mono);
    font-size: 12px;
    padding: 1px 6px;
    border-radius: var(--radius, 6px);
    background: var(--canvas-subtle);
    border: 1px solid var(--border-muted);
  }
  .models {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 6px 0 0;
  }
  /* A badge for a thing that failed, in the one red the app already has. */
  .badge.warn {
    color: var(--danger);
    background: color-mix(in srgb, var(--danger) 12%, transparent);
    border-color: color-mix(in srgb, var(--danger) 40%, transparent);
  }
  .badge {
    font-size: 11px;
    font-weight: 600;
    padding: 1px 8px;
    border-radius: 999px;
    color: var(--success);
    background: color-mix(in srgb, var(--success) 14%, transparent);
  }
  .device {
    display: inline-flex;
    align-items: baseline;
    gap: 6px;
    font-size: 13px;
  }
  .path {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg-muted);
    overflow-wrap: anywhere;
  }
  .asof {
    font-size: 12px;
  }

  .warnings,
  .note,
  .error {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 12px 0 0;
    font-size: 12px;
  }
  .warnings {
    list-style: none;
    padding: 0;
    flex-direction: column;
    align-items: flex-start;
    color: var(--attention);
  }
  .note {
    color: var(--fg-muted);
  }
  .error {
    color: var(--danger);
  }

  .progress {
    margin-top: 14px;
  }
  .line {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 12px;
    font-size: 12px;
    margin-bottom: 6px;
  }
  .pct {
    font-family: var(--font-mono);
    margin-left: 6px;
  }
  /* A bar, not a spinner: the number that matters is how much of a 35 MB file is on this
     disk, and a spinner cannot show a download that resumed at 90%. */
  .bar {
    height: 6px;
    border-radius: 999px;
    background: var(--canvas-subtle);
    border: 1px solid var(--border-muted);
    overflow: hidden;
  }
  .bar > span {
    display: block;
    height: 100%;
    background: var(--success-emphasis);
    transition: width 0.4s ease;
  }
  .progress.failed .bar > span {
    background: var(--danger);
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: 16px;
  }
  .hint {
    font-size: 12px;
    margin-right: 4px;
  }

  .link {
    background: none;
    border: 0;
    padding: 0;
    color: var(--accent);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  .link:hover {
    text-decoration: underline;
  }

  .footnote {
    font-size: 12px;
    margin-top: 12px;
  }

  .muted {
    color: var(--fg-muted);
  }

  @media (max-width: 700px) {
    .facts > div {
      grid-template-columns: 1fr;
      gap: 2px;
    }
  }
</style>
