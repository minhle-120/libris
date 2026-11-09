<script lang="ts">
  let {
    checked = $bindable(false),
    disabled = false,
    label
  }: { checked?: boolean; disabled?: boolean; label: string } = $props();
</script>

<label class="switch" class:disabled>
  <input type="checkbox" bind:checked {disabled} aria-label={label} />
  <span class="track"><span class="thumb"></span></span>
</label>

<style>
  .switch {
    display: inline-flex;
    cursor: pointer;
  }
  .switch.disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  input {
    position: absolute;
    opacity: 0;
    width: 0;
    height: 0;
  }
  .track {
    position: relative;
    width: 34px;
    height: 20px;
    background: var(--canvas-subtle);
    border: 1px solid var(--border);
    border-radius: 2em;
    transition: background 120ms ease, border-color 120ms ease;
  }
  .thumb {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    background: var(--fg-muted);
    border-radius: 50%;
    transition: transform 120ms ease, background 120ms ease;
  }
  input:checked + .track {
    background: var(--success-emphasis);
    border-color: transparent;
  }
  input:checked + .track .thumb {
    transform: translateX(14px);
    background: #ffffff;
  }
  input:focus-visible + .track {
    box-shadow: var(--focus-ring);
  }
</style>
