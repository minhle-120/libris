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
	/** Saved, validated and generated like the rest; rendered by a hand-written control elsewhere. */
	hidden?: boolean;
	options?: SettingOption[];
	/** Which engine mode this field means anything in; absent means both. */
	mode?: 'managed' | 'external';
};
export type SettingGroup = { id: string; label: string; help: string; fields: SettingField[] };

export type Settings = {
	engineMode: 'managed' | 'external';
	enginePort: number;
	engineDevice: string;
	engineDeviceAck: string;
	chatURL: string;
	embedURL: string;
	timeoutSeconds: number;
	chatModel: string;
	embedModel: string;
	embedDimensions: string;
	recallTopK: number;
	finalTopK: number;
	maxIterations: number;
	researchThinking: boolean;
	historyMessages: number;
	chunkWords: number;
	chunkOverlap: number;
	pdfPageImages: 'always' | 'fallback';
	pdfDPI: number;
	maxFileMB: number;
	keepOriginals: boolean;
};

export const settingGroups: SettingGroup[] = [
	{
		"id": "servers",
		"label": "Model servers",
		"help": "Either libris runs llama.cpp for you, or it talks to servers you started. The switch decides which of these fields exist — libris does not ask for an address it is about to compute.",
		"fields": [
			{
				"key": "engineMode",
				"label": "Who runs them",
				"help": "Libris can download llama.cpp, run it, and derive the addresses itself — or talk to servers you started. Not both.",
				"kind": "enum",
				"options": [
					{
						"value": "managed",
						"label": "Download and run llama.cpp"
					},
					{
						"value": "external",
						"label": "Servers I run myself"
					}
				]
			},
			{
				"key": "enginePort",
				"label": "Server port",
				"help": "0 lets libris take a free port each time the server starts. A number asks for that exact port, and the start fails if something else already holds it. Chat and embeddings share whatever port is chosen.",
				"kind": "number",
				"min": 1024,
				"max": 65535,
				"mode": "managed"
			},
			{
				"key": "engineDevice",
				"label": "Run models on",
				"help": "Which device llama.cpp offloads to. The choices are the slots the installed engine reports, CPU among them.",
				"kind": "text",
				"hidden": true,
				"mode": "managed"
			},
			{
				"key": "engineDeviceAck",
				"label": "Device choice confirmed",
				"help": "Written by the setup screen when the device row is confirmed. Blank means nobody has been asked yet.",
				"kind": "text",
				"hidden": true,
				"mode": "managed"
			},
			{
				"key": "chatURL",
				"label": "Chat server",
				"help": "Text and image input.",
				"kind": "url",
				"mode": "external"
			},
			{
				"key": "embedURL",
				"label": "Embedding server",
				"help": "Multimodal embeddings for text and page images.",
				"kind": "url",
				"mode": "external"
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
		"help": "The model that answers, and the one that finds. Changing the embedder re-indexes every source.",
		"fields": [
			{
				"key": "chatModel",
				"label": "Chat model",
				"help": "The name your chat server answers to. Must support tool calls for the research loop.",
				"kind": "text",
				"mode": "external"
			},
			{
				"key": "embedModel",
				"label": "Embedding model",
				"help": "The name your embedding server answers to.",
				"kind": "text",
				"mode": "external"
			},
			{
				"key": "embedDimensions",
				"label": "Embedding dimensions",
				"help": "Probed once at startup and recorded with every segment.",
				"kind": "text",
				"readonly": true
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
				"help": "Candidates pulled from the vector scan, best first.",
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
				"key": "researchThinking",
				"label": "Think before searching",
				"help": "Off sends the search request straight away. On lets the model reason first, which costs time each iteration and sometimes picks a better search. The answer's own thinking is unaffected.",
				"kind": "bool"
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
		"help": "Extraction is a background job. An original is kept until no notebook uses it.",
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
				"help": "Required for citations that open the original file.",
				"kind": "bool",
				"readonly": true
			}
		]
	}
];

export function defaultSettings(): Settings {
	return {
		engineMode: "managed",
		enginePort: 0,
		engineDevice: "",
		engineDeviceAck: "",
		chatURL: "http://127.0.0.1:8081",
		embedURL: "http://127.0.0.1:8082",
		timeoutSeconds: 300,
		chatModel: "",
		embedModel: "",
		embedDimensions: "",
		recallTopK: 10,
		finalTopK: 5,
		maxIterations: 5,
		researchThinking: false,
		historyMessages: 8,
		chunkWords: 100,
		chunkOverlap: 50,
		pdfPageImages: "always",
		pdfDPI: 110,
		maxFileMB: 512,
		keepOriginals: true,
	};
}
