import type { Citation } from "./types";

/** Display formatting. Kept out of components so the same rules apply everywhere. */

export function formatBytes(bytes: number): string {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(
    Math.floor(Math.log(bytes) / Math.log(1024)),
    units.length - 1,
  );
  const value = bytes / 1024 ** i;
  // A byte count is a fact, not a measurement: one decimal is plenty.
  return `${value >= 10 || i === 0 ? Math.round(value) : value.toFixed(1)} ${units[i]}`;
}

/** Coarse on purpose. A source list wants "3h ago", not a timestamp to the second. */
export function formatAge(unixSeconds: number): string {
  const seconds = Math.max(0, Date.now() / 1000 - unixSeconds);
  if (seconds < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(unixSeconds * 1000).toLocaleDateString();
}

/** Page provenance, including the case where a chunk straddles a page break. */
/**
 * A clip of a recording is a range of seconds, shown as m:ss — the same shape the player
 * above it shows, so the label under a clip and the time inside it agree.
 */
export function formatSpan(t0: number, t1?: number): string {
  const at = (s: number) => {
    const m = Math.floor(s / 60);
    const rest = Math.round(s % 60);
    return `${m}:${String(rest).padStart(2, "0")}`;
  };
  return t1 ? `${at(t0)}\u2013${at(t1)}` : at(t0);
}

export function formatPages(page?: number, pageEnd?: number): string {
  if (!page) return "";
  return pageEnd && pageEnd !== page
    ? `pages ${page}–${pageEnd}`
    : `page ${page}`;
}

/**
 * Where inside a source a citation points. Both a page raster and a text chunk
 * cite a page, so there is one form — which is why the function exists at all:
 * audio used to cite a `m:ss–m:ss` range here, and that branch is what made the
 * kind a parameter. It is gone with the modality (ADR 0003).
 *
 * The character range a text chunk cites is left out on purpose — it is what the
 * viewer highlights when the citation is opened, not what a label says about it.
 */
export function citationLocator(c: Pick<Citation, "kind" | "span">): string {
  if (c.kind === "audio" && (c.span.t0 || c.span.t1))
    return formatSpan(c.span.t0!, c.span.t1);
  return formatPages(c.span.page, c.span.page_end);
}

/**
 * What a citation says about itself: the chunk's number, then where it points. Both the
 * row above an answer and the button inside its sentences come through here, so the two
 * cannot describe the same citation differently.
 */
/**
 * Which glyph a citation carries. A clip of a recording gets the waveform, so the row above an
 * answer says what kind of thing it points at before anyone opens it.
 */
export function citationIcon(kind: Citation["kind"]): "image" | "audio" | "file" {
  return kind === "image" ? "image" : kind === "audio" ? "audio" : "file";
}

export function chipLabel(c: Pick<Citation, "chunk" | "kind" | "span">): string {
  const where = citationLocator(c);
  return where ? `Chunk ${c.chunk} · ${where}` : `Chunk ${c.chunk}`;
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** A day, for facts about a machine rather than about a running job.
 *  RFC3339 in, one locale-appropriate date out; an absent timestamp says so in words. */
export function formatDate(iso?: string): string {
  if (!iso) return "unknown";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "unknown" : d.toLocaleDateString();
}

/** How long ago a clock said something, in words a reader can use. The device report is
 *  the one place the app shows an answer it did not compute just now, so it has to say so. */
export function formatSince(iso?: string): string {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (seconds < 60) return "moments ago";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} h ago`;
  return `${Math.round(hours / 24)} d ago`;
}

/** How long a model spent thinking, in the words its row is labelled with. An empty answer
 *  means nobody measured — which is a different fact from "no time", and the caller renders
 *  no claim at all when it hears one. */
export function formatDuration(ms?: number): string {
  if (!ms || ms < 0) return "";
  const seconds = ms / 1000;
  if (seconds < 1) return "under a second";
  // One decimal until ten seconds, because that is where reading the number stops being
  // useful — and a thinking model's pause is mostly under it.
  if (seconds < 10) return `${seconds.toFixed(1)}s`;
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = Math.round(seconds % 60);
  return rest ? `${minutes}m ${rest}s` : `${minutes}m`;
}
