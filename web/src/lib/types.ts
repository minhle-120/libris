// Types shared with the Go backend.
//
// Settings are NOT defined here: internal/config generates ./config.gen.ts,
// which is the single source for defaults, labels, ranges and enums.
export type { Settings, SettingField, SettingGroup, SettingKind } from './config.gen';
export { defaultSettings, settingGroups } from './config.gen';

export type Notebook = { id: string; name: string; sourceCount: number };

export type SourceKind = 'pdf' | 'image' | 'audio' | 'text';
export type SourceStatus = 'queued' | 'extracting' | 'chunking' | 'embedding' | 'ready' | 'failed';
export type Source = { id: string; name: string; kind: SourceKind; status: SourceStatus };

export type Tab = { id: string; label: string; count?: number };

export type Citation = {
  chunk: number;
  sourceId: string;
  sourceName: string;
  page?: number;
  t0?: number;
  t1?: number;
};
