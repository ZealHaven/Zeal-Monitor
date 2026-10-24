import type { PageLoad } from './$types';
import { fetchServices } from '../lib/api';

export const load: PageLoad = async ({ fetch }) => {
	try {
		const services = await fetchServices(fetch);
		return { services };
	} catch (e) {
		return { services: [], error: String(e) };
	}
};
