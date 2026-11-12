// Types shared with the Go backend. Field names are snake_case because that is
// what internal/domain marshals — the JSON, not the Go, is the contract.
//
// Settings are NOT defined here: internal/config generates ./config.gen.ts,
// which is the single source for defaults, labels, ranges and enums.
export type { Settings, SettingField, SettingGroup, SettingKind } from './config.gen';
export { defaultSettings, settingGroups } from './config.gen';

export type Notebook = {
  id: string;
  name: string;
  created_at: number; // unix seconds
  source_count: number; // read from the database, so it is 0 on a brand-new notebook
};

export type SourceKind = 'pdf' | 'image' | 'audio' | 'text' | 'unsupported';

/**
 * Async ingest state. `processing` covers everything from queue to chunking —
 * there is no finer progress signal until ingest emits SSE events.
 */
export type SourceStatus = 'processing' | 'ready' | 'unsupported' | 'failed';

export type Source = {
  id: string; // sha256 of the contents
  name: string;
  kind: SourceKind;
  sha256: string;
  bytes: number;
  status: SourceStatus;
  detail?: string; // why unsupported or failed
  warnings?: string[];
  added_at: number;
  segment_count: number;
};

export type Asset = {
  id: string; // also the URL path under /api/assets/
  // No path field on purpose: absolute paths stay on the server, and files are
  // fetched by ID through /api/assets/ or /api/sources/{id}/original.
  mime: string;
  bytes: number;
};

/**
 * Points back into the original. Char offsets are page-local; `page_end` is
 * present only when a chunk straddles a page break. `t0` has no optional marker
 * because the first audio slice legitimately starts at zero.
 */
export type Provenance = {
  page?: number;
  page_end?: number;
  char_start?: number;
  char_end?: number;
  t0: number;
  t1?: number;
};

export type Segment = {
  id: string;
  source_id: string;
  doc_id: string; // a PDF yields two documents: the text and the page rasters
  kind: 'text' | 'image' | 'audio';
  ordinal: number;
  text: string;
  asset?: Asset;
  span: Provenance;
};

export type Tab = { id: string; label: string; count?: number };

// ── the conversation ──────────────────────────────────────────────

/** One conversation inside a notebook. `title` is the first question asked. */
export type Chat = {
  id: string;
  notebook_id: string;
  title: string;
  created_at: number;
};

export type MessageRole = 'user' | 'assistant';

/**
 * How an assistant turn ended. These are different claims about the sentence in
 * front of the reader, so they are drawn differently: an unchecked answer must not
 * look like a grounded one, and an interrupted one says so.
 */
export type MessageStatus =
  | 'answered'
  | 'no-info'
  | 'ungrounded'
  | 'canceled'
  | 'interrupted';

/**
 * What a `[[Chunk N]]` label resolved to, frozen when the answer was written.
 *
 * Self-contained on purpose: re-ingesting a source deletes and rewrites its segment
 * rows, so an old answer stored as a list of segment ids would lose its sources. The
 * quote, the span and the asset are copied out, and history still renders, still
 * cites and still opens.
 */
export type Citation = {
  chunk: number;
  segment_id: string;
  doc_id: string;
  source_id: string;
  source_name: string;
  kind: 'text' | 'image' | 'audio';
  ordinal: number;
  quote?: string;
  /** The page raster or audio slice a citation opens. */
  asset?: Asset;
  span: Provenance;
};

export type Message = {
  id: number;
  chat_id: string;
  role: MessageRole;
  content: string;
  status?: MessageStatus;
  created_at: number;
  citations?: Citation[];
};
