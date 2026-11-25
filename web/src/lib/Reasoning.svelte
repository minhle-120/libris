<script module>
  let panels = 0;

  /** `aria-controls` has to name something, so every instance needs an id of its own —
   *  two answers in one conversation cannot share one. */
  function nextPanel() {
    return `reasoning-${++panels}`;
  }
</script>

<script lang="ts">
  import Icon from './ui/Icon.svelte';
  import { formatDuration } from './format';

  /**
   * What the model said to itself before it wrote the answer, behind the row that says how
   * long that took.
   *
   * Collapsed by default, because the thinking is neither the answer nor the evidence for
   * it — the citation chips above the prose are the evidence, and they are what a reader
   * weighing an answer needs. But it stays one click away rather than being thrown away: an
   * answer is easier to doubt, or to trust, with the reasoning behind it readable, and a
   * reader who only wonders about it after the fact deserves the same look as one who
   * happened to be watching while it streamed.
   *
   * The panel holds the thinking and no commentary on it. The chips already say where an
   * answer came from; a line here explaining that the thinking is not evidence would be a
   * third voice saying it, in the one place a reader has stopped reading to look.
   *
   * `live` is the same control while the thinking is still arriving. The caller holds it
   * open and closes it when the first sentence lands, which is the moment the reading stops
   * being a spectator of the work and becomes the work's result.
   */
  let {
    text = '',
    ms = 0,
    live = false,
    open = $bindable(false)
  }: { text?: string; ms?: number; live?: boolean; open?: boolean } = $props();

  const panel = nextPanel();

  // No number is a different fact from a small one: a stream cut short, or an answer
  // written by a model that never thought, says nothing about time rather than “0s”.
  const label = $derived(
    live ? 'Thinking…' : ms > 0 ? `Thought for ${formatDuration(ms)}` : 'Thought'
  );
</script>

<div class="think" class:open>
  <button
    class="row"
    type="button"
    aria-expanded={open}
    aria-controls={panel}
    onclick={() => (open = !open)}
  >
    <span class="chev" aria-hidden="true"><Icon name="expand" size={11} /></span>
    <span class="label">{label}</span>
    {#if live}<span class="caret" aria-hidden="true"></span>{/if}
  </button>

  <!-- Hidden rather than unmounted, so `aria-controls` names a real element whether or
       not the row is open. Nothing inside it takes focus, so nothing focusable is hidden. -->
  <div id={panel} class="panel" role="region" aria-label="The model's reasoning">
    <div class="scroll">{text}</div>
  </div>
</div>

<style>
  .think {
    margin-bottom: 6px;
  }

  .row {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    margin-left: -3px;
    padding: 3px 5px;
    border: 0;
    border-radius: var(--r);
    background: none;
    color: var(--muted);
    font: 500 var(--fs-xs)/1 var(--mono);
    cursor: pointer;
  }

  .row:hover {
    background: var(--hover);
    color: var(--text);
  }

  /* The chevron points along the direction the click goes: right when there is nothing
     below it, down when there is. */
  .chev {
    display: inline-flex;
    transition: transform 120ms ease-out;
  }

  .open .chev {
    transform: rotate(90deg);
  }

  .panel {
    display: none;
    margin: 2px 0 10px;
    padding-left: 10px;
    border-left: 2px solid var(--border);
    color: var(--muted);
    font-size: var(--fs-sm);
  }

  .open .panel {
    display: block;
    animation: think-in 120ms ease-out;
  }

  .scroll {
    max-height: min(20rem, 45vh);
    overflow: auto;
    line-height: var(--lh);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  @keyframes think-in {
    from {
      opacity: 0;
      transform: translateY(-2px);
    }
    to {
      opacity: 1;
      transform: none;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .chev,
    .open .panel,
    .caret {
      animation: none;
      transition: none;
    }
  }
</style>
