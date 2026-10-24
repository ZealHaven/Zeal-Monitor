import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	server: {
		// monitord (:9050) is the front door; it proxies here in development.
		// 9065 (not Vite's default 5173) — that port is used by another app on this box.
		port: 9065,
		strictPort: true,
		host: '127.0.0.1',
		// Allow the clean hostnames that nginx/monitord use in front of Vite,
		// so HMR and SSR work when accessed via http://monitor.local etc.
		allowedHosts: ['localhost', '127.0.0.1']
	},
	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},

			// Deployed behind monitord (Go front door) as a systemd user service.
			adapter: adapter({
				out: 'build',
				precompress: true
			})
		})
	]
});
