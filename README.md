# Monitor

A local-first ops dashboard and chat for a single Linux host. Monitor discovers
what is running on the machine — listening ports, owning processes, working
directories, systemd units, Docker containers, nginx vhosts, Cloudflare tunnel
routes, and `/etc/hosts` names — then presents it as a live, clickable inventory.
A built-in chat dock can inspect and act on the host through an agent.

Designed to run on `127.0.0.1` by default. Tailnet and public access are
optional, opt-in integrations.

## Features

- **Live inventory** — processes, ports, users, commands, working directories,
  systemd units, Docker containers, and health status.
- **Aliases** — local, tailnet, and public names mapped to each service, with
  clickable links.
- **Health probes** — concurrent HTTP checks with up/down/unknown states.
- **SSE updates** — the dashboard streams inventory changes as they happen.
- **Chat dock** — streamed agent responses, visible tool calls, and quick
  actions for common ops tasks.
- **Audit log** — every shell command issued through chat is appended to a local
  JSONL file.
- **Single front door** — one Go process serves the API and reverse-proxies the
  SvelteKit frontend.

## Architecture

```
browser ──▶ monitord :9050 ──▶ /api/*        (JSON + SSE)
                     └──────▶ frontend :9051 (SvelteKit SSR)
                                  │
                                  └──▶ agent process (opencode serve, loopback)
```

`monitord` is the only entry point. In development it proxies to the Vite dev
server; in production it proxies to the built SvelteKit Node server.

## Requirements

- Linux (uses `ss`, `ps`, `/proc`, and optionally `sudo`, `docker`, `systemctl`)
- Go (see `backend/main/go.mod`)
- Node.js 20+ and npm
- `just` (optional, but recommended)

Some collectors use `sudo -n` for privileged reads. Without passwordless sudo,
those sources are skipped and the rest of the dashboard still works.

## Quick start (local only)

```sh
cp .env.example .env
just build
just dev
```

Open <http://127.0.0.1:9050>.

To use the friendly local hostname:

```sh
just hosts   # adds 127.0.0.1 monitor.local to /etc/hosts
```

Then open <http://monitor.local>.

## Configuration

All environment-specific values live in `.env` (gitignored). Start from
`.env.example`.

| Variable | Default | Purpose |
|---|---|---|
| `MONITORD_ADDR` | `127.0.0.1:9050` | Backend bind address |
| `FRONTEND_PORT` | `9051` | Production frontend port |
| `VITE_PORT` | `9065` | Development frontend port |
| `OPENCODE_PORT` | `9060` | Dedicated agent server port |
| `REPO_ROOT` | `.` | Repo root used to locate prompts |
| `MONITOR_CONFIG` | `~/.config/monitor/config.json` | Chat LLM settings file (written by the Settings page) |
| `MONITOR_MODEL` | — | Override the configured model (wins over the settings file) |
| `TAILSCALE_IP` | `127.0.0.1` | Tailnet IP for optional DNS setup |
| `TAILNET_DOMAIN` | `local` | Optional tailnet domain suffix |
| `PUBLIC_DOMAIN` | `example.com` | Optional public domain |
| `MONITOR_CORS_ORIGIN` | same-origin | API CORS origin (`*` for any, `-` disables) |

## LLM configuration

Open the **Settings** page in the dashboard to choose the chat agent's provider
and model, and to add a provider API key or endpoint override. The current
model is also shown in the chat header.

- Settings are stored in `~/.config/monitor/config.json` (mode `0600`), or
  `$MONITOR_CONFIG` when set. Keys are write-only in the UI and redacted on read.
- A model change applies to the next message via `opencode run --model`.
- A provider or credential change restarts the dedicated `opencode serve`
  process (about a second). If a chat is streaming, the save is refused with
  `409` so the stream is never cut.
- Providers already connected through `opencode auth login`, and standard
  provider variables such as `ANTHROPIC_API_KEY`, keep working and are shown as
  `OpenCode auth` or `env` in the UI.
