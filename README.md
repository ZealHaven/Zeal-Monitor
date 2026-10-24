# Monitor

Ops dashboard + chat for `<tailnet-domain>`. See [`docs/plans/implemented/1-initial.md`](docs/plans/implemented/1-initial.md) for the full plan.

Service-per-directory monorepo — split-ready for future microservices and microfrontends.

## Layout

```
backend/main/       main backend service (Go, :9050) — front door: API + SSE + proxy
backend/<name>/     future services, listed in go.work
frontend/monitor/   monitor app (SvelteKit 3 SSR, :9051 prod / :9065 dev, loopback only)
ops/                nginx vhost, dnsmasq zone, systemd units, Cloudflare scripts
```

## Access (all portless)

| Where | URL |
|---|---|
| This machine | `http://monitor.local` |
| Tailscale | `http://monitor.<tailnet-domain>` |
| Public | `https://monitor-dev.example.com` (optional, via Cloudflare Access) |

## Development

```sh
just dev        # monitord (:9050) + frontend dev server (:9065), proxied together
just build      # Go binary + SvelteKit build
just deploy     # install systemd user units and restart
just nginx      # install the nginx vhost and reload
just dns        # install the dnsmasq zone and verify
just cf-access  # create/update the Cloudflare Access app
```

## Milestones

M0 scaffold · M1 inventory · M2 local access · M3 chat dock · M4 tailnet access · M5 public access · M6 polish
