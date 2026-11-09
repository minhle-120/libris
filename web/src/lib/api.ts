import type { Settings } from './config.gen';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...init
  });

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
