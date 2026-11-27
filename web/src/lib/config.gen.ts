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
	finalTopK: number;
	maxIterations: number;
	researchThinking: boolean;
	historyMessages: number;
	chunkWords: number;
	chunkOverlap: number;
	audioClipSeconds: number;
	audioClipOverlap: number;
	videoClipSeconds: number;
	videoClipOverlap: number;
	pdfPageImages: 'always' | 'fallback';
	pdfDPI: number;
	maxFileMB: number;
};

export const settingGroups: SettingGroup[] = [
	{
		"id": "servers",
		"label": "Model servers",
		"help": "Either libris runs llama.cpp here, or it connects to servers you started.",
		"fields": [
			{
				"key": "engineMode",
				"label": "Who runs them",
				"help": "Libris can download and run llama.cpp, or connect to servers you start yourself.",
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
				"help": "0 uses a free port each time. A number uses that port, and the server fails to start if something else holds it.",
				"kind": "number",
				"min": 1024,
				"max": 65535,
				"mode": "managed"
			},
			{
				"key": "engineDevice",
				"label": "Run models on",
				"help": "Which device runs the models. The list comes from the installed engine.",
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
				"help": "The address of your chat server.",
				"kind": "url",
				"mode": "external"
			},
			{
				"key": "embedURL",
				"label": "Embedding server",
				"help": "The address of your embedding server.",
				"kind": "url",
				"mode": "external"
			},
			{
				"key": "timeoutSeconds",
				"label": "Request timeout",
				"help": "How long to wait for a model server before a request fails.",
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
		"help": "The model that answers, and the one that finds. Changing the embedding model means re-embedding every source.",
		"fields": [
			{
				"key": "chatModel",
				"label": "Chat model",
				"help": "The name your chat server answers to. It must support tool calls.",
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
				"help": "Detected at startup. Cannot be set here.",
				"kind": "text",
				"readonly": true
			}
		]
	},
	{
		"id": "retrieval",
		"label": "Retrieval",
		"help": "What a search returns, and how files are cut up before they are searched.",
		"fields": [
			{
				"key": "finalTopK",
				"label": "Chunks per search",
				"help": "How many chunks one search returns.",
				"kind": "number",
				"unit": "chunks",
				"min": 1,
				"max": 50
			},
			{
				"key": "maxIterations",
				"label": "Max iterations",
				"help": "How many search rounds the researcher may make. Each one costs time.",
				"kind": "number",
				"min": 1,
				"max": 10
			},
			{
				"key": "researchThinking",
				"label": "Think before searching",
				"help": "Lets the model reason before it searches. Slower each round, sometimes a better search.",
				"kind": "bool"
			},
			{
				"key": "historyMessages",
				"label": "History window",
				"help": "How many earlier messages the answer sees.",
				"kind": "number",
				"unit": "messages",
				"min": 0,
				"max": 64
			},
			{
				"key": "chunkWords",
				"label": "Chunk size",
				"help": "Words per chunk of text.",
				"kind": "number",
				"unit": "words",
				"min": 10,
				"max": 1000
			},
			{
				"key": "chunkOverlap",
				"label": "Chunk overlap",
				"help": "Words repeated at the start of the next chunk.",
				"kind": "number",
				"unit": "words",
				"min": 0,
				"max": 999
			},
			{
				"key": "audioClipSeconds",
				"label": "Recording clip length",
				"help": "Seconds of a recording per clip. Shorter clips search better.",
				"kind": "number",
				"unit": "seconds",
				"min": 5,
				"max": 60
			},
			{
				"key": "audioClipOverlap",
				"label": "Recording clip overlap",
				"help": "Percent of each clip repeated at the start of the next.",
				"kind": "number",
				"unit": "%",
				"min": 0,
				"max": 95
			},
			{
				"key": "videoClipSeconds",
				"label": "Video clip length",
				"help": "Seconds of video per clip. 30 is the most the server can embed in one go.",
				"kind": "number",
				"unit": "seconds",
				"min": 5,
				"max": 30
			},
			{
				"key": "videoClipOverlap",
				"label": "Video clip overlap",
				"help": "Percent of each clip repeated at the start of the next.",
				"kind": "number",
				"unit": "%",
				"min": 0,
				"max": 95
			}
		]
	},
	{
		"id": "ingest",
		"label": "Ingest",
		"help": "How an uploaded file becomes searchable text and images.",
		"fields": [
			{
				"key": "pdfPageImages",
				"label": "PDF embeddings",
				"help": "Whether every page also gets an image, or only the pages with no text.",
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
				"help": "Resolution of the page images shown beside a citation.",
				"kind": "number",
				"unit": "dpi",
				"min": 50,
				"max": 600
			},
			{
				"key": "maxFileMB",
				"label": "Max file size",
				"help": "Files larger than this are not accepted.",
				"kind": "number",
				"unit": "MB",
				"min": 1,
				"max": 102400
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
		finalTopK: 10,
		maxIterations: 5,
		researchThinking: false,
		historyMessages: 8,
		chunkWords: 100,
		chunkOverlap: 50,
		audioClipSeconds: 15,
		audioClipOverlap: 50,
		videoClipSeconds: 15,
		videoClipOverlap: 50,
		pdfPageImages: "always",
		pdfDPI: 110,
		maxFileMB: 512,
	};
}
