export type Alias = { kind: 'local' | 'tailnet' | 'public'; host: string; url: string };
export type Listen = { proto: string; addr: string; port: number };
export type Health = { state: 'up' | 'down' | 'unknown'; http?: number; ms: number };
export type Service = {
	pid: number;
	proc: string;
	user: string;
	cmd: string;
	cwd: string;
	unit?: string;
	container?: string;
	image?: string;
	listen: Listen[];
	aliases: Alias[];
	health?: Health;
};
