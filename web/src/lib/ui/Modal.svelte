<script lang="ts">
  import Button from './Button.svelte';

  /**
   * The one dialog both confirmations in the app are built from, and the settings bar is not it.
   *
   * Anatomy: a title, the plain sentence saying **what is about to be lost**, an optional boxed
   * note saying **what is *not* going** — set apart rather than merely spoken, because it is the
   * sentence people reread — then the actions, right-aligned.
   *
   * Cancel is first in the DOM on purpose: `showModal()` focuses the first control, so Enter on a
   * freshly opened confirmation cannot do the destructive thing. It is a native `<dialog>`, so the
   * browser owns Esc, the focus trap and the top layer; closing always travels upward, because a
   * parent that still believes a dialog is open can never open it again.
   */
  let {
    open = $bindable(false),
    klass = '',
    title,
    keeps = '',
    confirm = '',
    variant = 'primary',
    confirmDisabled = false,
    busy = false,
    error = '',
    onconfirm,
    onclosed,
    children
  }: {
    open?: boolean;
    /** The name a check selects on, e.g. `delete-notebook`. */
    klass?: string;
    title: string;
    keeps?: string;
    confirm?: string;
    variant?: 'primary' | 'danger';
    confirmDisabled?: boolean;
    busy?: boolean;
    error?: string;
    onconfirm?: () => void;
    /** Called whenever the dialog ends, by whatever route: Esc, the backdrop, or Cancel. */
    onclosed?: () => void;
    children?: import('svelte').Snippet;
  } = $props();

  /** The dialog is labelled by its own heading, which is also the thing the reader reads
   *  first. `klass` names the dialog, so the id is one per confirmation and stable across a
   *  re-render — a counter would move it every time the box reopened. */
  const heading = $derived(`${klass || 'modal'}-title`);

  let dlg = $state<HTMLDialogElement | undefined>();

  $effect(() => {
    const el = dlg;
    const shown = open;
    if (!el) return;
    if (shown && !el.open) el.showModal();
    else if (!shown && el.open) el.close();
  });

  // Closing only asks for the close; the native `close` event is the one place that reports it,
  // so a dialog cannot report its own ending twice.
  const close = () => (open = false);
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<dialog
  class="modal {klass}"
  aria-labelledby={heading}
  bind:this={dlg}
  onclick={(e) => e.target === dlg && close()}
  onclose={() => {
    open = false;
    onclosed?.();
  }}
>
  <div class="box">
    <h2 id={heading}>{title}</h2>
    {@render children?.()}
    {#if keeps}<p class="keeps">{keeps}</p>{/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <div class="actions">
      <Button onclick={close}>Cancel</Button>
      {#if confirm}
        <Button variant={variant} disabled={confirmDisabled || busy} onclick={onconfirm}>
          {confirm}
        </Button>
      {/if}
    </div>
  </div>
</dialog>
