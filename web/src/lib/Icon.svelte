<script lang="ts">
  let { name, size = 16 }: { name: string; size?: number } = $props();

  const paths: Record<string, string> = {
    search: 'M11 11m-7 0a7 7 0 1 0 14 0a7 7 0 1 0 -14 0M16.5 16.5L21 21',
    plus: 'M12 5v14M5 12h14',
    sun: 'M12 8a4 4 0 1 0 0 8a4 4 0 0 0 0-8M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4',
    moon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z',
    book: 'M4 5a2 2 0 0 1 2-2h13v18H6a2 2 0 0 1-2-2zM19 3v18',
    upload: 'M12 16V4M6 10l6-6 6 6M4 20h16',
    inbox: 'M4 13h4l2 3h4l2-3h4M4 13l2-8h12l2 8v5a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z',
    chat: 'M4 5a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H9l-5 4z',
    pulse: 'M3 12h4l2 6 4-14 2 8h6',
    settings:
      'M12 15a3 3 0 1 0 0-6a3 3 0 0 0 0 6M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2 2 2 0 1 1-4 0 1.7 1.7 0 0 0-2.9-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.7 1.7 0 0 0 2.6 15a2 2 0 1 1 0-4 1.7 1.7 0 0 0 1.8-2.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 2.9-1.8 2 2 0 1 1 4 0 1.7 1.7 0 0 0 2.9 1.8l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0 1.2 2.9 2 2 0 1 1 0 4 1.7 1.7 0 0 0-1.6 1Z',
    file: 'M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8zM14 3v5h5',
    image:
      'M4 5a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2zM4 17l5-5 4 4 3-3 4 4M9 9a1.5 1.5 0 1 0 0-3a1.5 1.5 0 0 0 0 3',
    audio: 'M9 18V6l11-2v12M9 18a3 3 0 1 1-6 0a3 3 0 0 1 6 0M20 16a3 3 0 1 1-6 0a3 3 0 0 1 6 0',
    trash: 'M4 7h16M10 4h4M6 7l1 13h10l1-13M10 11v6M14 11v6',
    x: 'M6 6l12 12M18 6L6 18',
    alert: 'M12 3l9 17H3zM12 9v5M12 17.5v.5',
    clock: 'M12 12m-9 0a9 9 0 1 0 18 0a9 9 0 1 0 -18 0M12 7v5l3.5 2',
    open: 'M14 4h6v6M20 4l-9 9M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4',
    layers: 'M12 2 2 7l10 5 10-5-10-5M2 17l10 5 10-5M2 12l10 5 10-5',
    retry: 'M23 4v6h-6M20.49 15a9 9 0 1 1-2.12-9.36L23 10',
    vector: 'M7 8m-3 0a3 3 0 1 0 6 0a3 3 0 1 0-6 0M17 16m-3 0a3 3 0 1 0 6 0a3 3 0 1 0-6 0M9.4 9.9 14.6 14.1'
  };
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="1.8"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
>
  <path d={paths[name] ?? ''} />
</svg>
