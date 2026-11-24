import { chipLabel } from './format';
import type { Citation, Segment } from './types';

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

/**
 * The stored chunks of one source, in reading order.
 *
 * A segment *is* a stored chunk — the unit the search returns and the viewer shows — so the
 * number a reader sees here is its position in the file. That is *not* the number in an
 * answer's `[[Chunk N]]` labels, which indexes the handful of candidates that one question
 * was shown; the two are different spaces and the viewer says so rather than letting a
 * reader hunt for "Chunk 3" of a file that has four.
 */
export function chunksOf(segments: Segment[], sourceId: string): Segment[] {
  return segments
    .filter((s) => s.source_id === sourceId)
    .slice()
    .sort((a, b) => a.ordinal - b.ordinal);
}

/**
 * The text a chunk repeats from the one before it: the longest stretch that is *both* the
 * tail of the previous chunk and the head of this one — found, not assumed.
 *
 * The chunker's overlap setting is a request, not a promise: page headers are stripped,
 * hyphenated words are joined, and a chunk that runs short leaves nothing to repeat. A
 * shading computed from the setting would then colour words that do not actually appear
 * twice, which is worse than shading nothing, because it teaches the reader to trust a mark
 * that lies. The scan is a prefix function over `next NUL prev`: its final value is exactly
 * that longest overlap, in linear time, on chunks long enough that trying every split would
 * not be.
 */
export function repeatOf(prev: string, next: string): string {
  const cap = Math.min(prev.length, next.length, 2000);
  if (cap === 0) return '';
  const needle = `${next.slice(0, cap)}\u0000${prev.slice(-cap)}`;
  const pi = new Uint32Array(needle.length);
  for (let i = 1; i < needle.length; i++) {
    let k = pi[i - 1];
    while (k > 0 && needle[i] !== needle[k]) k = pi[k - 1];
    if (needle[i] === needle[k]) k++;
    pi[i] = k;
  }
  // The last entry is capped by the length of `next`'s prefix, and never reaches the
  // separator, so it is a real suffix of `prev` and a real prefix of `next`.
  let n = pi[needle.length - 1];
  // Trim to a whole word at both ends, so a shaded run is a phrase and not two word
  // fragments. Trimming a suffix and a prefix by the same amount keeps both halves true.
  while (n > 0 && !/\s/.test(next[n - 1] ?? ' ') && !/\s/.test(next[n] ?? ' ')) n--;
  while (n > 0 && /\s/.test(next[n - 1] ?? ' ')) n--;
  return next.slice(0, n);
}
