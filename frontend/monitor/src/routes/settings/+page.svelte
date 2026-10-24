<script lang="ts">
	import { onMount } from 'svelte';
	import {
		APIError,
		fetchLLMModels,
		reloadAgent,
		saveLLMSettings
	} from '../../lib/api';
	import type {
		AgentStatus,
		LLMCatalog,
		LLMCatalogProvider,
		LLMPatch,
		LLMSettings
	} from '../../lib/types';

	type Option = { ref: string; name: string; variants?: string[] };

	let { data } = $props();

	let settings = $state<LLMSettings | null>(data.settings ?? null);
	let agent = $state<AgentStatus>(data.agent ?? { ready: false, url: '', model: '' });
	let loadError = $state('error' in data ? String(data.error) : '');
	let catalog = $state<LLMCatalog>({
		providers: [],
		current: settings?.model ?? '',
		source: 'unavailable'
	});
	let catalogLoading = $state(true);
	let catalogError = $state('');
	let providerFilter = $state('');

	let modelDraft = $state(settings?.model ?? '');
	let modelProviderID = $state(providerOf(settings?.model ?? ''));
	let credProviderID = $state(providerOf(settings?.model ?? ''));
	let baseURLDraft = $state(
		settings?.providers.find((p) => p.id === credProviderID)?.base_url ?? ''
	);
	let keyDraft = $state('');
	let clearKey = $state(false);
	let manual = $state(false);
	let variant = $state(variantOf(settings?.model ?? ''));

	let saving = $state(false);
	let reloading = $state(false);
	let notice = $state('');
	let error = $state('');

	function providerOf(ref: string): string {
		const i = ref.indexOf('/');
		return i > 0 ? ref.slice(0, i) : ref;
	}

	function variantOf(ref: string): string {
		const i = ref.indexOf('#');
		return i >= 0 ? ref.slice(i + 1) : '';
	}

	function baseOf(ref: string): string {
		const i = ref.indexOf('#');
		return i >= 0 ? ref.slice(0, i) : ref;
	}

	const rows = $derived.by(() => {
		const map = new Map<string, LLMCatalogProvider>();
		for (const p of catalog.providers ?? []) {
			map.set(p.id, { ...p, models: p.models ?? [] });
		}
		for (const p of settings?.providers ?? []) {
			const existing = map.get(p.id);
			if (existing) {
				if (p.api_key_set) {
					existing.api_key_set = true;
					existing.api_key_hint = p.api_key_hint;
					existing.key_source = p.source;
					existing.env_var = p.env_var;
				}
				if (p.name) existing.name = p.name;
				if (p.base_url) existing.base_url = p.base_url;
			} else {
				map.set(p.id, {
					id: p.id,
					name: p.name || p.id,
					connected: true,
					api_key_set: p.api_key_set,
					api_key_hint: p.api_key_hint,
					key_source: p.source,
					env_var: p.env_var,
					base_url: p.base_url,
					models: []
				});
			}
		}
		for (const prefix of [providerOf(modelDraft), credProviderID]) {
			if (prefix && !map.has(prefix)) {
				map.set(prefix, {
					id: prefix,
					name: `${prefix} (not in catalog)`,
					connected: false,
					api_key_set: false,
					models: []
				});
			}
		}
		return [...map.values()];
	});

	const credProvider = $derived(rows.find((p) => p.id === credProviderID));
	const modelProvider = $derived(rows.find((p) => p.id === modelProviderID));
	const modelRows = $derived(
		rows.filter((p) => p.connected || p.models.length > 0 || p.id === modelProviderID)
	);
	const credRows = $derived.by(() => {
		const q = providerFilter.trim().toLowerCase();
		let list = q
			? rows.filter(
					(p) => p.id.toLowerCase().includes(q) || p.name.toLowerCase().includes(q)
				)
			: rows;
		if (!list.some((p) => p.id === credProviderID)) {
			const current = rows.find((p) => p.id === credProviderID);
			if (current) list = [current, ...list];
		}
		return list;
	});
	const savedBase = $derived(credProvider?.base_url ?? '');
	const modelList = $derived(modelProvider?.models ?? []);
	const baseRef = $derived(baseOf(modelDraft));
	const selectedModel = $derived(modelList.find((m) => m.ref === baseRef));

	const modelOptions = $derived.by(() => {
		const list: Option[] = modelList.map((m) => ({
			ref: m.ref,
			name: m.name,
			variants: m.variants
		}));
		if (baseRef && !list.some((o) => o.ref === baseRef)) {
			list.unshift({ ref: baseRef, name: `${baseRef} (not in catalog)` });
		}
		return list;
	});

	const variants = $derived.by(() => {
		if (selectedModel?.variants?.length) return selectedModel.variants;
		const opt = modelOptions.find((o) => o.ref === baseRef);
		return opt?.variants ?? [];
	});

	const modelLocked = $derived(settings?.model_source === 'env');
	const credentialsDirty = $derived(
		keyDraft.trim() !== '' || clearKey || baseURLDraft.trim() !== savedBase
	);
	const dirty = $derived(
		!!settings && (modelDraft.trim() !== settings.model || credentialsDirty)
	);

	function resetCreds(id: string) {
		const row = rows.find((r) => r.id === id);
		baseURLDraft = row?.base_url ?? '';
		keyDraft = '';
		clearKey = false;
	}

	function chooseModelProvider(id: string) {
		modelProviderID = id;
		const list = rows.find((r) => r.id === id)?.models ?? [];
		if (list.length && !list.some((m) => m.ref === baseRef)) {
			chooseModel(list[0].ref);
		}
	}

	function chooseCredProvider(id: string) {
		credProviderID = id;
		resetCreds(id);
	}

	function chooseModel(ref: string) {
		const next = rows.find((r) => r.id === modelProviderID)?.models.find((m) => m.ref === ref);
		const v = variant && next?.variants?.includes(variant) ? variant : '';
		modelDraft = ref + (v ? `#${v}` : '');
		variant = v;
	}

	function chooseVariant(v: string) {
		variant = v;
		modelDraft = baseRef + (v ? `#${v}` : '');
	}

	async function loadCatalog() {
		catalogLoading = true;
		catalogError = '';
		try {
			catalog = await fetchLLMModels();
			if (catalog.error) catalogError = catalog.error;
			if (!rows.some((r) => r.id === modelProviderID)) {
				const first = rows[0];
				if (first) modelProviderID = first.id;
			}
			if (!rows.some((r) => r.id === credProviderID)) {
				const first = rows[0];
				if (first) {
					credProviderID = first.id;
					resetCreds(first.id);
				}
			}
			if (!credentialsDirty) {
				baseURLDraft = rows.find((r) => r.id === credProviderID)?.base_url ?? '';
			}
		} catch (e) {
			catalogError = e instanceof Error ? e.message : String(e);
		} finally {
			catalogLoading = false;
		}
	}

	onMount(loadCatalog);

	async function save() {
		if (!settings || saving) return;
		const patch: LLMPatch = {};
		const nextModel = modelDraft.trim();
		if (!nextModel) {
			error = 'Choose a model or enter a model reference.';
			return;
		}
		if (nextModel !== settings.model) patch.model = nextModel;

		const creds: { apiKey?: string; baseURL?: string } = {};
		if (clearKey) creds.apiKey = '';
		else if (keyDraft.trim()) creds.apiKey = keyDraft.trim();
		if (baseURLDraft.trim() !== savedBase) creds.baseURL = baseURLDraft.trim();
		if (Object.keys(creds).length) patch.providers = { [credProviderID]: creds };

		if (!patch.model && !patch.providers) {
			notice = 'Nothing to save.';
			return;
		}

		saving = true;
		error = '';
		notice = '';
		try {
			const res = await saveLLMSettings(patch);
			settings = res.settings;
			agent = res.agent;
			keyDraft = '';
			clearKey = false;
			baseURLDraft =
				res.settings.providers.find((p) => p.id === credProviderID)?.base_url ?? '';
			notice =
				res.applied === 'restart'
					? 'Saved. The agent restarted with the new provider configuration.'
					: 'Saved. The model applies to your next message.';
			await loadCatalog();
			window.dispatchEvent(new CustomEvent('monitor:llm-changed'));
		} catch (e) {
			if (e instanceof APIError && e.code === 'chat_in_progress') {
				error = 'A chat is running. Finish or cancel it, then save again.';
			} else if (e instanceof APIError) {
				error = e.message;
			} else {
				error = e instanceof Error ? e.message : String(e);
			}
		} finally {
			saving = false;
		}
	}

	async function reload() {
		if (reloading) return;
		reloading = true;
		error = '';
		notice = '';
		try {
			const res = await reloadAgent();
			agent = res.agent;
			notice = 'Agent reloaded.';
			await loadCatalog();
		} catch (e) {
			error =
				e instanceof APIError && e.code === 'chat_in_progress'
					? 'A chat is running. Finish or cancel it, then reload.'
					: e instanceof Error
						? e.message
						: String(e);
		} finally {
			reloading = false;
		}
	}

	function removeProvider() {
		if (!settings || !credProvider) return;
		if (!confirm(`Remove saved credentials for ${credProvider.name}?`)) return;
		const patch: LLMPatch = { providers: { [credProviderID]: { remove: true } } };
		saving = true;
		error = '';
		notice = '';
		saveLLMSettings(patch)
			.then(async (res) => {
				settings = res.settings;
				agent = res.agent;
				keyDraft = '';
				clearKey = false;
				baseURLDraft = '';
				notice = 'Provider configuration removed.';
				await loadCatalog();
			})
			.catch((e) => {
				error = e instanceof APIError ? e.message : String(e);
			})
			.finally(() => {
				saving = false;
			});
	}

	function fmtContext(n: number): string {
		if (!n) return '—';
		if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n % 1_000_000 ? 1 : 0)}M`;
		if (n >= 1000) return `${Math.round(n / 1000)}k`;
		return String(n);
	}

	function fmtCost(n?: number): string {
		if (n === undefined) return '';
		if (n === 0) return 'free';
		return `$${n}/Mtok`;
	}
</script>

<svelte:head>
	<title>Settings · Monitor</title>
</svelte:head>

<main class="settings">
	<header class="page-head">
		<h1>Settings</h1>
		<p>Configure the model and provider used by the chat agent.</p>
	</header>

	{#if loadError}
		<div class="banner error">{loadError}</div>
	{/if}
	{#if error}
		<div class="banner error">{error}</div>
	{/if}
	{#if notice}
		<div class="banner ok">{notice}</div>
	{/if}

	<section class="card">
		<div class="card-head">
			<h2>Agent</h2>
			<button class="ghost" onclick={reload} disabled={reloading || saving}>
				{reloading ? 'Restarting…' : 'Restart agent'}
			</button>
		</div>
		<div class="status-row">
			<span class="dot" class:up={agent.ready} class:down={!agent.ready}></span>
			<span>{agent.ready ? 'ready' : 'starting'}</span>
			<span class="sep">·</span>
			<code>{agent.url || '—'}</code>
			<span class="sep">·</span>
			<span class="model-inline">{agent.model || '—'}</span>
		</div>
		<p class="hint">
			Credentials are stored in <code>{settings?.path ?? '—'}</code> (mode 0600). Keys are
			write-only and never displayed in full.
		</p>
	</section>

	{#if settings}
		<section class="card">
			<h2>Model</h2>
			{#if modelLocked}
				<p class="banner info">
					<code>MONITOR_MODEL</code> is set in the environment and overrides this file.
				</p>
			{/if}

			<div class="field-grid">
				<label>
					<span>Provider</span>
					<select
						value={modelProviderID}
						onchange={(e) => chooseModelProvider((e.currentTarget as HTMLSelectElement).value)}
						disabled={modelLocked || catalogLoading}
					>
						{#each modelRows as p (p.id)}
							<option value={p.id}>{p.name}{p.connected ? '' : ' (not connected)'}</option>
						{/each}
					</select>
				</label>

				<label>
					<span>Model</span>
					{#if manual}
						<input
							type="text"
							bind:value={modelDraft}
							oninput={(e) => (modelProviderID = providerOf((e.currentTarget as HTMLInputElement).value))}
							placeholder="provider/model or provider/model#variant"
							disabled={modelLocked}
						/>
					{:else}
						<select
							value={baseRef}
							onchange={(e) => chooseModel((e.currentTarget as HTMLSelectElement).value)}
							disabled={modelLocked || catalogLoading || !modelOptions.length}
						>
							{#each modelOptions as m (m.ref)}
								<option value={m.ref}>{m.name}</option>
							{/each}
						</select>
					{/if}
				</label>

				{#if variants.length}
					<label>
						<span>Variant</span>
						<select
							value={variant}
							onchange={(e) => chooseVariant((e.currentTarget as HTMLSelectElement).value)}
							disabled={modelLocked}
						>
							<option value="">default</option>
							{#each variants as v (v)}
								<option value={v}>{v}</option>
							{/each}
						</select>
					</label>
				{/if}
			</div>

			<div class="model-meta">
				{#if selectedModel}
					<span>{fmtContext(selectedModel.context)} context</span>
					<span class="sep">·</span>
					<span>{fmtContext(selectedModel.output)} output</span>
					{#if fmtCost(selectedModel.input_cost)}
						<span class="sep">·</span>
						<span>in {fmtCost(selectedModel.input_cost)}</span>
					{/if}
					{#if fmtCost(selectedModel.output_cost)}
						<span class="sep">·</span>
						<span>out {fmtCost(selectedModel.output_cost)}</span>
					{/if}
					<span class="sep">·</span>
					<span class:disabled={!selectedModel.enabled}>{selectedModel.status}</span>
				{:else}
					<span class="muted">
						{catalogLoading ? 'Loading model catalog…' : 'No catalog entry for this model.'}
					</span>
				{/if}
			</div>

			<label class="check">
				<input type="checkbox" bind:checked={manual} />
				<span>Enter model reference manually</span>
			</label>

			{#if catalogError}
				<p class="hint warn">Model discovery: {catalogError}</p>
			{/if}
			{#if !catalogLoading && catalog.source === 'opencode' && !modelList.length}
				<p class="hint">
					No models discovered for {modelProvider?.name ?? modelProviderID}. Add a key below,
					then the agent will restart and refresh this list.
				</p>
			{/if}
		</section>

		<section class="card">
			<h2>Provider credentials</h2>
			<div class="field-grid">
				<label>
					<span>Provider</span>
					<input
						type="search"
						bind:value={providerFilter}
						placeholder="Search providers…"
						spellcheck="false"
					/>
					<select
						value={credProviderID}
						onchange={(e) => chooseCredProvider((e.currentTarget as HTMLSelectElement).value)}
						disabled={catalogLoading}
					>
						{#each credRows as p (p.id)}
							<option value={p.id}>{p.name}{p.connected ? '' : ' (not connected)'}</option>
						{/each}
					</select>
				</label>
			</div>

			<div class="cred-status">
				{#if credProvider?.api_key_set}
					<span class="badge ok">
						configured{#if credProvider.api_key_hint} {credProvider.api_key_hint}{/if}
					</span>
					{#if credProvider.key_source === 'env'}
						<span class="badge">env {credProvider.env_var}</span>
					{:else if credProvider.key_source === 'opencode'}
						<span class="badge">OpenCode auth</span>
					{:else}
						<span class="badge">file</span>
					{/if}
				{:else if credProvider?.connected}
					<span class="badge ok">provider connected</span>
				{:else}
					<span class="badge warn">not connected · no key configured</span>
				{/if}
			</div>

			<div class="field-grid">
				<label>
					<span>API key {#if credProvider?.api_key_set}<em>· leave blank to keep</em>{/if}</span>
					<input
						type="password"
						bind:value={keyDraft}
						autocomplete="off"
						spellcheck="false"
						placeholder={credProvider?.api_key_set ? '••••••••' : 'paste provider API key'}
						oninput={() => (clearKey = false)}
					/>
				</label>
				<label>
					<span>Base URL <em>· optional</em></span>
					<input
						type="url"
						bind:value={baseURLDraft}
						placeholder="https://gateway.example/v1"
						spellcheck="false"
					/>
				</label>
			</div>

			<div class="actions">
				{#if credProvider?.api_key_set && credProvider.key_source !== 'env' && credProvider.key_source !== 'opencode'}
					<button class="danger ghost" onclick={() => (clearKey = !clearKey)}>
						{clearKey ? 'Undo clear' : 'Clear saved key'}
					</button>
					<button class="danger ghost" onclick={removeProvider} disabled={saving}>
						Remove provider
					</button>
				{/if}
				<span class="spacer"></span>
				<button class="primary" onclick={save} disabled={saving || !dirty}>
					{saving ? 'Saving…' : 'Save'}
				</button>
			</div>
			{#if credProvider?.key_source === 'opencode'}
				<p class="hint">
					Authenticated through OpenCode. A key entered here overrides it for Monitor only.
				</p>
			{/if}
			{#if clearKey}
				<p class="hint warn">The saved key will be removed when you save.</p>
			{/if}
			<p class="hint">
				Changing credentials restarts the agent (about a second). Model changes apply to the
				next message without a restart.
			</p>
		</section>
	{:else}
		<section class="card">
			<p class="muted">Settings could not be loaded.</p>
		</section>
	{/if}
</main>

<style>
	.settings {
		max-width: 860px;
		margin: 0 auto;
		padding: 20px 16px 80px;
	}
	.page-head h1 {
		margin: 0 0 4px;
		font-size: 20px;
	}
	.page-head p {
		margin: 0 0 16px;
		color: var(--muted);
		font-size: 13px;
	}
	.card {
		background: var(--bg-elev);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		margin-bottom: 14px;
	}
	.card h2 {
		margin: 0 0 12px;
		font-size: 14px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--muted);
	}
	.card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 10px;
	}
	.card-head h2 {
		margin: 0;
	}
	.status-row {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 13px;
		flex-wrap: wrap;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--muted-2);
	}
	.dot.up {
		background: var(--green);
	}
	.dot.down {
		background: var(--amber);
	}
	.sep {
		color: var(--muted-2);
	}
	.model-inline {
		color: var(--text);
	}
	.field-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 12px;
		margin-bottom: 10px;
	}
	label {
		display: flex;
		flex-direction: column;
		gap: 5px;
		font-size: 13px;
		color: var(--muted);
	}
	label span {
		color: var(--muted);
	}
	label em {
		font-style: normal;
		color: var(--muted-2);
	}
	input,
	select {
		background: var(--panel);
		color: var(--text);
		border: 1px solid var(--border-strong);
		border-radius: 6px;
		padding: 7px 9px;
		font: inherit;
		font-size: 13px;
		min-width: 0;
	}
	input:focus,
	select:focus {
		outline: 1px solid var(--accent);
		border-color: var(--accent);
	}
	input:disabled,
	select:disabled {
		opacity: 0.55;
	}
	.model-meta {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		font-size: 12px;
		color: var(--muted);
		min-height: 16px;
	}
	.disabled {
		color: var(--amber);
	}
	.check {
		flex-direction: row;
		align-items: center;
		gap: 8px;
		margin-top: 10px;
		cursor: pointer;
	}
	.check input {
		margin: 0;
	}
	.cred-status {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-bottom: 12px;
	}
	.badge {
		border: 1px solid var(--border-strong);
		border-radius: 10px;
		padding: 2px 8px;
		font-size: 12px;
		color: var(--muted);
	}
	.badge.ok {
		color: var(--teal);
		border-color: #2a4a45;
		background: #142624;
	}
	.badge.warn {
		color: var(--amber);
		border-color: #4a3a1a;
		background: #2a2010;
	}
	.actions {
		display: flex;
		gap: 8px;
		align-items: center;
		margin-top: 12px;
		flex-wrap: wrap;
	}
	.spacer {
		flex: 1;
	}
	button.primary {
		background: var(--accent);
		color: var(--bg);
		border-color: var(--accent);
		font-weight: 600;
	}
	button.ghost {
		background: transparent;
		color: var(--text);
		border: 1px solid var(--border-strong);
	}
	button.danger {
		color: var(--red);
		border-color: #4a2020;
	}
	.hint {
		margin: 10px 0 0;
		font-size: 12px;
		color: var(--muted);
	}
	.hint code {
		font-size: 11.5px;
	}
	.hint.warn {
		color: var(--amber);
	}
	.muted {
		color: var(--muted);
	}
	.banner {
		border-radius: 8px;
		padding: 9px 12px;
		font-size: 13px;
		margin-bottom: 12px;
		border: 1px solid var(--border-strong);
	}
	.banner.error {
		color: var(--red);
		border-color: #4a2020;
		background: #2a1212;
	}
	.banner.ok {
		color: var(--teal);
		border-color: #2a4a45;
		background: #142624;
	}
	.banner.info {
		color: var(--amber);
		border-color: #4a3a1a;
		background: #2a2010;
	}
	:global(html[data-theme='light']) .badge.ok,
	:global(html[data-theme='light']) .banner.ok {
		color: #0d9488;
		background: #e6f4f2;
		border-color: #99d6cf;
	}
	:global(html[data-theme='light']) .badge.warn,
	:global(html[data-theme='light']) .banner.info {
		color: #b45309;
		background: #faf0e0;
		border-color: #f0d3a8;
	}
	:global(html[data-theme='light']) .banner.error {
		color: #dc2626;
		background: #fdeaea;
		border-color: #f3b7b7;
	}
	:global(html[data-theme='light']) .danger {
		border-color: #f3b7b7;
	}
	@media (max-width: 560px) {
		.field-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
