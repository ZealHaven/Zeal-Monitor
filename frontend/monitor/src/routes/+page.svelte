<script lang="ts">
	import type { Service, Listen, Alias } from '../lib/types';
	import AliasChip from '../lib/components/AliasChip.svelte';
	import HealthDot from '../lib/components/HealthDot.svelte';
	import ChatDock from '../lib/components/ChatDock.svelte';
	import { triggerScan } from '../lib/api';

	let { data } = $props();
	let services = $state<Service[]>(data.services ?? []);
	let query = $state('');
	let scanning = $state(false);
	let expanded = $state<Set<number>>(new Set());
	let error = $state(data.error ?? '');
	let sortKey = $state<string | null>('ports');
	let sortDir = $state<'asc' | 'desc'>('asc');
	let showDown = $state(false);
	let showUnknown = $state(false);

	function setSort(key: string) {
		if (sortKey === key) sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		else {
			sortKey = key;
			sortDir = 'asc';
		}
	}

	const sorted = $derived.by(() => {
		const list = [...filtered];
		if (!sortKey) return list;
		const dir = sortDir === 'asc' ? 1 : -1;
		list.sort((a, b) => {
			if (sortKey === 'ports') {
				const ra = dnsRank(a);
				const rb = dnsRank(b);
				if (ra !== rb) return ra - rb;
				const pa = uniquePorts(a)[0] ?? 0;
				const pb = uniquePorts(b)[0] ?? 0;
				if (pa < pb) return -1 * dir;
				if (pa > pb) return 1 * dir;
				return 0;
			}
			let va: string | number = '';
			let vb: string | number = '';
			switch (sortKey) {
				case 'health':
					va = a.health?.state ?? '';
					vb = b.health?.state ?? '';
					break;
				case 'ports':
					va = uniquePorts(a)[0] ?? 0;
					vb = uniquePorts(b)[0] ?? 0;
					break;
				case 'process':
					va = a.proc.toLowerCase();
					vb = b.proc.toLowerCase();
					break;
				case 'cmd':
					va = a.cmd.toLowerCase();
					vb = b.cmd.toLowerCase();
					break;
				case 'path':
					va = a.cwd.toLowerCase();
					vb = b.cwd.toLowerCase();
					break;
				case 'aliases':
					va = a.aliases.length;
					vb = b.aliases.length;
					break;
				default:
					return 0;
			}
			if (va < vb) return -1 * dir;
			if (va > vb) return 1 * dir;
			return 0;
		});
		return list;
	});

	function toggle(pid: number) {
		const next = new Set(expanded);
		if (next.has(pid)) next.delete(pid);
		else next.add(pid);
		expanded = next;
	}

	const filtered = $derived(
		services.filter((s) => {
			const healthState = s.health?.state ?? 'unknown';
			if (healthState === 'down' && !showDown) return false;
			if (healthState === 'unknown' && !showUnknown) return false;
			const q = query.toLowerCase().trim();
			if (!q) return true;
			return (
				String(s.pid).includes(q) ||
				s.proc.toLowerCase().includes(q) ||
				s.cmd.toLowerCase().includes(q) ||
				s.cwd.toLowerCase().includes(q) ||
				s.listen.some((l: Listen) => String(l.port).includes(q)) ||
				s.aliases.some((a: Alias) => a.host.toLowerCase().includes(q) || a.url.toLowerCase().includes(q))
			);
		})
	);

	const totalPorts = $derived(services.reduce((n, s) => n + s.listen.length, 0));

	function uniquePorts(s: Service): number[] {
		return Array.from(new Set(s.listen.map((l) => l.port)));
	}

	function portLabel(s: Service): string {
		const ports = uniquePorts(s);
		if (ports.length <= 1) return ports.length === 0 ? '—' : String(ports[0]);
		return ports.length + ' ports';
	}

	const ALIAS_ORDER: Record<string, number> = { public: 0, local: 1, tailnet: 2 };
	function sortedAliases(s: Service): Alias[] {
		return [...s.aliases].sort((a, b) => (ALIAS_ORDER[a.kind] ?? 9) - (ALIAS_ORDER[b.kind] ?? 9));
	}
	function dnsRank(s: Service): number {
		const kinds = s.aliases.map((a) => a.kind);
		if (kinds.includes('public')) return 0;
		if (kinds.includes('local')) return 1;
		if (kinds.includes('tailnet')) return 2;
		return 3;
	}

	async function rescan() {
		scanning = true;
		try {
			services = await triggerScan();
			error = '';
		} catch (e) {
			error = String(e);
		} finally {
			scanning = false;
		}
	}

</script>

<svelte:head><title>Monitor</title></svelte:head>

