<script lang="ts">
  import Icon from './Icon.svelte';
  import Button from './Button.svelte';

  /**
   * A failed *request* — not a failed job. One pattern for the whole app: a tinted banner at
   * the top of the main column, the server's own sentence, announced. No screen invents its
   * own error treatment, and no error is only a console message.
   */
  let { message, ondismiss }: { message: string; ondismiss?: () => void } = $props();
</script>

<p class="banner" role="alert">
  <Icon name="alert" />
  <span class="msg">{message}</span>
  {#if ondismiss}
    <Button variant="ghost" size="sm" ariaLabel="Dismiss" onclick={ondismiss}>
      <Icon name="x" size={13} />
    </Button>
  {/if}
</p>

<style>
  .msg {
    min-width: 0;
    overflow-wrap: anywhere;
  }
</style>
