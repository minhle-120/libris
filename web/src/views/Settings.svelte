<script lang="ts">
  import { onMount } from 'svelte';
  import { settingGroups, type SettingField, type Settings } from '../lib/config.gen';
  import { app, loadSettings, saveSettings, settingsDirty } from '../lib/store.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';
  import Machine from './settings/Machine.svelte';
  import Models from './settings/Models.svelte';

  /**
   * Every label, kind, range and unit on this page comes from the schema the backend
   * generates. A control the schema did not ask for is a defect, which is why nothing here
   * spells out a single setting's name.
   *
   * What is *not* a setting lives beside the facts it needs: what is installed on this
   * machine and what weights are on its disk are read from the host endpoints, and their
   * controls are verbs.
   */
  const mode = $derived(app.settings.engineMode);

  /** `hidden` is `noview:"true"` in internal/config: defined, saved, validated and generated
   *  like every other setting, with its control living where the facts it needs are. */
  const shown = (f: SettingField) => (!f.mode || f.mode === mode) && !f.hidden;

  // A group whose every field is hidden is a heading with nothing under it. One group is kept
  // anyway: it hosts the machine section.
  const sections = $derived(
    settingGroups
      .map((g) => ({ ...g, fields: g.fields.filter(shown) }))
      .filter((g) => g.fields.length > 0 || g.id === 'servers')
  );

  let current = $state(settingGroups[0]?.id ?? '');
  let notice = $state('');
  /** Bumped on every save. Port and mode are settings, so a saved change alters what the
   *  machine section is allowed to offer — and it must not go on showing the old port. */
  let savedToken = $state(0);
  /** Whether a model worth serving is on disk. The engine card's Start button is gated on it,
   *  and the two cards are siblings that read different endpoints. */
  let modelSet = $state(0);

  onMount(() => {
    void loadSettings();
  });

  async function save() {
    notice = '';
    if (await saveSettings()) {
      savedToken++;
      notice = 'Saved';
    }
  }

  function go(id: string) {
    current = id;
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  // The record is a typed union, so reads and writes go through these rather than a cast in
  // the template.
  const bag = () => app.settings as unknown as Record<string, unknown>;
  const str = (k: keyof Settings) => String(bag()[k as string] ?? '');
  const num = (k: keyof Settings) => Number(bag()[k as string] ?? 0);
  const bool = (k: keyof Settings) => Boolean(bag()[k as string]);
  function set(k: keyof Settings, value: unknown) {
    bag()[k as string] = value;
    notice = '';
  }

  const status = $derived(
    app.settingsLoading
      ? 'Loading…'
      : app.settingsError
        ? app.settingsError
        : settingsDirty()
          ? 'Unsaved changes'
          : notice
            ? notice
            : 'Saved settings loaded from the backend'
  );
</script>

<div class="page">
  <div class="head">
    <h1>Settings</h1>
    <p>Everything here stays on this machine.</p>
  </div>

  <div class="grid">
    <nav class="subnav" aria-label="Settings sections">
      {#each sections as group (group.id)}
        <button
          class="nav-item"
          class:active={current === group.id}
          onclick={() => go(group.id)}
        >
          {group.label}
        </button>
      {/each}
    </nav>

    <div class="panels">
      <!-- One bar, sticky: a change made in the first section should not have to hunt for its
           button. The edge carries the news, because a tint behind two disabled buttons would
           compete with the primary itself. -->
      <div class="actions" class:dirty={settingsDirty()} class:bad={!!app.settingsError}>
        <span class="status" role={app.settingsError ? 'alert' : undefined}>
          <Icon name={app.settingsError ? 'alert' : settingsDirty() ? 'pencil' : 'check'} size={14} />
          {status}
        </span>
        <div class="buttons">
          <Button size="sm" disabled={!settingsDirty() || app.settingsSaving} onclick={loadSettings}>
            Discard
          </Button>
          <Button
            variant="primary"
            size="sm"
            disabled={!settingsDirty() || app.settingsSaving}
            onclick={save}
          >
            {app.settingsSaving ? 'Saving…' : 'Save changes'}
          </Button>
        </div>
      </div>

      {#each sections as group (group.id)}
        <section class="card" id={group.id}>
          <header>
            <h2>{group.label}</h2>
            <p>{group.help}</p>
          </header>

          {#each group.fields as field (field.key)}
            <div class="row">
              <div class="label">
                <span>{field.label}</span>
                <p>{field.help}</p>
              </div>

              {#if field.kind === 'bool'}
                <button
                  class="switch"
                  role="switch"
                  aria-checked={bool(field.key)}
                  aria-label={field.label}
                  disabled={field.readonly}
                  onclick={() => set(field.key, !bool(field.key))}
                >
                  <span></span>
                </button>
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
                <div class="control">
                  <input
                    class="input"
                    class:num={field.kind === 'number'}
                    type={field.kind === 'number'
                      ? 'number'
                      : field.kind === 'password'
                        ? 'password'
                        : 'text'}
                    min={field.min}
                    max={field.max}
                    placeholder={field.readonly && str(field.key) === ''
                      ? 'probed at startup'
                      : undefined}
                    value={field.kind === 'number' ? num(field.key) : str(field.key)}
                    disabled={field.readonly}
                    oninput={(e) =>
                      set(
                        field.key,
                        field.kind === 'number' ? Number(e.currentTarget.value) : e.currentTarget.value
                      )}
                  />
                  {#if field.unit}<span class="unit">{field.unit}</span>{/if}
                </div>
              {/if}
            </div>
          {/each}

          {#if group.id === 'servers' && mode === 'managed'}
            <Machine saved={savedToken} modelsReady={modelSet > 0} />
          {/if}

          {#if group.id === 'models' && mode === 'managed'}
            <Models saved={savedToken} onSet={(n) => (modelSet = n)} />
          {/if}
        </section>
      {/each}
    </div>
  </div>
</div>

<style>
  .page {
    max-width: 980px;
    padding: 20px 24px 40px;
  }

  .head {
    padding-bottom: 14px;
  }

  .head p {
    font-size: var(--fs-sm);
    color: var(--muted);
    padding-top: 2px;
  }


  .grid {
    display: grid;
    grid-template-columns: 150px minmax(0, 1fr);
    gap: 22px;
    align-items: start;
  }

  .subnav {
    position: sticky;
    top: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }

  .nav-item {
    padding: 6px 10px;
    border: 0;
    border-radius: var(--r);
    background: none;
    text-align: left;
    color: var(--text);
    font: inherit;
    font-size: var(--fs-sm);
  }

  .nav-item:hover {
    background: var(--hover);
  }

  /* A short accent bar, not a filled row: the nav is a place in the margin, not a selection in
     a list. */
  .nav-item.active {
    font-weight: 600;
    box-shadow: inset 2px 0 0 var(--accent);
    border-radius: 0 var(--r) var(--r) 0;
  }

  .panels {
    display: flex;
    flex-direction: column;
    gap: 18px;
    min-width: 0;
  }

  .actions {
    position: sticky;
    /* <main> is the scroller and it starts below the header, so 0 is flush under the header.
       Reaching for the header's height here leaves a band of fields sliding through the gap. */
    top: 0;
    z-index: 5;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px 14px;
    flex-wrap: wrap;
    padding: 9px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r);
    background: var(--bg);
  }

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
    font-size: var(--fs-xs);
    color: var(--muted);
  }

  .buttons {
    display: flex;
    gap: 8px;
  }

  section.card {
    overflow: hidden;
    scroll-margin-top: 54px;
  }

  section > header {
    padding: 12px 16px;
    background: var(--chrome);
    border-bottom: 1px solid var(--border);
  }

  section > header h2 {
    font-size: var(--fs);
    font-weight: 600;
  }

  section > header p {
    font-size: var(--fs-xs);
    color: var(--muted);
    padding-top: 2px;
    line-height: 1.5;
  }

  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 4px 16px;
    align-items: center;
    padding: 11px 16px;
    border-bottom: 1px solid var(--border-soft);
  }

  .row:last-of-type {
    border-bottom: 0;
  }

  .label {
    min-width: 0;
  }

  .label > span {
    font-size: var(--fs-sm);
    font-weight: 500;
  }

  .label p {
    font-size: var(--fs-xs);
    color: var(--muted);
    line-height: 1.5;
    padding-top: 1px;
  }

  .control {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .control input.num {
    width: 88px;
    text-align: right;
  }

  .control input:not(.num) {
    width: 260px;
  }

  .select {
    width: 260px;
    padding-right: 8px;
    appearance: auto;
  }

  .unit {
    font-size: var(--fs-xs);
    color: var(--muted);
    white-space: nowrap;
  }

  .switch {
    display: inline-flex;
    width: 34px;
    height: 20px;
    padding: 2px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--chrome);
    justify-content: flex-start;
  }

  .switch > span {
    display: block;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--muted);
  }

  .switch[aria-checked='true'] {
    background: var(--accent);
    border-color: var(--accent);
    justify-content: flex-end;
  }

  .switch[aria-checked='true'] > span {
    background: #fff;
  }

  .switch:disabled {
    opacity: 0.55;
  }

  @media (max-width: 1000px) {
    .control input:not(.num),
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
      border-radius: var(--r);
      background: var(--hover);
    }

    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
