import type { Asset, Chat, Citation, Message, Notebook, Segment, Source } from './types';
import type { Settings } from './config.gen';

/** The one place in the frontend that talks to /api. */

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  // Only JSON bodies get the type header: a FormData body must keep the
  // multipart boundary the browser generates for it.
  if (typeof init?.body === 'string' && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }

  const res = await fetch(path, { ...init, headers });

  const body = await res.text();
  let data: unknown = null;
  if (body) {
    try {
      data = JSON.parse(body);
    } catch {
      // A non-JSON body (proxy error page, truncated stream) still needs to
      // surface as an error rather than a parse failure.
      throw new Error(`${res.status} ${res.statusText}: ${body.slice(0, 200)}`);
    }
  }

  if (!res.ok) {
    const message = (data as { error?: string } | null)?.error;
    throw new Error(message ?? `${res.status} ${res.statusText}`);
  }
  return data as T;
}

/** Go marshals an empty slice as null, and callers should never see that. */
const list = <T>(value: T[] | null): T[] => value ?? [];

// ── settings ─────────────────────────────────────────────────────

/** Effective configuration: environment defaults overlaid with the settings file. */
export function getSettings(): Promise<Settings> {
  return request<Settings>('/api/settings');
}

/** Partial update; absent keys keep their current value. Returns the result. */
export function putSettings(settings: Settings): Promise<Settings> {
  return request<Settings>('/api/settings', {
    method: 'PUT',
    body: JSON.stringify(settings)
  });
}

// ── notebooks ────────────────────────────────────────────────────

export async function listNotebooks(): Promise<Notebook[]> {
  return list(await request<Notebook[] | null>('/api/notebooks'));
}

export function createNotebook(name: string): Promise<Notebook> {
  return request<Notebook>('/api/notebooks', { method: 'POST', body: JSON.stringify({ name }) });
}

export function renameNotebook(id: string, name: string): Promise<void> {
  return request<void>(`/api/notebooks/${id}`, { method: 'PATCH', body: JSON.stringify({ name }) });
}

export function deleteNotebook(id: string): Promise<void> {
  return request<void>(`/api/notebooks/${id}`, { method: 'DELETE' });
}

// ── sources ──────────────────────────────────────────────────────

export async function listSources(notebookId: string): Promise<Source[]> {
  return list(await request<Source[] | null>(`/api/notebooks/${notebookId}/sources`));
}

/**
 * Uploads one file. The response is the source in `processing` state — the
 * server answers as soon as the bytes are stored, and ingest continues in the
 * background, so the caller polls (or reloads) to see the outcome.
 */
export function uploadSource(notebookId: string, file: File): Promise<Source> {
  const form = new FormData();
  form.append('file', file, file.name);
  return request<Source>(`/api/notebooks/${notebookId}/sources`, { method: 'POST', body: form });
}

/** Removes a source from a notebook. The file and its derived data are kept. */
export function detachSource(notebookId: string, sourceId: string): Promise<void> {
  return request<void>(`/api/notebooks/${notebookId}/sources/${sourceId}`, { method: 'DELETE' });
}

export function getSource(sourceId: string): Promise<Source> {
  return request<Source>(`/api/sources/${sourceId}`);
}

/**
 * Re-runs a source through the whole pipeline from its kept original: chunks,
 * page rasters and vectors all derived again. Use it when a source failed — the
 * embedding server was not reachable when the file arrived, say — and again when
 * the pipeline changed underneath a source that worked.
 *
 * Answers 202 with the source back in `processing`, so the caller keeps polling
 * the row it already has. A source that is processing is refused with 400.
 */
export function reingestSource(sourceId: string): Promise<Source> {
  return request<Source>(`/api/sources/${sourceId}/reingest`, { method: 'POST' });
}

export async function listSegments(sourceId: string): Promise<Segment[]> {
  return list(await request<Segment[] | null>(`/api/sources/${sourceId}/segments`));
}

// ── stored files ─────────────────────────────────────────────────

/** The kept original, streamed by the backend. Open in a tab, or seek with Range. */
export function originalURL(sourceId: string): string {
  return `/api/sources/${sourceId}/original`;
}

/** A derived page raster or audio slice. The asset id contains slashes, which
 * are the path here, so only each segment is escaped. */
