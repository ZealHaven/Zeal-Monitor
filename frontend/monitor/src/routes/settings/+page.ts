import type { PageLoad } from './$types';
import { fetchLLMSettings } from '../../lib/api';

export const load: PageLoad = async ({ fetch }) => {
	try {
		return await fetchLLMSettings(fetch);
	} catch (e) {
		return {
			settings: null,
			agent: { ready: false, url: '', model: '' },
			error: String(e)
		};
	}
};
