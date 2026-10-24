<script lang="ts">
	import { onMount } from 'svelte';
	import type { ChatEvent, ChatMessage, ChatTool } from '../chat';
	import { sendChat } from '../chat';
	import { marked } from 'marked';
	marked.setOptions({ gfm: true, breaks: true });
	// Prevent XSS from model/tool output: render raw HTML escaped.
	marked.use({
		renderer: {
			html(html: any) {
				return html.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
			}
		}
	});

	let { collapsed = $bindable(true) }: { collapsed?: boolean } = $props();

	let messages = $state<ChatMessage[]>([]);
	let input = $state('');
	let sending = $state(false);
	let status = $state('');
	let sessionId = $state('');

	const quickActions = [
		{ label: 'Restart nginx', prompt: 'Restart nginx and confirm it is healthy.' },
		{ label: 'What is on :3000?', prompt: 'What process and service is listening on port 3000?' },
		{ label: 'Free port 8765', prompt: 'Find what is using port 8765 and stop it, then rescan.' },
		{ label: 'Switch port 3000→3001', prompt: 'Switch the service on port 3000 to port 3001 (update its nginx vhost and restart), then rescan.' },
		{ label: 'Refresh inventory', prompt: 'Rescan the inventory and summarize what changed.' }
	];

	onMount(() => {
		const stored = localStorage.getItem('monitor.sessionId');
		sessionId = stored || 'ses_' + Math.random().toString(36).slice(2, 10);
		localStorage.setItem('monitor.sessionId', sessionId);
	});

	async function send(text?: string) {
		const msg = (text ?? input).trim();
		if (!msg || sending) return;
		input = '';
		messages = [...messages, { role: 'user', text: msg, tools: [], streaming: false }];
		let assistant = $state<ChatMessage>({ role: 'assistant', text: '', tools: [], streaming: true });
		messages = [...messages, assistant];
		status = 'Thinking...';
		sending = true;
		try {
			await sendChat(sessionId, msg, (ev: ChatEvent) => {
				if (ev.type === 'session' && ev.session_id) sessionId = ev.session_id;
				if (ev.type === 'step_start') status = 'Thinking...';
				if (ev.type === 'tool') {
					const name = (ev.tool || '').toLowerCase();
					const cmd = typeof ev.tool_input?.command === 'string' ? ev.tool_input.command.toLowerCase() : '';
					const searching = /search|web|grep|find|ss |lsof|ps /.test(name + ' ' + cmd) || name === 'shell' || name === 'bash';
					status = searching ? 'Searching...' : 'Thinking...';
				}
				if (ev.type === 'text' && ev.text) status = '';
				if (ev.type === 'text' && ev.text) {
					assistant.text += ev.text;
					messages = [...messages];
				}
				if (ev.type === 'tool') {
					const t: ChatTool = {
						name: ev.tool,
						input: ev.tool_input ?? {},
						output: ev.tool_output ?? '',
						status: ev.tool_status ?? 'completed'
					};
					assistant.tools = [...assistant.tools, t];
					messages = [...messages];
				}
				if (ev.type === 'error' && ev.error) {
					assistant.text += `\n[error] ${ev.error}`;
					messages = [...messages];
				}
			});
		} catch (e) {
			assistant.text += `\n[error] ${String(e)}`;
		} finally {
			assistant.streaming = false;
			status = '';
			messages = [...messages];
			sending = false;
		}
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			send();
		}
	}

	function newThread() {
		sessionId = 'ses_' + Math.random().toString(36).slice(2, 10);
		localStorage.setItem('monitor.sessionId', sessionId);
		messages = [];
		status = '';
	}
</script>

