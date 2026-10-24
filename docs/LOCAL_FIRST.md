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

All environment-specific values (IPs, domains, paths) are configurable via
`ops/config.env` (see `ops/config.env.example`).