export function assetURL(asset: Pick<Asset, 'id'>): string {
  return `/api/assets/${asset.id.split('/').map(encodeURIComponent).join('/')}`;
}

// ── the conversation ───────────────────────────────────────────────

export async function listChats(notebookId: string): Promise<Chat[]> {
  return list(await request<Chat[] | null>(`/api/notebooks/${notebookId}/chats`));
}

/** A conversation's title, after its reader decided what it should be. */
export function renameChat(notebookId: string, chatId: string, title: string): Promise<void> {
  return request<void>(`/api/notebooks/${notebookId}/chats/${chatId}`, {
    method: 'PATCH',
    body: JSON.stringify({ title })
  });
}

export function createChat(notebookId: string, title: string): Promise<Chat> {
  return request<Chat>(`/api/notebooks/${notebookId}/chats`, {
    method: 'POST',
    body: JSON.stringify({ title })
  });
}

export function deleteChat(notebookId: string, chatId: string): Promise<void> {
  return request<void>(`/api/notebooks/${notebookId}/chats/${chatId}`, { method: 'DELETE' });
}

export async function listMessages(notebookId: string, chatId: string): Promise<Message[]> {
  return list(
    await request<Message[] | null>(`/api/notebooks/${notebookId}/chats/${chatId}/messages`)
  );
}

/** What a running question reports. Every callback is optional. */
export type AskEvents = {
  /** A sentence for the status line. Never part of the answer, never stored. */
  onStatus?: (text: string) => void;
  /** Fires once, before the answer streams, so the sources can be shown while it is written. */
  onCitations?: (citations: Citation[]) => void;
  /** The answer arriving. `reasoning` is a model thinking out loud: show it as
   * progress, never append it to the answer. */
  onDelta?: (text: string, reasoning: string) => void;
  /** The finished message, exactly as the server stored it. */
  onDone?: (message: Message) => void;
};

/**
 * Ask one question and stream what happens, resolving when the turn ends.
 *
 * `fetch` rather than `EventSource` because the request is a POST with a body. The
 * `signal` is the stop button: the backend treats the client hanging up as the
 * cancellation, which is why there is no stop endpoint to fall out of sync with.
 */
export async function ask(
  notebookId: string,
  chatId: string,
  question: string,
  events: AskEvents,
  signal?: AbortSignal
): Promise<void> {
  const res = await fetch(`/api/notebooks/${notebookId}/chats/${chatId}/ask`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ question }),
    signal
  });

  // A refusal happens before the stream opens, so it is a normal JSON error and
  // deserves the same message the rest of the API produces.
  if (!res.ok) {
    const body = await res.text();
    let message = `${res.status} ${res.statusText}`;
    try {
      message = (JSON.parse(body) as { error?: string }).error ?? message;
    } catch {
      if (body) message = `${message}: ${body.slice(0, 200)}`;
    }
    throw new Error(message);
  }
  if (!res.body) throw new Error('the answer stream is empty');

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    // A frame is a block terminated by a blank line. Only complete blocks are
    // parsed, because the network can split one anywhere — including mid-JSON.
    let cut = buffer.indexOf(FRAME_END);
    while (cut >= 0) {
      dispatch(buffer.slice(0, cut), events);
      buffer = buffer.slice(cut + FRAME_END.length);
      cut = buffer.indexOf(FRAME_END);
    }
  }
}

const FRAME_END = '\n\n';

function dispatch(frame: string, events: AskEvents) {
  let name = 'message';
  const data: string[] = [];
  for (const line of frame.split('\n')) {
    if (line.startsWith('event: ')) name = line.slice(7).trim();
    else if (line.startsWith('data: ')) data.push(line.slice(6));
  }
  if (!data.length) return;

  let payload: unknown;
  try {
    payload = JSON.parse(data.join('\n'));
  } catch {
    return; // a frame we cannot read is not worth losing the rest of the answer over
  }

  switch (name) {
    case 'status':
      events.onStatus?.((payload as { text?: string }).text ?? '');
      break;
    case 'citations':
      events.onCitations?.(payload as Citation[]);
      break;
    case 'delta': {
      const d = payload as { text?: string; reasoning?: string };
      events.onDelta?.(d.text ?? '', d.reasoning ?? '');
      break;
    }
    case 'done':
      events.onDone?.(payload as Message);
      break;
    case 'error':
      throw new Error((payload as { error?: string }).error ?? 'the question failed');
  }
}
