<script lang="ts">
  import type { Crumb } from '../lib/types';
  import Icon from '../lib/Icon.svelte';
  import Button from '../lib/Button.svelte';

  let {
    theme,
    view = 'notebook',
    crumbs = [],
    ontoggle,
    onsettings,
    onhome
  }: {
    theme: 'light' | 'dark';
    view?: 'notebook' | 'settings';
    crumbs?: Crumb[];
    ontoggle: () => void;
    onsettings: () => void;
    onhome: () => void;
  } = $props();
</script>

<header class="header">
  <div class="brand">
    <button type="button" class="mark" title="Notebooks" onclick={onhome}>
      <Icon name="book" size={15} />
    </button>
    <span class="name">libris</span>
    <nav class="crumbs" aria-label="Breadcrumb">
      {#each crumbs as crumb, i (i)}
        {#if i > 0}<span class="sep">/</span>{/if}
        {#if crumb.onclick}
          <button
            type="button"
            class="crumb link"
            title={crumb.label}
            onclick={crumb.onclick}
          >{crumb.label}</button>
        {:else}
          <span class="crumb" title={crumb.label}>{crumb.label}</span>
        {/if}
      {/each}
    </nav>
  </div>

  <label class="search">
    <Icon name="search" size={14} />
    <input placeholder="Search notebooks and sources" />
    <kbd>/</kbd>
  </label>

  <div class="right">
    <Button
      variant="ghost"
      iconOnly
      title="Settings"
      active={view === 'settings'}
      onclick={onsettings}
    >
      <Icon name="settings" />
    </Button>
    <Button
      variant="ghost"
      iconOnly
      title={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
      onclick={ontoggle}
    >
      <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
    </Button>
  </div>
</header>

<style>
  .header {
    position: sticky;
    top: 0;
    z-index: 10;
    display: flex;
    align-items: center;
    gap: 16px;
    height: var(--header-h);
    padding: 0 16px;
    background: var(--canvas-subtle);
    border-bottom: 1px solid var(--border);
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .brand > .mark,
  .brand > .name {
    flex-shrink: 0;
  }
  .mark {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    padding: 0;
    color: var(--canvas);
    background: var(--fg);
    border: none;
    border-radius: var(--radius);
    cursor: pointer;
  }
  .name {
    font-family: var(--font-mono);
    font-size: 15px;
    font-weight: 600;
    letter-spacing: -0.2px;
  }
  .sep {
    color: var(--border);
  }
  .crumbs {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .crumb {
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: 14px;
    color: var(--fg-muted);
    white-space: nowrap;
  }
  .crumb:last-child {
    max-width: 260px;
    color: var(--fg);
  }
  .crumb.link {
    padding: 2px 6px;
    margin: 0 -6px;
    font: inherit;
    font-size: 14px;
    border: none;
    border-radius: var(--radius);
    background: none;
    cursor: pointer;
  }
  .crumb.link:hover {
    color: var(--accent);
    background: var(--canvas);
    text-decoration: underline;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    width: min(320px, 32vw);
    height: 30px;
    padding: 0 8px;
    margin-left: auto;
    color: var(--fg-muted);
    background: var(--canvas);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .search input {
    flex: 1;
    min-width: 0;
    font: inherit;
    color: var(--fg);
    background: none;
    border: none;
    outline: none;
  }
  .search input::placeholder {
    color: var(--fg-muted);
  }
  kbd {
    padding: 0 4px;
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 16px;
    color: var(--fg-muted);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .right {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
  }
  @media (max-width: 720px) {
    .search,
    .crumb,
    .sep {
      display: none;
    }
  }
</style>
