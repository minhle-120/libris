<script lang="ts">
  import { attachChips, trimPartialLabel } from '../lib/chunks';
  import { renderRich } from '../lib/markdown';
  import type { Citation } from '../lib/types';

  /**
   * One rendered answer body: markdown, math and code, with citation chips attached.
   *
   * It has no styles of its own — the content is built by hand rather than by Svelte, so
   * scoped component CSS cannot reach it. `.md` in `app.css` is where it is dressed,
   * alongside the KaTeX and highlight.js token colours.
   */
  let {
    text,
    citations = [],
    oncite = undefined,
    streaming = false
  }: {
    text: string;
    citations?: Citation[];
    oncite?: (citation: Citation) => void;
    /** Draw the caret at the end of the answer, where the next token will land. */
    streaming?: boolean;
  } = $props();

  let host = $state<HTMLDivElement | undefined>();

  $effect(() => {
    // Both reads are what re-runs this: a stored answer when a chat is re-read, and a
    // streaming one on every token. Re-rendering the whole body is the simple thing and
    // the cheap thing at the sizes an answer reaches; the registry hands up its citations
    // once, before the answer starts, so the second dependency rarely moves.
    // A half-written citation label at the end of the buffer is held back, so the grammar the
    // model was told to use never reaches the reader as text — not even for one frame.
    const source = streaming ? trimPartialLabel(text) : text;
    const found = citations;
    const el = host;
    if (!el) return;

    const fragment = renderRich(source);
    attachChips(fragment, found, (citation) => oncite?.(citation));
    if (streaming) appendCaret(fragment);
    el.replaceChildren(fragment);
  });

  /**
   * The caret belongs on the line the answer is being written on, which means inside the
   * last block rather than under it — a caret alone on its own line after a list reads as
   * a second cursor. Math is left alone: KaTeX owns the layout inside its own span, and a
   * stray element there moves the fraction bars.
   */
  function appendCaret(root: DocumentFragment): void {
    const DESCEND = new Set([
      'p', 'ul', 'ol', 'li', 'blockquote', 'table', 'tbody', 'tr', 'td', 'th', 'pre',
      'code', 'em', 'strong', 'del', 'a', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6'
    ]);
    let node: Element | null = root.lastElementChild;
    while (node?.lastElementChild && DESCEND.has(node.tagName.toLowerCase())) {
      node = node.lastElementChild;
    }
    const caret = document.createElement('span');
    caret.className = 'caret';
    if (!node) root.append(caret);
    else if (DESCEND.has(node.tagName.toLowerCase())) node.append(caret);
    else node.after(caret); // math, or anything else whose inside is not ours to enter
  }
</script>

<div class="md" bind:this={host}></div>
