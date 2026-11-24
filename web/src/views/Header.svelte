<script lang="ts">
  import { app, goHome, leaveSettings, openSettings, toggleTheme } from '../lib/store.svelte';
  import Button from '../lib/ui/Button.svelte';
  import Icon from '../lib/ui/Icon.svelte';

  /**
   * One row: the brand tile, which is the home button and carries no wordmark — `libris` is
   * already the first crumb — then the breadcrumb, then the two buttons at the right edge.
   *
   * There is no search box. There is no endpoint it could ask, and a control that promises a
   * feature the app does not have is worse than no control at all.
   */
  const inSettings = $derived(app.view === 'settings');
  const notebook = $derived(app.notebooks.find((n) => n.id === app.crumbNotebook));
  const chat = $derived(app.chats.find((c) => c.id === app.chatId));
</script>

<header class="header">
  <Button variant="ghost" klass="brand" onclick={goHome}>
    <span class="mark" title="Notebooks"><Icon name="book" /></span>
  </Button>

  <nav class="crumbs" aria-label="Breadcrumb">
    <button class="crumb link" onclick={goHome}>libris</button>
    {#if inSettings}
      <span class="sep" aria-hidden="true">/</span>
      <span class="crumb">settings</span>
    {:else if notebook}
      <span class="sep" aria-hidden="true">/</span>
      <span class="crumb truncate">{notebook.name}</span>
      <!-- A conversation with no title yet is not in the breadcrumb at all: the server names it
           from the first question, so until then there is nothing to show. -->
      {#if chat?.title}
        <span class="sep" aria-hidden="true">/</span>
        <span class="crumb truncate">{chat.title}</span>
      {/if}
    {/if}
  </nav>

  <div class="right">
    <Button
      variant="ghost"
      shape="icon"
      title="Settings"
      ariaLabel="Settings"
      klass={inSettings ? 'active' : ''}
      onclick={inSettings ? leaveSettings : openSettings}
    >
      <Icon name="settings" />
    </Button>
    <Button
      variant="ghost"
      shape="icon"
      title={app.theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'}
      ariaLabel="Switch theme"
      onclick={toggleTheme}
    >
      <Icon name={app.theme === 'dark' ? 'sun' : 'moon'} />
    </Button>
  </div>
</header>

<style>
  .header {
    display: flex;
    align-items: center;
    gap: 4px;
    height: var(--header-h);
    flex: none;
    padding: 0 8px 0 4px;
    background: var(--chrome);
    border-bottom: 1px solid var(--border);
  }

  /* The Button's own element is outside this component's scope, so the layout hooks it is
     given are addressed from here. */
  .header :global(.btn.brand) {
    width: 30px;
    height: 30px;
    padding: 0;
    flex: none;
  }

  /* A filled square with the book glyph: the only solid thing in the header. */
  .mark {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 5px;
    background: var(--text);
    color: var(--bg);
  }

  .crumbs {
    display: flex;
    align-items: center;
    gap: 2px;
    min-width: 0;
    flex: 1;
    font-size: var(--fs);
  }

  .crumb {
    max-width: 26ch;
    padding: 2px 4px;
    border: 0;
    border-radius: 4px;
    background: none;
    color: var(--text);
    font: inherit;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .crumb.link {
    color: var(--muted);
  }

  .crumb.link:hover {
    background: var(--hover);
    color: var(--text);
  }

  /* Air with a slash in it, kept behind the labels. */
  .sep {
    color: var(--border);
    padding: 0 1px;
  }

  .right {
    display: flex;
    align-items: center;
    gap: 2px;
    flex: none;
    margin-left: auto;
  }

  .right :global(.btn.active) {
    background: var(--hover);
    color: var(--text);
  }

  @media (max-width: 720px) {
    .crumbs {
      display: none;
    }
  }
</style>
