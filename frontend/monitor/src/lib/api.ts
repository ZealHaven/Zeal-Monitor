import type { AuditEntry, LLMCatalog, LLMPatch, LLMSettingsResponse, Service } from './types';

// On the server, relative URLs cannot be resolved by the runtime fetch; talk to
// monitord directly. MONITORD_URL is read at runtime so deployments can move the
// backend without rebuilding. In the browser, use the same origin (monitord
// front door).
const API =
	typeof window === 'undefined'
		? ((globalThis as any).process?.env?.MONITORD_URL ??
			import.meta.env.MONITORD_URL ??
			'http://127.0.0.1:9050')
		: '';

export class APIError extends Error {
	status: number;
	code: string;

	constructor(status: number, code: string, message: string) {
		super(message);
		this.name = 'APIError';
		this.status = status;
		this.code = code;
	}
}

async function request<T>(
	path: string,
	init?: RequestInit,
	fetchFn: typeof fetch = fetch
): Promise<T> {
	const r = await fetchFn(`${API}${path}`, init);
	const text = await r.text();
	let body: any = null;
	if (text) {
		try {
			body = JSON.parse(text);
		} catch {
			body = null;
		}
	}
	if (!r.ok) {
		throw new APIError(
			r.status,
			body?.error ?? `http_${r.status}`,
			body?.message ?? `request failed: ${r.status}`
		);
	}
	return body as T;
}

export async function fetchServices(fetchFn: typeof fetch = fetch): Promise<Service[]> {
	const r = await fetchFn(`${API}/api/services`);
	if (!r.ok) throw new Error(`services: ${r.status}`);
	const j = await r.json();
	return j.services ?? [];
}

export async function triggerScan(fetchFn: typeof fetch = fetch): Promise<Service[]> {
	const r = await fetchFn(`${API}/api/scan`, { method: 'POST' });
	if (!r.ok) throw new Error(`scan: ${r.status}`);
	const j = await r.json();
	return j.services ?? [];
}

export async function fetchLLMSettings(
	fetchFn: typeof fetch = fetch
): Promise<LLMSettingsResponse> {
	return request<LLMSettingsResponse>('/api/settings/llm', undefined, fetchFn);
}

export async function saveLLMSettings(
	patch: LLMPatch,
	fetchFn: typeof fetch = fetch
): Promise<LLMSettingsResponse> {
	return request<LLMSettingsResponse>(
		'/api/settings/llm',
		{
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(patch)
		},
		fetchFn
	);
}

export async function fetchLLMModels(fetchFn: typeof fetch = fetch): Promise<LLMCatalog> {
	return request<LLMCatalog>('/api/llm/models', undefined, fetchFn);
}

export async function reloadAgent(fetchFn: typeof fetch = fetch): Promise<{ agent: LLMSettingsResponse['agent'] }> {
	return request('/api/settings/llm/reload', { method: 'POST' }, fetchFn);
}

export async function fetchAudit(
	limit = 200,
	fetchFn: typeof fetch = fetch
): Promise<AuditEntry[]> {
	const j = await request<{ entries?: AuditEntry[] }>(`/api/audit?limit=${limit}`, undefined, fetchFn);
	return j.entries ?? [];
}