<div class="layout">
	<div class="main">
		<header class="bar">
			<h1>Monitor</h1>
			<span class="meta">{services.length} processes · {totalPorts} listeners</span>
			<input class="search" placeholder="search port, process, path, host…" bind:value={query} />
			<label class="filter"><input type="checkbox" bind:checked={showDown} /> show down</label>
			<label class="filter"><input type="checkbox" bind:checked={showUnknown} /> show unknown</label>
			<button onclick={rescan} disabled={scanning}>{scanning ? 'scanning…' : 'rescan'}</button>
		</header>

		{#if error}
			<p class="error">{error}</p>
		{/if}

		<main>
			<table>
				<thead>
					<tr>
						<th class="sortable" onclick={() => setSort('health')}>health{sortKey === 'health' ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}</th>
						<th class="sortable" onclick={() => setSort('ports')}>port(s){sortKey === 'ports' ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}</th>
						<th class="sortable" onclick={() => setSort('aliases')}>aliases{sortKey === 'aliases' ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}</th>
						<th class="sortable" onclick={() => setSort('process')}>process{sortKey === 'process' ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}</th>
						<th class="sortable" onclick={() => setSort('cmd')}>cmd{sortKey === 'cmd' ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}</th>
						<th class="sortable" onclick={() => setSort('path')}>path (cwd){sortKey === 'path' ? (sortDir === 'asc' ? ' ▲' : ' ▼') : ''}</th>
					</tr>
				</thead>
				<tbody>
					{#each sorted as s (s.pid)}
						<tr class="row" onclick={() => toggle(s.pid)}>
							<td><HealthDot health={s.health} /></td>
							<td class="ports">{portLabel(s)}</td>
							<td class="aliases">
								{#each sortedAliases(s) as a (a.url)}
									<AliasChip alias={a} />
								{/each}
							</td>
							<td class="proc">{s.proc}<span class="pid">#{s.pid}</span></td>
							<td class="cmd" title={s.cmd}>{s.cmd}</td>
							<td class="cwd" title={s.cwd}>{s.cwd || '—'}</td>
						</tr>
						{#if expanded.has(s.pid)}
							<tr class="detail">
								<td colspan="6">
									<div class="detail-grid">
										<div><span>pid</span>{s.pid}</div>
										<div><span>proc</span>{s.proc}</div>
										<div><span>user</span>{s.user || '—'}</div>
										<div><span>unit</span>{s.unit || '—'}</div>
										<div><span>container</span>{s.container || '—'}{s.image ? ' (' + s.image + ')' : ''}</div>
										<div class="full"><span>cmd</span>{s.cmd}</div>
										<div class="full"><span>cwd</span>{s.cwd || '—'}</div>
										<div class="full"><span>health</span>{s.health ? s.health.state + ' · ' + s.health.ms + 'ms' + (s.health.http ? ' · HTTP ' + s.health.http : '') : 'not probed'}</div>
										<div class="full"><span>ports</span>{uniquePorts(s).join(', ') || '—'}</div>
										<div class="full"><span>aliases</span>{sortedAliases(s).map((a) => a.url).join('  ') || '—'}</div>
										<div class="full"><span>listeners</span>{s.listen.map((l) => l.proto + ' ' + l.addr + ':' + l.port).join('  ')}</div>
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
			{#if sorted.length === 0}
				<p class="empty">no services match “{query}”</p>
			{/if}
		</main>
	</div>

	<ChatDock />
</div>

<style>
	.layout {
		display: block;
		min-height: 100vh;
	}
	.main {
		min-width: 0;
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border);
		position: sticky;
		top: 0;
		background: var(--bg);
		z-index: 10;
		flex-wrap: wrap;
	}
	.bar h1 {
		margin: 0;
		font-size: 18px;
		font-weight: 600;
	}
	.bar .meta {
		color: var(--muted);
		font-size: 13px;
	}
	.search {
		flex: 1;
		max-width: 360px;
		background: var(--panel);
		color: var(--text);
		border: 1px solid var(--border-strong);
		border-radius: 6px;
		padding: 6px 10px;
		font: inherit;
		font-size: 14px;
	}
	button {
		background: var(--border-strong);
		color: var(--text);
		border: 1px solid var(--border-hover);
		border-radius: 6px;
		padding: 6px 12px;
		font: inherit;
		font-size: 14px;
		cursor: pointer;
	}
	button:disabled {
		opacity: .5;
		cursor: default;
	}
	main {
		padding: 16px;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 14px;
	}
	th,
	td {
		text-align: left;
		padding: 8px 10px;
		border-bottom: 1px solid var(--border);
		vertical-align: top;
	}
	th {
		color: var(--muted);
		font-weight: 500;
		font-size: 13px;
		text-transform: uppercase;
		letter-spacing: .04em;
		position: sticky;
		top: 56px;
		background: var(--bg);
		z-index: 5;
	}
	th.sortable {
		cursor: pointer;
		user-select: none;
	}
	th.sortable:hover {
		color: var(--text);
	}
	.ports {
		white-space: nowrap;
		font-weight: 600;
	}
	.proc {
		white-space: nowrap;
	}
	.pid {
		color: var(--muted);
		font-size: 13px;
		margin-left: 4px;
	}
	.cmd,
	.cwd {
		max-width: 260px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.aliases {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.unit {
		font-size: 13px;
		color: var(--muted);
	}
	tr {
		cursor: pointer;
	}
	tr:hover {
		background: var(--panel);
	}
	tr.detail td {
		background: var(--bg-elev);
		padding: 12px 16px;
	}
	.detail-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 6px 16px;
		font-size: 13px;
	}
	.detail-grid div {
		display: flex;
		gap: 8px;
	}
	.detail-grid span {
		color: var(--muted);
		min-width: 72px;
	}
	.detail-grid .full {
		grid-column: 1 / -1;
	}
	.detail-grid .full span {
		min-width: 72px;
	}
	.error {
		color: var(--red);
		padding: 8px 16px;
		font-size: 14px;
	}
	.empty {
		color: var(--muted);
		padding: 24px 0;
		font-size: 14px;
	}
	.filter {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		font-size: 13px;
		color: var(--muted);
		cursor: pointer;
		user-select: none;
	}
	.filter input {
		accent-color: var(--accent);
	}
</style>
