import type { Asset, Notebook, Segment, Source } from './types';
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
