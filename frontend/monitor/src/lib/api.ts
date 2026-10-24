import type { Service } from './types';

// On the server, relative URLs cannot be resolved by the runtime fetch; talk to
// monitord directly. In the browser, use the same origin (monitord front door).
const API = typeof window === 'undefined'
	? (import.meta.env.MONITORD_URL ?? 'http://127.0.0.1:9050')
	: '';

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
