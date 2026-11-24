import { chipLabel } from './format';
import type { Citation } from './types';

/**
 * The answer arrives with `[[Chunk N]]` labels inside it. That is the grammar the
 * synthesis prompt is instructed to use, and it is what makes an answer checkable, but
 * it is noise to a reader — so the labels become buttons that open the source at the
 * cited place.
 *
 * This runs on the *rendered* answer, after markdown and math, and that is the whole
 * design: a DOM pass cannot be provoked by an answer that quotes the grammar,
 * and it is blind inside `<code>`, `<pre>` and `.katex` without trying. So a PDF that
 * contains the literal text `[[Chunk 3]]` inside a code block cites nothing.
 */

const LABEL = /\[\[Chunk (\d+)\]\]/g;

/** Subtrees a label must not become a button in, whatever it says. */
const OFF_LIMITS = 'code, pre, .katex';

/**
 * Replace every well-formed label in `root` with a citation button.
 *
 * A label with no citation behind it is dropped rather than rendered as a dead button.
 * That is not a defensive flourish: the registry only holds what the model was actually
 * shown, so `[[Chunk 99]]` means a model invented a source, and the honest thing to do
 * with an invented citation is to remove it from the answer and leave the sentence
 * standing. `[[chunk 1]]` is not dropped silently either — it is left as the text it is,
 * because silently deleting a malformed citation would hide a prompt bug.
 */
export function attachChips(
  root: ParentNode,
  citations: Citation[],
  onCite: (citation: Citation) => void
): void {
  const byChunk = new Map<number, Citation>();
  for (const c of citations) byChunk.set(c.chunk, c);

  const found: Text[] = [];
  const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (node) =>
      node.parentElement?.closest(OFF_LIMITS) ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT
  });
  for (let n = walk.nextNode(); n; n = walk.nextNode()) found.push(n as Text);

  for (const node of found) {
    const text = node.data;
    LABEL.lastIndex = 0;
    if (!LABEL.test(text)) continue;
    LABEL.lastIndex = 0;

    const fragment = document.createDocumentFragment();
    let last = 0;
    for (const match of text.matchAll(LABEL)) {
      const at = match.index ?? 0;
      if (at > last) fragment.append(text.slice(last, at));
      const citation = byChunk.get(Number(match[1]));
      if (citation) fragment.append(chip(citation, onCite));
      last = at + match[0].length;
    }
    if (last < text.length) fragment.append(text.slice(last));
    node.replaceWith(fragment);
  }
}

function chip(citation: Citation, onCite: (citation: Citation) => void): HTMLButtonElement {
  const button = document.createElement('button');
  button.className = 'inline';
  button.type = 'button';
  // The number is the label and the locator is the tooltip, which is the same split the
  // chip row above the answer makes — one rule for both, in lib/format.ts.
  button.title = `${citation.source_name} — ${chipLabel(citation)}`;
  button.textContent = String(citation.chunk);
  button.addEventListener('click', () => onCite(citation));
  return button;
}

/**
 * A label can straddle two deltas, so mid-stream the buffer may end with `[[Chunk 1`
 * or `[[C`. Rendered as-is that flickers a fragment of the grammar at the reader; a
 * half-written label at the very end of the buffer is therefore held back until its
 * closing bracket lands. Applied only to the text still arriving — a finished answer
 * is shown exactly as it was stored, and dropping a stray there would be a lie.
 */
export function trimPartialLabel(text: string): string {
  const match = /\[\[(?:Chun(?:k(?: \d*)?)?)?]?$/.exec(text);
  return match ? text.slice(0, match.index) : text;
}
