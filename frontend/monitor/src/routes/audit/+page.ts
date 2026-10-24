import type { PageLoad } from './$types';

export const load: PageLoad = async () => {
	try {
		const r = await fetch('http://127.0.0.1:9050/api/audit?limit=200');
		const j = await r.json();
		return { entries: j.entries ?? [] };
	} catch (e) {
		return { entries: [], error: String(e) };
	}
};
