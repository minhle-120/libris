import type { Citation } from './types';

/** Display formatting. Kept out of components so the same rules apply everywhere. */

export function formatBytes(bytes: number): string {
  if (!bytes) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** i;
  // A byte count is a fact, not a measurement: one decimal is plenty.
  return `${value >= 10 || i === 0 ? Math.round(value) : value.toFixed(1)} ${units[i]}`;
}

/** Coarse on purpose. A source list wants "3h ago", not a timestamp to the second. */
export function formatAge(unixSeconds: number): string {
  const seconds = Math.max(0, Date.now() / 1000 - unixSeconds);
  if (seconds < 60) return 'just now';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(unixSeconds * 1000).toLocaleDateString();
}

/** Audio provenance is a range in seconds, shown as m:ss. */
export function formatSpan(t0: number, t1?: number): string {
  const at = (s: number) => {
    const m = Math.floor(s / 60);
    const rest = Math.round(s % 60);
    return `${m}:${String(rest).padStart(2, '0')}`;
  };
  return t1 ? `${at(t0)}–${at(t1)}` : at(t0);
}

/** Page provenance, including the case where a chunk straddles a page break. */
export function formatPages(page?: number, pageEnd?: number): string {
  if (!page) return '';
  return pageEnd && pageEnd !== page ? `pages ${page}–${pageEnd}` : `page ${page}`;
}

/**
 * Where inside a source a citation points, in the shortest form that is still
 * honest: audio cites a time range, a page raster and a text chunk both cite a page.
 *
 * The character range a text chunk cites is left out on purpose — it is what the
 * viewer highlights when the citation is opened, not what a label says about it.
 */
export function citationLocator(c: Pick<Citation, 'kind' | 'span'>): string {
  if (c.kind === 'audio' && (c.span.t0 || c.span.t1)) return formatSpan(c.span.t0, c.span.t1);
  return formatPages(c.span.page, c.span.page_end);
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
