<script lang="ts">
  import Button from './Button.svelte';

  /**
   * The one confirmation box. There were two of them written out longhand, a third was
   * on its way, and the thing worth keeping identical is not the copy — it is that the
   * safe control is the one the browser focuses.
   *
   * `showModal()` focuses the first control it finds, so Cancel is first in the markup
   * and Enter on a freshly opened confirmation cannot do the destructive thing. The
   * browser owns Esc, the focus trap and the top layer; this file owns the layout.
   */
  let {
    label,
    cls = '',
    title,
    lead,
    note = undefined,
    confirmLabel,
    busy = false,
    open = $bindable(false),
    onconfirm,
    onclose = undefined
  }: {
    /** Names the dialog to assistive tech, and to a test that has to find it. */
    label: string;
    /** Extra class so a caller can style or select this particular confirmation. */
    cls?: string;
    title: string;
    /** The sentence that says what is about to be lost. Plain text, never markup. */
    lead: string;
    /** The reassurance underneath — the thing that is *not* going. */
    note?: string | undefined;
    confirmLabel: string;
    /** True while the action is in flight, so the box cannot be pressed twice. */
    busy?: boolean;
    open?: boolean;
    onconfirm: () => void;
    onclose?: (() => void) | undefined;
  } = $props();

  let dlg = $state<HTMLDialogElement | null>(null);

  // The parent opens this by flipping `open`; the browser closes it with Esc, and a
  // backdrop click closes it here. Either way the close travels back up, because a
  // parent that still believes the box is open will never be able to open it again.
  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) dlg.showModal();
    else if (!open && dlg.open) dlg.close();
  });
</script>

<dialog
  class="modal {cls}"
  aria-label={label}
  bind:this={dlg}
  onclick={(e) => e.target === dlg && dlg?.close()}
  onclose={() => {
    open = false;
    onclose?.();
  }}
>
  <h2>{title}</h2>
  <p class="lead">{lead}</p>
  {#if note}<p class="note">{note}</p>{/if}
  <div class="actions">
    <Button variant="ghost" onclick={() => dlg?.close()}>Cancel</Button>
    <Button variant="danger" disabled={busy} onclick={onconfirm}>{confirmLabel}</Button>
  </div>
</dialog>

<style>
  .lead {
    margin: 0;
    font-size: 14px;
    line-height: 1.55;
  }
  /* Set apart rather than merely spoken: what users fear losing here is a file, and
     the sentence that says which files go and which stay is the one they reread. */
  .note {
    margin: 12px 0 0;
    padding: 10px 12px;
    font-size: 13px;
    line-height: 1.5;
    color: var(--fg-muted);
    background: var(--canvas-subtle);
    border-radius: var(--radius);
  }
  .actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
    margin-top: 18px;
  }
</style>
