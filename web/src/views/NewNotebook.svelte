<script lang="ts">
  import { app, createNotebook } from '../lib/store.svelte';
  import Modal from '../lib/ui/Modal.svelte';

  /**
   * One field for the name. Enter creates; the primary button is disabled while the name is
   * blank — the server refuses an unnamed notebook too, and refusing here keeps that from being
   * the first thing anyone learns about the feature.
   */
  let name = $state('');
  let busy = $state(false);
  let error = $state('');

  async function create() {
    const trimmed = name.trim();
    if (!trimmed || busy) return;
    busy = true;
    error = '';
    try {
      await createNotebook(trimmed);
      name = '';
      app.newNotebook = false;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<Modal
  klass="new-notebook"
  bind:open={app.newNotebook}
  title="New notebook"
  confirm="Create"
  confirmDisabled={name.trim() === ''}
  busy={busy}
  {error}
  onconfirm={create}
>
  <input
    class="input"
    type="text"
    placeholder="Name"
    autocomplete="off"
    aria-label="Notebook name"
    bind:value={name}
    onkeydown={(e) => e.key === 'Enter' && create()}
  />
</Modal>
