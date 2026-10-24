export type Alias = { kind: 'local' | 'tailnet' | 'public'; host: string; url: string };
export type Listen = { proto: string; addr: string; port: number };
export type Health = { state: 'up' | 'down' | 'unknown'; http?: number; ms: number };
export type Service = {
	pid: number;
	proc: string;
	user: string;
	cmd: string;
	cwd: string;
	unit?: string;
	container?: string;
	image?: string;
	listen: Listen[];
	aliases: Alias[];
	health?: Health;
};

export type AgentStatus = {
	ready: boolean;
	url: string;
	model: string;
};

export type LLMProviderSettings = {
	id: string;
	name?: string;
	api_key_set: boolean;
	api_key_hint?: string;
	source?: 'file' | 'env' | 'opencode';
	env_var?: string;
	base_url?: string;
	custom?: boolean;
};

export type LLMSettings = {
	path: string;
	model: string;
	model_source: 'default' | 'file' | 'env';
	providers: LLMProviderSettings[];
	env_overrides?: string[];
};

export type LLMSettingsResponse = {
	settings: LLMSettings;
	agent: AgentStatus;
	applied?: 'immediate' | 'restart';
};

export type LLMCatalogModel = {
	ref: string;
	name: string;
	provider_id: string;
	model_id: string;
	context: number;
	output: number;
	status: string;
	enabled: boolean;
	variants?: string[];
	input_cost?: number;
	output_cost?: number;
};

export type LLMCatalogProvider = {
	id: string;
	name: string;
	connected: boolean;
	api_key_set: boolean;
	api_key_hint?: string;
	key_source?: 'file' | 'env' | 'opencode';
	env_var?: string;
	base_url?: string;
	models: LLMCatalogModel[];
};

export type LLMCatalog = {
	providers: LLMCatalogProvider[];
	current: string;
	source: 'opencode' | 'unavailable';
	error?: string;
};

export type LLMProviderPatch = {
	apiKey?: string;
	baseURL?: string;
	name?: string;
	remove?: boolean;
};

export type LLMPatch = {
	model?: string;
	providers?: Record<string, LLMProviderPatch>;
};

export type AuditEntry = {
	time: string;
	session_id: string;
	tool: string;
	command?: string;
	status?: string;
	output?: string;
};
