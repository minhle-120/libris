<script lang="ts">
  import { onMount } from 'svelte';
  import { defaultSettings, settingGroups, type Settings } from '../lib/config.gen';
  import { getSettings, putSettings } from '../lib/api';
  import SettingRow from '../lib/SettingRow.svelte';
  import Toggle from '../lib/Toggle.svelte';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';
  import EnginePanel from '../lib/EnginePanel.svelte';
  import ModelPanel from '../lib/ModelPanel.svelte';

  // Values come from the backend; labels, help, ranges and enums come from the
  // generated schema. Nothing in this file defines a setting.
  let settings = $state<Settings>(defaultSettings());
  let saved = $state('');
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let notice = $state('');
  // Bumped on every save, and handed to the machine section as a reason to refetch. A
  // settings change that alters what the backend offers must not leave the section showing
  // the other build's file size.
  let savedToken = $state(0);
  // Bumped when the model files on this disk change. The engine card and the models card are
  // siblings, and neither reads the other's endpoint; this is the one line between them, and
  // it exists because the Start button is gated on there being a model to run.
  let modelSet = $state(0);

  // Which half of the model-server group applies. It is one switch rather than two toggles
  // that can both be on, and the fields it hides are the ones libris derives for itself
  // — a form that asks for an address it is about to compute is how this page
  // became confusing.
  const mode = $derived(settings.engineMode);

  function shown(field: { mode?: 'managed' | 'external'; hidden?: boolean }): boolean {
    // hidden is `noview:"true"` in internal/config: the setting is defined, saved, validated
    // and generated exactly like the others, and its control lives beside the facts it needs
    // — for the device, the list the installed engine printed — rather than as a text field
    // asking someone to type a name they can only learn from a command line.
    return (!field.mode || field.mode === mode) && !field.hidden;
  }

  // A group whose every field is hidden is a heading with nothing under it.
  const sections = $derived(
    settingGroups
      .map((group) => ({ ...group, fields: group.fields.filter(shown) }))
      .filter((group) => group.fields.length > 0 || group.id === 'servers')
  );

  const snapshot = $derived(JSON.stringify(settings));
  const dirty = $derived(!loading && snapshot !== saved);

  let current = $state(settingGroups[0]?.id ?? '');

  onMount(load);

  async function load() {
    loading = true;
    error = '';
    try {
      apply(await getSettings());
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  function apply(next: Settings) {
    settings = next;
    saved = JSON.stringify(next);
  }

  async function save() {
    saving = true;
    error = '';
    notice = '';
    try {
      apply(await putSettings(settings));
      notice = 'Saved';
      savedToken++;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  // Settings is a union-typed record, so reads go through these accessors and
  // writes through set(). Keeps the template type-safe without casts inline.
  const bag = () => settings as unknown as Record<string, unknown>;
  const str = (key: string) => String(bag()[key] ?? '');
  const num = (key: string) => Number(bag()[key] ?? 0);
  const bool = (key: string) => Boolean(bag()[key]);
  function set(key: string, value: unknown) {
    bag()[key] = value;
  }

  function go(id: string) {
    current = id;
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
</script>

<div class="page">
  <div class="head">
    <h1>Settings</h1>
    <p class="muted">
      Defined in <code>internal/config</code>, served by <code>GET /api/settings</code>, saved with <code>PUT</code>.
    </p>
  </div>

  <div class="grid">
    <nav class="subnav" aria-label="Settings sections">
      {#each sections as group (group.id)}
        <button
          type="button"
          class="nav-item"
          class:active={current === group.id}
          onclick={() => go(group.id)}
        >
          {group.label}
        </button>
      {/each}
    </nav>

    <div class="panels">
      <!-- One bar, and sticky: the page is several screens of fields, so a change made in
           the first panel should not have to hunt for its Save button at the bottom. The
           app header is sticky too, so this parks just below it rather than under it. -->
      <div class="actions" class:dirty class:bad={!!error}>
        <span class="status" role={error ? 'alert' : undefined}>
          <Icon name={error ? 'alert' : dirty ? 'pencil' : 'check'} size={14} />
          {#if loading}
            Loading…
          {:else if error}
            {error}
          {:else if dirty}
            Unsaved changes
          {:else if notice}
            {notice}
          {:else}
            Saved settings loaded from the backend
          {/if}
        </span>
        <div class="buttons">
          <Button size="sm" disabled={!dirty || saving || loading} onclick={load}>Discard</Button>
          <Button
            variant="primary"
            size="sm"
            disabled={!dirty || saving}
            onclick={save}
          >
            {saving ? 'Saving…' : 'Save changes'}
          </Button>
        </div>
      </div>

      {#each sections as group (group.id)}
        <section class="card" id={group.id}>
          <header>
            <h2>{group.label}</h2>
            <p class="muted">{group.help}</p>
          </header>

          {#each group.fields.filter(shown) as field (field.key)}
            <SettingRow label={field.label} description={field.help}>
              {#if field.kind === 'bool'}
                <Toggle
                  label={field.label}
                  checked={bool(field.key)}
                  disabled={field.readonly}
                  onchange={(value) => set(field.key, value)}
                />
              {:else if field.kind === 'enum'}
                <select
                  class="input select"
                  value={str(field.key)}
                  disabled={field.readonly}
                  onchange={(e) => set(field.key, e.currentTarget.value)}
                >
                  {#each field.options ?? [] as option (option.value)}
                    <option value={option.value}>{option.label}</option>
                  {/each}
                </select>
              {:else}
                <input
                  class="input"
                  class:num={field.kind === 'number'}
                  class:wide={field.kind !== 'number'}
                  type={field.kind === 'number'
                    ? 'number'
                    : field.kind === 'password'
                      ? 'password'
                      : 'text'}
                  min={field.min}
                  max={field.max}
                  placeholder={field.readonly && str(field.key) === '' ? 'probed at startup' : undefined}
                  value={field.kind === 'number' ? num(field.key) : str(field.key)}
                  disabled={field.readonly}
                  oninput={(e) =>
                    set(
                      field.key,
                      field.kind === 'number' ? Number(e.currentTarget.value) : e.currentTarget.value
                    )}
                />
                {#if field.unit}
                  <span class="unit">{field.unit}</span>
                {/if}
              {/if}
            </SettingRow>
          {/each}

          <!-- The machine is not a setting. What is installed, what is new, and what the box
               has in it are read from /api/host/*, and its buttons are verbs — so this lives
               under the group that names it rather than among the generated fields above. -->
          {#if group.id === 'servers' && mode === 'managed'}
            <EnginePanel
              saved={savedToken}
              modelSet={modelSet}
              device={str('engineDevice')}
              onDevice={(v) => set('engineDevice', v)}
            />
          {/if}

          <!-- The model files a managed engine loads are not settings — a setting is something
               libris can apply, and downloading a file is not one, so `internal/config` defines
               none of it — but they belong under the group that names them, beside the probed
               dimensions, which is the one model fact that is a setting. -->
          {#if group.id === 'models' && mode === 'managed'}
            <ModelPanel saved={savedToken} onModels={() => modelSet++} />
          {/if}
        </section>
      {/each}

    </div>
  </div>
</div>

<style>
  .page {
    max-width: 1000px;
    padding: 24px;
  }
  .head {
    margin-bottom: 20px;
  }
  .head p {
    margin-top: 4px;
    font-size: 13px;
  }
  code {
    font-family: var(--font-mono);
    font-size: 11px;
    padding: 1px 4px;
    background: var(--canvas-subtle);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .grid {
    display: grid;
    grid-template-columns: 160px minmax(0, 1fr);
    gap: 24px;
    align-items: start;
  }
  .subnav {
    position: sticky;
    top: 16px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .nav-item {
    padding: 6px 10px;
    font: inherit;
    font-size: 13px;
    color: var(--fg);
    text-align: left;
    background: none;
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .nav-item:hover {
    background: var(--canvas-subtle);
  }
  .nav-item.active {
    font-weight: 600;
    box-shadow: inset 2px 0 0 var(--accent);
    border-radius: 0 var(--radius) var(--radius) 0;
  }
  .panels {
    display: flex;
    flex-direction: column;
    gap: 20px;
    min-width: 0;
  }
  section.card {
    overflow: hidden;
    /* The save bar is sticky and roughly this tall; without the margin the section's own
       heading arrives underneath it, which is what the subnav exists to bring you to. */
    scroll-margin-top: 60px;
  }
  section header {
    padding: 14px 16px;
    background: var(--canvas-subtle);
    border-bottom: 1px solid var(--border);
  }
  section header h2 {
    font-size: 14px;
  }
  section header p {
    margin-top: 2px;
    font-size: 12px;
  }
  .wide {
    width: 280px;
  }
  .num {
    width: 88px;
    text-align: right;
  }
  .select {
    width: 280px;
    padding-right: 8px;
    appearance: auto;
  }
  .unit {
    font-size: 12px;
    color: var(--fg-muted);
    white-space: nowrap;
  }
  .actions {
    position: sticky;
    /* <main> is the scroller and starts below the header, so 0 is flush under it.
       var(--header-h) here would leave a band of fields sliding through the gap. */
    top: 0;
    z-index: 9;
    /* the field rows are cards, so an opaque background is not decoration */
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px 16px;
    flex-wrap: wrap;
    padding: 10px 14px;
    background: var(--canvas);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  /* Both buttons are disabled until something changes, so the edge is what says the
     page is holding work: a tint behind them would compete with the primary itself. */
  .actions.dirty {
    border-color: var(--accent);
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .actions.bad {
    border-color: var(--danger);
    box-shadow: inset 3px 0 0 var(--danger);
  }
  .actions.bad .status {
    color: var(--danger);
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .buttons {
    display: flex;
    gap: 8px;
  }
  @media (max-width: 1000px) {
    .wide,
    .select {
      width: 100%;
    }
  }
  @media (max-width: 860px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .subnav {
      position: static;
      flex-direction: row;
      flex-wrap: wrap;
    }
    .nav-item.active {
      box-shadow: none;
      border-radius: var(--radius);
    }
  }
</style>
