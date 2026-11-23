// Types shared with the Go backend. Field names are snake_case because that is
// what internal/domain marshals — the JSON, not the Go, is the contract.
//
// Settings are NOT defined here: internal/config generates ./config.gen.ts,
// which is the single source for defaults, labels, ranges and enums.
export type {
  Settings,
  SettingField,
  SettingGroup,
  SettingKind,
} from "./config.gen";
export { defaultSettings, settingGroups } from "./config.gen";

export type Notebook = {
  id: string;
  name: string;
  created_at: number; // unix seconds
  source_count: number; // read from the database, so it is 0 on a brand-new notebook
};

export type SourceKind = "pdf" | "image" | "audio" | "text" | "unsupported";

/**
 * Async ingest state. `processing` covers everything from queue to chunking —
 * there is no finer progress signal until ingest emits SSE events.
 */
export type SourceStatus = "processing" | "ready" | "unsupported" | "failed";

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
  /**
   * How many notebooks list this file. Removing it from the last one deletes the
   * original and everything derived from it, so the row that says `1` is the row
   * whose trash can costs something — and the tooltip says so.
   */
  notebooks_using: number;
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
  kind: "text" | "image" | "audio";
  ordinal: number;
  text: string;
  asset?: Asset;
  span: Provenance;
};

/** The two views the right-hand panel offers. Chat is not one of them: the chat is
 * the page the panel sits beside, not a place you navigate to from it. */
export type Panel = "sources" | "activity";

/** One level of the header breadcrumb. No `onclick` means "this is the page you are
 * on", which is the only honest reason a crumb is not a link. */
export type Crumb = { label: string; onclick?: () => void };

// ── the conversation ──────────────────────────────────────────────

/** One conversation inside a notebook. `title` is the first question asked. */
export type Chat = {
  id: string;
  notebook_id: string;
  title: string;
  created_at: number;
};

export type MessageRole = "user" | "assistant";

/**
 * How an assistant turn ended. These are different claims about the sentence in
 * front of the reader, so they are drawn differently: an unchecked answer must not
 * look like a grounded one, and an interrupted one says so.
 */
export type MessageStatus =
  "answered" | "no-info" | "ungrounded" | "canceled" | "interrupted";

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
  kind: "text" | "image" | "audio";
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

// ── the machine ──────────────────────────────────────────────────
// These mirror the JSON of /api/host/*, which is deviceinfo.Report and app.EngineView
// marshalled by internal/domain's rules — snake_case, and an empty list is null.

export type EngineState =
  "idle" | "downloading" | "verifying" | "unpacking" | "done" | "failed";

export type EngineProgress = {
  state: EngineState;
  tag?: string;
  asset?: string;
  done: number;
  total: number;
  error?: string;
  since?: string;
  resumed?: boolean;
};

export type InstalledEngine = {
  tag: string;
  backend: string;
  path: string;
  bytes: number;
  installed_at: string;
  sha256?: string;
  upstream?: string;
};

/** One round trip answers the whole section: what is here, what is new, how the
 *  download is going, and how much a stopped one is still holding. */
export type EngineView = {
  installed?: InstalledEngine;
  backend: string;
  latest?: { tag: string; published: string; url?: string };
  asset?: { name: string; url: string; size: number; sha256?: string };
  update_available: boolean;
  check_error?: string;
  /** A check is running in the background and a later poll will carry its answer. Absent
   *  means there is nothing in flight — which is not the same as having failed. */
  checking?: boolean;
  progress: EngineProgress;
  dir: string;
  parts_bytes: number;
};

export type Device = {
  kind: string;
  name: string;
  total_bytes?: number;
  bus_id?: string;
  bus_vendor?: string;
  driver?: string;
  description?: string;
  free_bytes?: number;
  source?: string;
};

export type DeviceReport = {
  os: string;
  arch: string;
  devices: Device[];
  warnings?: string[];
  fingerprint?: string;
  checked_at: string;
  /** Set only when the server read its cache: "somebody asked this morning", not "we asked". */
  cached?: boolean;
};
