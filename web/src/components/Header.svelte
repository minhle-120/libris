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
    <!-- The word "libris" is the first crumb already. Saying it twice — a brand label
         and then the location that starts with the same word — is why the header read
         as "libris libris / test / Sources". The tile is the brand; the crumbs spell it. -->
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
  .brand > .mark {
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
  .mark {
    transition: opacity 100ms ease;
  }
  .mark:hover {
    opacity: 0.8;
  }
  /* The separator is not a breadcrumb: it is air with a slash in it, so it stays behind
     the labels it separates instead of competing with them. */
  .sep {
    color: var(--fg-muted);
    opacity: 0.4;
    font-size: 13px;
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
  .crumb.link {
    transition: background 80ms ease, color 80ms ease;
  }
  .crumb.link:hover {
    color: var(--accent);
    background: var(--hover);
    text-decoration: none;
  }
  /* A filled field with no border until it is wanted — the one cue borrowed from how
     Google draws a search box, and the reason the header used to read as a row of
     outlined boxes. */
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    width: min(340px, 32vw);
    height: 32px;
    padding: 0 10px;
    margin-left: auto;
    color: var(--fg-muted);
    background: var(--canvas);
    border: 1px solid transparent;
    border-radius: 8px;
    transition: border-color 100ms ease, box-shadow 100ms ease;
  }
  .search:hover {
    border-color: var(--border);
  }
  .search:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
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
    padding: 0 5px;
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 17px;
    color: var(--fg-muted);
    background: var(--hover);
    border: none;
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
