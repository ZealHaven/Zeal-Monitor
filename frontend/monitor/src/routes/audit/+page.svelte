<script lang="ts">
	import type { PageData } from './$types';
	let { data }: { data: PageData } = $props();
</script>

<svelte:head><title>Audit — Monitor</title></svelte:head>

<main class="audit">
	<h1>Chat audit log</h1>
	<p class="meta">{data.entries.length} entries (newest last)</p>
	{#if data.error}
		<p class="error">{data.error}</p>
	{/if}
	<table>
		<thead>
			<tr>
				<th>time</th>
				<th>session</th>
				<th>tool</th>
				<th>command</th>
				<th>status</th>
				<th>output</th>
			</tr>
		</thead>
		<tbody>
			{#each data.entries as e (e.time + e.command)}
				<tr>
					<td class="t">{new Date(e.time).toLocaleString()}</td>
					<td class="s">{e.session_id}</td>
					<td>{e.tool}</td>
					<td class="cmd">{e.command}</td>
					<td class="st">{e.status}</td>
					<td class="out">{(e.output ?? '').slice(0, 200)}</td>
				</tr>
			{/each}
		</tbody>
	</table>
	{#if data.entries.length === 0}
		<p class="empty">no audit entries yet</p>
	{/if}
</main>

<style>
	.audit {
		padding: 24px;
		max-width: 1200px;
		margin: 0 auto;
		color: var(--text);
	}
	h1 {
		margin: 0 0 4px;
		font-size: 18px;
	}
	.meta {
		color: var(--muted);
		font-size: 13px;
		margin: 0 0 16px;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 14px;
	}
	th,
	td {
		text-align: left;
		padding: 6px 8px;
		border-bottom: 1px solid var(--border);
		vertical-align: top;
	}
	th {
		color: var(--muted);
		font-size: 13px;
		text-transform: uppercase;
	}
	.t {
		white-space: nowrap;
		color: var(--muted);
	}
	.s {
		font-size: 13px;
		color: var(--muted-2);
	}
	.cmd {
		font-family: monospace;
		max-width: 320px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.out {
		font-family: monospace;
		max-width: 320px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--teal);
	}
	.st {
		color: var(--muted);
	}
	.empty {
		color: var(--muted);
		padding: 24px 0;
		font-size: 14px;
	}
	.error {
		color: var(--red);
		font-size: 14px;
	}
</style>
