import type { Citation } from './types';

/**
 * The answer arrives with `[[Chunk N]]` labels inside it. That is the grammar the
 * synthesis prompt is instructed to use, and it is what makes an answer checkable, but
 * it is noise to a reader — so the labels are split out and rendered as buttons that
 * open the source at the cited place.
 *
 * Kept out of the component because the same split is needed twice over: for a stored
 * message, and for the answer arriving one token at a time.
 */

export type Piece =
  | { kind: 'text'; text: string }
  | { kind: 'chunk'; chunk: number; citation?: Citation };

const LABEL = /\[\[Chunk (\d+)\]\]/g;

/**
 * Split prose from chunk labels, attaching each label to the citation it names.
 *
 * A label with no citation behind it is dropped rather than rendered as a dead
 * button. That is not a defensive flourish: the registry only holds what the model was
 * actually shown, so `[[Chunk 99]]` means a model invented a source, and the honest
 * thing to do with an invented citation is to remove it from the answer and leave the
 * sentence standing. `[[chunk 1]]` is not dropped silently either — it is left as the
 * text it is, because silently deleting a malformed citation would hide a prompt bug.
 */
export function chunkPieces(text: string, citations: Citation[] = []): Piece[] {
  const byChunk = new Map<number, Citation>();
  for (const c of citations) byChunk.set(c.chunk, c);

  const pieces: Piece[] = [];
  let last = 0;
  for (const match of text.matchAll(LABEL)) {
    const at = match.index ?? 0;
    if (at > last) pieces.push({ kind: 'text', text: text.slice(last, at) });
    const chunk = Number(match[1]);
    const citation = byChunk.get(chunk);
    if (citation) pieces.push({ kind: 'chunk', chunk, citation });
    last = at + match[0].length;
  }
  if (last < text.length) pieces.push({ kind: 'text', text: text.slice(last) });
  return pieces;
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
