<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';

	let { children } = $props();
	let theme = $state('dark');

	onMount(() => {
		const stored = localStorage.getItem('monitor.theme');
		if (stored === 'light' || stored === 'dark') {
			theme = stored;
		} else if (window.matchMedia('(prefers-color-scheme: light)').matches) {
			theme = 'light';
		}
	});

	$effect(() => {
		if (typeof document !== 'undefined') {
			document.documentElement.dataset.theme = theme;
			try {
				localStorage.setItem('monitor.theme', theme);
			} catch {
				/* ignore */
			}
		}
	});

	function toggleTheme() {
		theme = theme === 'dark' ? 'light' : 'dark';
	}
</script>

<nav class="topnav">
	<a href="/">Monitor</a>
	<a href="/audit">Audit</a>
	<span class="spacer"></span>
	<button class="theme-toggle" onclick={toggleTheme} title="Toggle light/dark theme" aria-label="Toggle theme">
		{theme === 'dark' ? '☀ Light' : '🌙 Dark'}
	</button>
</nav>

{@render children()}

<style>
	.topnav {
		display: flex;
		align-items: center;
		gap: 16px;
		padding: 8px 16px;
		border-bottom: 1px solid var(--border);
		font-size: 14px;
		background: var(--bg);
	}
	.topnav a {
		color: var(--muted);
		text-decoration: none;
	}
	.topnav a:hover {
		color: var(--text);
	}
	.spacer {
		flex: 1;
	}
	.theme-toggle {
		background: transparent;
		border: 1px solid var(--border-strong);
		color: var(--text);
		border-radius: 6px;
		padding: 4px 10px;
		font-size: 13px;
		cursor: pointer;
	}
	.theme-toggle:hover {
		background: var(--panel);
	}
</style>
