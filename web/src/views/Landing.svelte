<script lang="ts">
  import { app } from '../lib/store.svelte';
  import Button from '../lib/ui/Button.svelte';

  /**
   * The landing: no notebook is picked, what a notebook *is*, what goes in, and that nothing
   * leaves the machine — that last clause is a promise, not decoration, so it belongs here
   * rather than buried in settings.
   */
</script>

<section class="landing">
  <div class="inner">
    <h1>Nothing open</h1>
    <p>
      A notebook is a set of sources you ask questions about. Add PDFs, images or text, and every
      answer comes back from them, citing the page.
    </p>
    <p class="muted">It all stays on this machine.</p>
    <Button variant="primary" onclick={() => (app.newNotebook = true)}>New notebook</Button>
  </div>
</section>

<style>
  .landing {
    display: grid;
    place-items: center;
    min-height: 100%;
    padding: 32px;
  }

  .inner {
    max-width: 34rem;
    display: grid;
    gap: 10px;
    justify-items: start;
  }

  h1 {
    font-size: 18px;
    font-weight: 600;
  }

  p {
    font-size: 14px;
    line-height: 1.6;
  }

  .inner :global(.btn) {
    margin-top: 6px;
  }
</style>
