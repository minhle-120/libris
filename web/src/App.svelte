<script lang="ts">
  import { onMount } from 'svelte';
  import { createNotebook, listNotebooks } from './lib/api';
  import { readOpen, SIDE, writeOpen } from './lib/panel';
  import { errorMessage } from './lib/format';
  import type { Crumb, Notebook, Panel, Source } from './lib/types';
  import Header from './components/Header.svelte';
  import Sidebar from './components/Sidebar.svelte';
  import NotebookView from './components/NotebookView.svelte';
  import SettingsView from './components/SettingsView.svelte';
  import EmptyState from './lib/EmptyState.svelte';
  import Icon from './lib/Icon.svelte';
  import Button from './lib/Button.svelte';

  let theme = $state<'light' | 'dark'>(
    document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'
  );
  let view = $state<'notebook' | 'settings'>('notebook');
  let notebooks = $state<Notebook[]>([]);
  let activeId = $state<string | null>(null);
  let loading = $state(true);
  let error = $state('');
  let dlg = $state<HTMLDialogElement | null>(null);
  let nameInput = $state<HTMLInputElement | null>(null);
  let newName = $state('');
  let panel = $state<Panel>('sources');
  let panelOpen = $state(readOpen(SIDE));
  let inspecting = $state<Source | null>(null);
  // The breadcrumb's first level means "no notebook picked", and load() must not
  // undo that by re-selecting the newest one the next time a source changes.
  let atHome = $state(false);

  const active = $derived(notebooks.find((nb) => nb.id === activeId) ?? null);

  const panelLabel: Record<Panel, string> = { sources: 'Sources', activity: 'Activity' };

  // The chat is not in here. It is the page, and a breadcrumb names where you are
  // by climbing out of, not the ground you are standing on.
  const crumbs = $derived.by((): Crumb[] => {
    const out: Crumb[] = [{ label: 'libris', onclick: goHome }];
    if (view === 'settings') return [...out, { label: 'settings' }];
    if (!active) return out;
    out.push({ label: active.name });
    if (!panelOpen) return out;
    // The page you are on is not a link, and Sources is one level above an open
    // chunk viewer — which is the only time it becomes a way back.
    out.push(inspecting ? { label: 'Sources', onclick: () => (inspecting = null) } : { label: panelLabel[panel] });
    if (inspecting) out.push({ label: inspecting.name });
    return out;
  });

  onMount(load);

  async function load() {
    try {
      notebooks = await listNotebooks();
      // Keep the selection if it still exists, otherwise open the newest — unless
      // the user asked for the notebook list itself by clicking up to libris.
      if (!atHome && !notebooks.some((nb) => nb.id === activeId)) {
        activeId = notebooks.at(-1)?.id ?? null;
      }
      error = '';
    } catch (e) {
      error = errorMessage(e);
    } finally {
      loading = false;
    }
  }

  function openNew() {
    error = '';
    newName = '';
    dlg?.showModal();
  }

  async function create() {
    const name = newName.trim();
    // The server refuses an unnamed notebook; refusing here keeps that from being
    // the first thing a user learns about the feature.
    if (!name) return;
    error = '';
    try {
      const notebook = await createNotebook(name);
      notebooks = [...notebooks, notebook];
      activeId = notebook.id;
      view = 'notebook';
      dlg?.close();
    } catch (e) {
      error = errorMessage(e);
    }
  }

  /**
   * A drop the page does not handle is a file the browser opens for you, and that
   * default fires even when an upload succeeded, because the `drop` event reaches
   * `document` uncancelled. So the shell swallows file drops wherever they land —
   * the sidebar, the header, the chat tab — as long as they carry files: a text
   * drop into an input must keep doing what the browser does with it.
   */
  const fileDrop = (e: DragEvent) => {
    if (Array.from(e.dataTransfer?.types ?? []).includes('Files')) e.preventDefault();
  };

  function renamed(id: string, name: string) {
    notebooks = notebooks.map((nb) => (nb.id === id ? { ...nb, name } : nb));
  }

  function openNotebook(id: string) {
    if (id !== activeId) inspecting = null;
    activeId = id;
    atHome = false;
    view = 'notebook';
  }

  function goHome() {
    view = 'notebook';
    activeId = null;
    atHome = true;
    inspecting = null;
  }

  function setPanel(next: Panel) {
    view = 'notebook';
    panel = next;
    panelOpen = true;
    writeOpen(SIDE, true);
  }

  function setPanelOpen(next: boolean) {
    panelOpen = next;
    writeOpen(SIDE, next);
    if (!next) inspecting = null;
  }

  /** A citation is an instruction to look: the panel opens even if it was shut. */
  function inspect(source: Source | null) {
    inspecting = source;
    if (source) {
      panel = 'sources';
      panelOpen = true;
      writeOpen(SIDE, true);
    }
  }

  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('libris.theme', theme);
  }
