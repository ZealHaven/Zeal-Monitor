# Local-first mode

Monitor can run entirely on one machine without Tailscale, Cloudflare, nginx,
dnsmasq, or systemd.

## Minimal run

1. Build: `just build`
2. Run backend: `cd backend/main && go run ./cmd/monitord --addr 127.0.0.1:9050`
3. Run frontend: `cd frontend/monitor && npm run dev` (or build + `node build`)
4. Open `http://127.0.0.1:9050`

## Optional integrations

- Tailnet access: configure `ops/dnsmasq/` and `ops/nginx/` (see ops scripts).
- Public access: configure `ops/cloudflare/create-access-app.sh` with your own
  Cloudflare account.
- Systemd: install units from `ops/systemd/` after adjusting paths.

## Chat model

The chat agent works without an API key when OpenCode already has a provider
connected (`opencode auth login`). To use a different provider or model, open
**Settings** in the dashboard. Settings are stored in
`~/.config/monitor/config.json` (mode `0600`); set `MONITOR_CONFIG` to override
the path or `MONITOR_MODEL` to force a model from the environment. Model changes
apply to the next message; credential changes restart the local agent.

All environment-specific values (IPs, domains, paths) are configurable via
`ops/config.env` (see `ops/config.env.example`).