- The provider list comes from OpenCode's integration catalog, so providers you
  have not connected yet (for example Anthropic, OpenAI, or Google) are listed
  too. Add a key for one in **Provider credentials**; the agent restarts and its
  models appear in the model picker.
- Precedence: compiled default, then the settings file, then environment
  (`MONITOR_MODEL`, `MONITOR_LLM_<PROVIDER>_API_KEY`). Environment values win;
  an environment model is shown as locked.

The `opencode-go/beam-free` default is used when no settings file exists.

## Common tasks

```sh
just --list      # show all tasks
just dev         # monitord + Vite dev server
just dev-backend # backend only
just build       # Go binary + SvelteKit build
just test        # Go tests
just check       # go vet + svelte-check
just fmt         # gofmt backend
```

## Production deployment

The included systemd user units assume the repository is checked out on the
target host. Adjust paths and environment values before installing.

```sh
just deploy          # build, install user units, restart services
just deploy-frontend # rebuild and restart only the frontend
```

Services:

- `monitord.service` — backend on `MONITORD_ADDR`
- `monitor-web.service` — SvelteKit Node server on `FRONTEND_PORT`

Check status with:

```sh
systemctl --user status monitord monitor-web
```

## Optional integrations

These are not required for local use.

- **nginx** — serve friendly hostnames and reverse-proxy apps on the host.
- **dnsmasq** — wildcard DNS for a tailnet domain.
- **Tailscale** — reach the dashboard from other devices.
- **Cloudflare Access** — publish publicly behind an identity provider.

Relevant files live under `ops/`:

```
ops/nginx/        nginx vhost and expose helper
ops/dnsmasq/      wildcard DNS zone template
ops/systemd/      user service units
ops/cloudflare/   optional Access + tunnel setup
ops/opencode/     system prompt for the chat agent
```

See [`docs/LOCAL_FIRST.md`](docs/LOCAL_FIRST.md) for the minimal local setup.

## API

| Method | Path | Description |
|---|---|---|
| `GET` | `/healthz` | Liveness check |
| `GET` | `/api/services` | Current inventory |
| `GET` | `/api/services/{pid}` | One service by PID |
| `POST` | `/api/scan` | Force a rescan |
| `GET` | `/api/events` | Inventory changes (SSE) |
| `POST` | `/api/chat` | Chat with the agent (SSE) |
| `GET` | `/api/audit?limit=N` | Recent audited commands |
| `GET` | `/api/settings/llm` | Redacted LLM settings + agent status |
| `PUT` | `/api/settings/llm` | Update model and/or provider credentials |
| `POST` | `/api/settings/llm/reload` | Restart the agent (picks up external auth changes) |
| `GET` | `/api/llm/models` | Provider/model catalog from the running agent |

## Security

Monitor is intended for a trusted, local machine.

- The chat agent can run shell commands. The included agent profile allows
  `bash` and the host account may have passwordless sudo.
- Keep `monitord` bound to loopback unless you add authentication in front of
  it. The bundled nginx/Cloudflare setup is optional and must be secured
  separately.
- The audit log is written to `~/.local/state/monitor/audit.jsonl` with mode
  `0600`.
- CORS defaults to same-origin so the SvelteKit SSR server and browser both
  work through the monitord front door. Set `MONITOR_CORS_ORIGIN=*` to allow
  any origin, or `-` to disable CORS headers.

Do not expose the chat endpoint to an untrusted network without an
authenticating proxy.

## Project layout

```
backend/main/       Go service (API, SSE, proxy, collectors, agent harness)
backend/<name>/     future services, listed in go.work
frontend/monitor/   SvelteKit app
ops/                nginx, dnsmasq, systemd, and Cloudflare helpers
docs/               guides and implementation notes
```

## Status

Early-stage project. The core inventory, dashboard, chat dock, and local
deployment flow work; optional network integrations vary by environment.
