import type { Service } from './types';

export type ChatTool = {
	name: string;
	input: Record<string, unknown>;
	output: string;
	status: string;
};

export type ChatMessage = {
	role: 'user' | 'assistant';
	text: string;
	tools: ChatTool[];
	streaming: boolean;
};

export type ChatEvent =
	| { type: 'session'; session_id: string }
	| { type: 'text'; text: string }
	| { type: 'tool'; tool: string; tool_input?: Record<string, unknown>; tool_output?: string; tool_status?: string }
	| { type: 'step_start' }
	| { type: 'step_finish' }
	| { type: 'error'; error: string }
	| { type: 'done'; session_id?: string };

export async function sendChat(
	sessionId: string,
	message: string,
	onEvent: (e: ChatEvent) => void
): Promise<void> {
	const res = await fetch('/api/chat', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ session_id: sessionId, message })
	});
	if (!res.ok || !res.body) throw new Error(`chat: ${res.status}`);
	const reader = res.body.getReader();
	const dec = new TextDecoder();
	let buf = '';
	while (true) {
		const { done, value } = await reader.read();
		if (done) break;
		buf += dec.decode(value, { stream: true });
		const lines = buf.split('\n');
		buf = lines.pop() ?? '';
		for (const line of lines) {
			if (line.startsWith('data: ')) {
				try {
					onEvent(JSON.parse(line.slice(6)) as ChatEvent);
				} catch {
					/* ignore malformed */
				}
			}
		}
	}
}
