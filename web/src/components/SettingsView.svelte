<script lang="ts">
  import { onMount } from 'svelte';
  import { defaultSettings, settingGroups, type Settings } from '../lib/config.gen';
  import { getSettings, putSettings } from '../lib/api';
  import SettingRow from '../lib/SettingRow.svelte';
  import Toggle from '../lib/Toggle.svelte';
  import Button from '../lib/Button.svelte';
  import Icon from '../lib/Icon.svelte';

  // Values come from the backend; labels, help, ranges and enums come from the
  // generated schema. Nothing in this file defines a setting.
  let settings = $state<Settings>(defaultSettings());
  let saved = $state('');
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let notice = $state('');

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
      {#each settingGroups as group (group.id)}
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
      {#each settingGroups as group (group.id)}
        <section class="card" id={group.id}>
          <header>
            <h2>{group.label}</h2>
            <p class="muted">{group.help}</p>
          </header>

          {#each group.fields as field (field.key)}
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
        </section>
      {/each}

      {#if error}
        <div class="banner error" role="alert">
          <Icon name="pulse" size={14} />
          {error}
        </div>
      {/if}

      <div class="foot">
        <span class="muted small">
          <Icon name="inbox" size={14} />
          {#if loading}
            Loading…
          {:else if notice}
            {notice}
          {:else if dirty}
            Unsaved changes
          {:else}
            Saved settings loaded from the backend
          {/if}
        </span>
        <div class="foot-actions">
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
    scroll-margin-top: 12px;
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
  .banner {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    font-size: 13px;
    border: 1px solid var(--danger);
    border-radius: var(--radius);
    color: var(--danger);
    background: var(--canvas);
  }
  .foot-actions {
    display: flex;
    gap: 8px;
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 4px 0 24px;
  }
  .small {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
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
