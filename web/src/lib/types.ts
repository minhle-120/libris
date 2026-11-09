// Mirrors internal/httpapi/dto.go once the backend exists.
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

export type Settings = {
  // llama.cpp
  chatUrl: string;
  embedUrl: string;
  rerankUrl: string;
  apiKey: string;
  timeoutSeconds: number;
  // models
  chatModel: string;
  contextSize: number;
  gpuLayers: number;
  embedModel: string;
  embedDim: number | null;
  rerankModel: string;
  rerankEnabled: boolean;
  // retrieval
  recallTopK: number;
  finalTopK: number;
  maxIterations: number;
  historyMessages: number;
  chunkSize: number;
  chunkOverlap: number;
  // ingest
  pdfPageImages: 'always' | 'fallback';
  pdfDPI: number;
  audioWindowSeconds: number;
  maxFileMB: number;
};

export function defaultSettings(): Settings {
  return {
    chatUrl: 'http://127.0.0.1:8081',
    embedUrl: 'http://127.0.0.1:8082',
    rerankUrl: 'http://127.0.0.1:8083',
    apiKey: '',
    timeoutSeconds: 300,
    chatModel: 'qwen3-vl-8b-instruct-q4_k_m',
    contextSize: 16384,
    gpuLayers: -1,
    embedModel: 'embeddinggemma-2-q8_0',
    embedDim: null,
    rerankModel: 'bge-reranker-v2-m3-q8_0',
    rerankEnabled: true,
    recallTopK: 10,
    finalTopK: 5,
    maxIterations: 5,
    historyMessages: 8,
    chunkSize: 100,
    chunkOverlap: 50,
    pdfPageImages: 'always',
    pdfDPI: 150,
    audioWindowSeconds: 30,
    maxFileMB: 512
  };
}