<aside class="dock" class:collapsed>
	{#if collapsed}
		<button class="expand-btn" onclick={() => (collapsed = false)} title="Open chat" aria-label="Open chat">💬</button>
	{:else}
		<div class="dock-header">
			<span class="title">Chat</span>
			<div class="actions">
				<button class="ghost" onclick={newThread} title="New thread">new</button>
				<button class="ghost" onclick={() => (collapsed = true)} title="Collapse chat" aria-label="Collapse chat">✕</button>
			</div>
		</div>

		<div class="quick">
			{#each quickActions as qa (qa.label)}
				<button class="quick-chip" onclick={() => send(qa.prompt)} disabled={sending}>{qa.label}</button>
			{/each}
		</div>

		<div class="messages">
			{#each messages as m (m)}
				<div class="msg msg-{m.role}">
					<div class="bubble">
						{#if m.text}
							<div class="md">{@html marked.parse(m.text)}</div>
						{/if}
						{#if m.streaming && !m.text && m.tools.length === 0}
							<span class="typing">{status || '…'}</span>
						{/if}
						{#each m.tools as t (t)}
							<details class="tool" open={t.status !== 'completed'}>
								<summary>{t.name} · {t.status}</summary>
								{#if t.input && Object.keys(t.input).length}
									<pre class="tool-block">$ {JSON.stringify(t.input)}</pre>
								{/if}
								{#if t.output}
									<pre class="tool-block out">{t.output}</pre>
								{/if}
							</details>
						{/each}
					</div>
				</div>
			{/each}
		</div>

		{#if sending}
			<div class="status">{status || 'Thinking...'}</div>
		{/if}
		<div class="composer">
			<textarea bind:value={input} onkeydown={onKey} placeholder="ask or command… (Enter to send, Shift+Enter for newline)" rows="3"></textarea>
			<button class="send" onclick={() => send()} disabled={sending || !input.trim()}>
				{sending ? '…' : 'send'}
			</button>
		</div>
	{/if}
</aside>

<style>
	.dock {
		position: fixed;
		top: auto;
		bottom: 16px;
		right: 16px;
		z-index: 40;
		display: flex;
		flex-direction: column;
		border: 1px solid var(--border);
		background: var(--bg-elev);
		border-radius: 12px;
		box-shadow: 0 8px 24px rgba(0,0,0,.35);
		overflow: hidden;
	}
	.dock.collapsed {
		width: 52px;
		height: 52px;
		border-radius: 26px;
	}
	.dock:not(.collapsed) {
		width: 570px;
		height: calc(100vh - 32px);
	}
	.expand-btn {
		width: 100%;
		height: 100%;
		border: none;
		background: var(--accent);
		color: var(--bg);
		font-size: 22px;
		cursor: pointer;
	}
	.dock-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 10px 12px;
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
	}
	.title {
		font-weight: 600;
		font-size: 14px;
		color: var(--text);
	}
	.actions {
		display: flex;
		gap: 4px;
	}
	.ghost {
		background: transparent;
		border: 1px solid var(--border-strong);
		color: var(--muted);
		border-radius: 4px;
		padding: 4px 8px;
		font: inherit;
		font-size: 12px;
		cursor: pointer;
		min-width: 28px;
	}
	.ghost:hover {
		color: var(--text);
		background: var(--panel);
	}
	.quick {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		padding: 8px 10px;
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
	}
	.quick-chip {
		background: var(--panel);
		border: 1px solid var(--border-strong);
		color: var(--text);
		border-radius: 10px;
		padding: 3px 9px;
		font: inherit;
		font-size: 12px;
		cursor: pointer;
	}
	.quick-chip:disabled {
		opacity: .5;
		cursor: default;
	}
	.messages {
		flex: 1;
		overflow-y: auto;
		padding: 10px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.msg {
		display: flex;
	}
	.msg-user {
		justify-content: flex-end;
	}
	.bubble {
		max-width: 92%;
		background: var(--panel);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 8px 10px;
		font-size: 14px;
		color: var(--text);
	}
	.msg-user .bubble {
		background: #1e2a3a;
		border-color: #2a3a5a;
	}
	:global(html[data-theme='light']) .msg-user .bubble {
		background: #eaf0fb;
		border-color: #b6c9f5;
		color: var(--text);
	}
	.text {
		margin: 0;
		white-space: pre-wrap;
		word-break: break-word;
		font: inherit;
	}
	.typing {
		color: var(--muted);
	}
	.md {
		margin: 0;
		font: inherit;
		font-size: 14px;
		line-height: 1.45;
		color: var(--text);
		word-break: break-word;
	}
	.md :global(p) { margin: 0 0 8px; }
	.md :global(p:last-child) { margin-bottom: 0; }
	.md :global(h1), .md :global(h2), .md :global(h3), .md :global(h4) {
		margin: 10px 0 4px;
		font-weight: 600;
		line-height: 1.25;
	}
	.md :global(h1) { font-size: 18px; }
	.md :global(h2) { font-size: 16px; }
	.md :global(h3) { font-size: 15px; }
	.md :global(ul), .md :global(ol) { margin: 4px 0 8px 20px; padding: 0; }
	.md :global(li) { margin: 2px 0; }
	.md :global(code) {
		font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
		font-size: 12.5px;
		background: var(--bg);
		border: 1px solid var(--border-strong);
		border-radius: 4px;
		padding: 1px 4px;
	}
	.md :global(pre) {
		margin: 6px 0;
		padding: 8px 10px;
		background: var(--bg);
		border: 1px solid var(--border-strong);
		border-radius: 6px;
		overflow: auto;
		max-height: 240px;
	}
	.md :global(pre code) {
		background: transparent;
		border: none;
		padding: 0;
		font-size: 12.5px;
	}
	.md :global(a) { color: var(--accent); }
	.md :global(blockquote) {
		margin: 6px 0;
		padding: 4px 10px;
		border-left: 3px solid var(--border-strong);
		color: var(--muted);
	}
	.md :global(table) { border-collapse: collapse; margin: 6px 0; }
	.md :global(th), .md :global(td) { border: 1px solid var(--border-strong); padding: 4px 8px; }
	.tool {
		margin-top: 6px;
		background: var(--bg);
		border: 1px solid var(--border-strong);
		border-radius: 6px;
		padding: 4px 8px;
		font-size: 12px;
	}
	.tool summary {
		cursor: pointer;
		color: var(--accent);
	}
	.tool-block {
		margin: 4px 0 0;
		white-space: pre-wrap;
		word-break: break-all;
		font: inherit;
		font-size: 12px;
		color: var(--text);
		max-height: 160px;
		overflow: auto;
	}
	.tool-block.out {
		color: var(--teal);
	}
	.status {
		padding: 4px 10px;
		font-size: 12px;
		color: var(--muted);
		border-top: 1px solid var(--border);
		flex-shrink: 0;
	}
	.composer {
		display: flex;
		gap: 6px;
		padding: 10px;
		border-top: 1px solid var(--border);
		flex-shrink: 0;
	}
	.composer textarea {
		flex: 1;
		background: var(--panel);
		color: var(--text);
		border: 1px solid var(--border-strong);
		border-radius: 6px;
		padding: 6px 8px;
		font: inherit;
		font-size: 13px;
		resize: none;
	}
	.send {
		align-self: flex-end;
		background: var(--border-strong);
		color: var(--text);
		border: 1px solid var(--border-hover);
		border-radius: 6px;
		padding: 6px 12px;
		font: inherit;
		cursor: pointer;
	}
	.send:disabled {
		opacity: .5;
		cursor: default;
	}
</style>
