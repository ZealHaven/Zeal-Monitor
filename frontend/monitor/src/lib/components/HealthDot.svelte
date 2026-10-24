<script lang="ts">
	import type { Health } from '../types';
	let { health }: { health?: Health } = $props();
	const state = $derived(health?.state ?? 'unknown');
	const tip = $derived(
		health
			? `${health.state} · ${health.ms}ms${health.http ? ' · HTTP ' + health.http : ''}`
			: 'not probed'
	);
</script>

<span class="dot dot-{state}" title={tip}></span>
