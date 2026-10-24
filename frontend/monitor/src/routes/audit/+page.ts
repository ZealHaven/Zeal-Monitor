import type { PageLoad } from './$types';
import { fetchAudit } from '../../lib/api';

export const load: PageLoad = async ({ fetch }) => {
	try {
		const entries = await fetchAudit(200, fetch);
		return { entries };
	} catch (e) {
		return { entries: [], error: String(e) };
	}
};