</script>

<Header
  {theme}
  {view}
  {crumbs}
  ontoggle={toggleTheme}
  onsettings={() => (view = 'settings')}
  onhome={goHome}
/>

<div class="shell">
  <Sidebar
    {notebooks}
    {activeId}
    onselect={openNotebook}
    onnew={openNew}
    onrenamed={renamed}
    onerror={(m) => (error = m)}
  />
  <main>
    {#if error}
      <div class="banner" role="alert">
        <Icon name="alert" size={14} />
        {error}
      </div>
    {/if}

    {#if view === 'settings'}
      <SettingsView />
    {:else if active}
      {#key active.id}
        <NotebookView
          notebook={active}
          {panel}
          open={panelOpen}
          {inspecting}
          onpanel={setPanel}
          onopen={setPanelOpen}
          oninspect={inspect}
          onchange={load}
        />
      {/key}
    {:else if loading}
      <p class="center muted">Loading notebooks…</p>
    {:else}
      <div class="landing">
        <EmptyState
          icon="book"
          title="No notebook selected"
          description="A notebook holds a set of sources. Create one to start adding PDFs, images, or audio. Nothing leaves this machine."
        >
          <Button variant="primary" onclick={openNew}>New notebook</Button>
        </EmptyState>
      </div>
    {/if}
  </main>
</div>

<svelte:window on:dragover={fileDrop} on:drop={fileDrop} />

<dialog
  class="new-notebook"
  aria-label="New notebook"
  bind:this={dlg}
  onclick={(e) => e.target === dlg && dlg?.close()}
  onclose={() => nameInput?.blur()}
>
  <h2>New notebook</h2>
  <label class="field">
    <span>Name</span>
    <input
      type="text"
      maxlength="200"
      placeholder="Paper notes"
      bind:this={nameInput}
      bind:value={newName}
      onkeydown={(e) => e.key === 'Enter' && create()}
    />
  </label>
  <div class="actions">
    <Button variant="ghost" onclick={() => dlg?.close()}>Cancel</Button>
    <Button variant="primary" disabled={!newName.trim()} onclick={create}>Create</Button>
  </div>
</dialog>

<style>
  .shell {
    display: flex;
    flex: 1;
    min-height: 0;
    align-items: stretch;
  }
  main {
    flex: 1;
    min-width: 0;
    overflow-y: auto;
    background: var(--canvas);
  }
  .landing {
    max-width: 640px;
    padding: 48px 24px;
    margin: 0 auto;
  }
  .center {
    padding: 48px 24px;
    text-align: center;
  }
  /* Native <dialog> + showModal(): Esc, focus trapping and the top layer come from
     the browser, so the only thing written here is the layout of three controls. */
  .new-notebook {
    width: min(420px, calc(100vw - 32px));
    padding: 20px;
    color: var(--fg);
    background: var(--canvas);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg, 12px);
    box-shadow: 0 12px 32px rgb(0 0 0 / 18%);
  }
  .new-notebook::backdrop {
    background: rgb(0 0 0 / 45%);
  }
  .new-notebook h2 {
    margin: 0 0 14px;
    font-size: 16px;
    font-weight: 600;
  }
  .field {
    display: block;
  }
  .field span {
    display: block;
    margin-bottom: 4px;
    font-size: 12px;
    font-weight: 600;
    color: var(--fg-muted);
  }
  .field input {
    width: 100%;
    padding: 6px 8px;
    font: inherit;
    font-size: 14px;
    color: var(--fg);
    background: var(--canvas);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    outline: none;
  }
  .field input:focus {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 25%, transparent);
  }
  .actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    margin-top: 18px;
  }
  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    max-width: 1080px;
    margin: 16px 24px 0;
    padding: 10px 12px;
    font-size: 13px;
    color: var(--danger);
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    background: var(--canvas);
  }
</style>
