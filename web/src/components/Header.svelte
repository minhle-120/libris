<script lang="ts">
  import Icon from '../lib/Icon.svelte';
  import Button from '../lib/Button.svelte';

  let {
    theme,
    view = 'notebook',
    ontoggle,
    onnew,
    onsettings,
    onhome
  }: {
    theme: 'light' | 'dark';
    view?: 'notebook' | 'settings';
    ontoggle: () => void;
    onnew: () => void;
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
    <span class="sep">/</span>
    <span class="crumb">{view === 'settings' ? 'settings' : 'notebooks'}</span>
  </div>

  <label class="search">
    <Icon name="search" size={14} />
    <input placeholder="Search notebooks and sources" />
    <kbd>/</kbd>
  </label>

  <div class="right">
    <Button variant="primary" size="sm" onclick={onnew}>
      <Icon name="plus" size={14} />
      New
    </Button>
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
  .crumb {
    font-size: 14px;
    color: var(--fg-muted);
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
