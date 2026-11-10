// Code generated from internal/config. DO NOT EDIT.
// Edit internal/config/config.go, then run: make generate

export type SettingKind = 'text' | 'url' | 'password' | 'number' | 'bool' | 'enum';
export type SettingOption = { value: string; label: string };
export type SettingField = {
	key: keyof Settings;
	label: string;
	help: string;
	kind: SettingKind;
	unit?: string;
	min?: number;
	max?: number;
	readonly?: boolean;
	options?: SettingOption[];
};
export type SettingGroup = { id: string; label: string; help: string; fields: SettingField[] };

export type Settings = {
	chatURL: string;
	embedURL: string;
	rerankURL: string;
	apiKey: string;
	timeoutSeconds: number;
	chatModel: string;
	contextSize: number;
	gpuLayers: number;
	embedModel: string;
	embedDimensions: string;
	rerankModel: string;
	rerankEnabled: boolean;
	recallTopK: number;
	finalTopK: number;
	maxIterations: number;
	historyMessages: number;
	chunkWords: number;
	chunkOverlap: number;
	pdfPageImages: 'always' | 'fallback';
	pdfDPI: number;
	audioWindowSeconds: number;
	maxFileMB: number;
	keepOriginals: boolean;
};

export const settingGroups: SettingGroup[] = [
	{
		"id": "connection",
		"label": "llama.cpp servers",
		"help": "One process per model. Separate ports keep embedding and chat from starving each other.",
		"fields": [
			{
				"key": "chatURL",
				"label": "Chat server",
				"help": "Text, image and audio input.",
				"kind": "url"
			},
			{
				"key": "embedURL",
				"label": "Embedding server",
				"help": "Multimodal embeddings for text, page images and audio.",
				"kind": "url"
			},
			{
				"key": "rerankURL",
				"label": "Rerank server",
				"help": "Optional. Degrades to vector order when unset.",
				"kind": "url"
			},
			{
				"key": "apiKey",
				"label": "API key",
				"help": "Sent as a bearer token to all three servers.",
				"kind": "password"
			},
			{
				"key": "timeoutSeconds",
				"label": "Request timeout",
				"help": "Local CPU inference is slow. Lower it for GPU.",
				"kind": "number",
				"unit": "s",
				"min": 5,
				"max": 3600
			}
		]
	},
	{
		"id": "models",
		"label": "Models",
		"help": "Names are what the server reports. Changing the embedder re-indexes every source.",
		"fields": [
			{
				"key": "chatModel",
				"label": "Chat model",
				"help": "Must support tool calls for the research loop.",
				"kind": "text"
			},
			{
				"key": "contextSize",
				"label": "Context window",
				"help": "n_ctx per slot: the KV cache budget.",
				"kind": "number",
				"unit": "tokens",
				"min": 512,
				"max": 1048576
			},
			{
				"key": "gpuLayers",
				"label": "GPU layers",
				"help": "-1 for all, 0 for CPU only.",
				"kind": "number",
				"min": -1,
				"max": 999
			},
			{
				"key": "embedModel",
				"label": "Embedding model",
				"help": "EmbeddingGemma 2: text, image and audio in one space.",
				"kind": "text"
			},
			{
				"key": "embedDimensions",
				"label": "Embedding dimensions",
				"help": "Probed once at startup and recorded with every segment.",
				"kind": "text",
				"readonly": true
			},
			{
				"key": "rerankModel",
				"label": "Reranker",
				"help": "bge-reranker-v2-m3, or a VL reranker for images.",
				"kind": "text"
			},
			{
				"key": "rerankEnabled",
				"label": "Enable reranking",
				"help": "Off means pure vector order.",
				"kind": "bool"
			}
		]
	},
	{
		"id": "retrieval",
		"label": "Retrieval",
		"help": "Research loop budget. Every extra iteration costs one search round trip.",
		"fields": [
			{
				"key": "recallTopK",
				"label": "Recall depth",
				"help": "Candidates pulled from the vector scan, before reranking.",
				"kind": "number",
				"unit": "candidates",
				"min": 1,
				"max": 100
			},
			{
				"key": "finalTopK",
				"label": "Evidence per search",
				"help": "What the research agent sees in a tool result.",
				"kind": "number",
				"unit": "chunks",
				"min": 1,
				"max": 50
			},
			{
				"key": "maxIterations",
				"label": "Max iterations",
				"help": "Deeper multi-hop research; diminishing returns past 3.",
				"kind": "number",
				"min": 1,
				"max": 10
			},
			{
				"key": "historyMessages",
				"label": "History window",
				"help": "Recent turns given to the synthesizer.",
				"kind": "number",
				"unit": "messages",
				"min": 0,
				"max": 64
			},
			{
				"key": "chunkWords",
				"label": "Chunk size",
				"help": "Target words per text chunk.",
				"kind": "number",
				"unit": "words",
				"min": 10,
				"max": 1000
			},
			{
				"key": "chunkOverlap",
				"label": "Chunk overlap",
				"help": "Words carried into the next chunk.",
				"kind": "number",
				"unit": "words",
				"min": 0,
				"max": 999
			}
		]
	},
	{
		"id": "ingest",
		"label": "Ingest",
		"help": "Extraction is a background job. Originals are always kept.",
		"fields": [
			{
				"key": "pdfPageImages",
				"label": "PDF embeddings",
				"help": "A PDF becomes two documents: page text and page rasters.",
				"kind": "enum",
				"options": [
					{
						"value": "always",
						"label": "Text + page image"
					},
					{
						"value": "fallback",
						"label": "Page image only when text is missing"
					}
				]
			},
			{
				"key": "pdfDPI",
				"label": "PDF raster DPI",
				"help": "Resolution for page images. Only affects how legible a cited page looks: the embedding cost per image is fixed.",
				"kind": "number",
				"unit": "dpi",
				"min": 50,
				"max": 600
			},
			{
				"key": "audioWindowSeconds",
				"label": "Audio window",
				"help": "Clip length for audio segments; align with transcript boundaries.",
				"kind": "number",
				"unit": "s",
				"min": 5,
				"max": 600
			},
			{
				"key": "maxFileMB",
				"label": "Max file size",
				"help": "Larger files are refused at the door, not mid-job.",
				"kind": "number",
				"unit": "MB",
				"min": 1,
				"max": 102400
			},
			{
				"key": "keepOriginals",
				"label": "Keep originals",
				"help": "Required for page and timestamp citations.",
				"kind": "bool",
				"readonly": true
			}
		]
	}
];

export function defaultSettings(): Settings {
	return {
		chatURL: "http://127.0.0.1:8081",
		embedURL: "http://127.0.0.1:8082",
		rerankURL: "http://127.0.0.1:8083",
		apiKey: "",
		timeoutSeconds: 300,
		chatModel: "qwen3-vl-8b-instruct-q4_k_m",
		contextSize: 16384,
		gpuLayers: -1,
		embedModel: "embeddinggemma-2-q8_0",
		embedDimensions: "",
		rerankModel: "bge-reranker-v2-m3-q8_0",
		rerankEnabled: true,
		recallTopK: 10,
		finalTopK: 5,
		maxIterations: 5,
		historyMessages: 8,
		chunkWords: 100,
		chunkOverlap: 50,
		pdfPageImages: "always",
		pdfDPI: 110,
		audioWindowSeconds: 30,
		maxFileMB: 512,
		keepOriginals: true,
	};
}
