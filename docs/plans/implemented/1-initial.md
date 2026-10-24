# Monitor — Initial Plan

**Status:** draft v3 (decisions locked) · **Date:** 2026-10-17 · **Owner:** <owner-email>

## 1. Goal

A self-hosted ops dashboard for `<tailnet-domain>` that:

1. **Inventory** — shows every listening port on the machine: port, owning process, working directory/path, and the DNS names (local `.local`, tailnet `*.<tailnet-domain>`, public `*.example.com`) mapped to it. DNS names are clickable links that jump straight into the app.
2. **Chat dock (right side)** — a quick chat powered by **OpenCode** that can *act* on the machine: switch a port, restart a service, free a port, etc.
3. **Access — no port typing anywhere:**

| Where | URL (all portless) |
|---|---|
| This machine | `http://monitor.local` |
| Tailscale devices | `http://monitor.<tailnet-domain>` |
| Public | `https://monitor-dev.example.com` (behind Cloudflare Access — Google login, **only** <owner-email>) |

> `:9050` is the backend's bind port (confirmed), typed only as a raw fallback (`http://monitor.<tailnet-domain>:9050` would work too, but the canonical URL is portless).

### Decisions taken (Q&A rounds)

| Question | Decision |
|---|---|
| Tailnet name | **`http://monitor.<tailnet-domain>`** — exact name, portless. Requires wiring the `<tailnet-domain>` zone to this box: Tailscale **split DNS** (one 2-min admin-console entry) + a local **dnsmasq** wildcard zone + nginx vhost. See §7. |
| Multi-app requirement | The `*.<tailnet-domain>` zone is **wildcard**: every app on this machine can be exposed tailnet-wide as `<name>.<tailnet-domain>` by adding one `server_name` to its nginx vhost. Existing access patterns (FQDN `<tailnet-domain>.<tailnet-suffix>`, `<tailnet-domain>:<port>`) stay untouched. |
| Backend port | **9050** (Go daemon, front door). SvelteKit `9051` + dedicated OpenCode `9060` stay loopback-only. |
| Chat harness | **OpenCode** (`opencode serve` HTTP API + SDK). |
| Public auth | **Cloudflare Access + Google, allow only <owner-email>** — created via **Cloudflare API using the existing `CF_API_TOKEN`** ("we have the CLI, leverage it"). No dashboard work. |
| Privilege level | **Root-powered** — chat runs as `<user>` with existing `NOPASSWD: ALL` + docker group. Audit log + a few hard deny-patterns only. |
| Stack | **Go backend** (single daemon, front door) + **SvelteKit SSR** frontend. |

---

## 2. Environment survey (verified 2026-10-16/17)

- **Host:** `<tailnet-domain>`, Tailscale IP `<tailscale-ip>`, tailnet suffix `<tailnet-suffix>`, MagicDNS on, search domain `<tailnet-suffix>`.
- **Tailscale DNS today:** split-DNS routes only `ts.net.`; **no `<tailnet-domain>` zone yet** (`dig monitor.<tailnet-domain> @100.100.100.100` → empty). Admin console currently has **no custom nameserver** — adding zone `<tailnet-domain> → <tailscale-ip>` is the one manual step (§7).
- **Port 53** on the tailscale IP is **free** (only systemd-resolved holds `127.0.0.53:53` / `127.0.0.54:53`). `dnsmasq` installable via existing `NOPASSWD: apt`.
- **nginx** on `:80` (root); one vhost per app in `/etc/nginx/sites-enabled/*.conf`. ⚠️ `jobhunt.conf` owns `server_name jobhunt.local <tailnet-domain>.<tailnet-suffix>` → we register **short/clean names only** (`monitor.local`, `monitor.<tailnet-domain>`), never the FQDN, avoiding vhost-order ambiguity.
- **`/etc/hosts`** maps `9router.local`, `deepseek.local`, `jobhunt.local`, `rowbot.local`, `test1.local`, `nginx-app.local` → `127.0.0.1` (convention we follow with `monitor.local`).
- **cloudflared** system service runs **`/etc/cloudflared/config.yml`** (⚠️ the active config; `~/.cloudflared/config.yml` is a stale duplicate). Tunnel `<tunnel-name>` (`<tunnel-id>`) serves ~30 `*.example.com` hostnames. `monitor.example.com` → `:9009` is **uptime-kuma — stays untouched**.
- **Cloudflare API ready:** `CF_ACCOUNT_ID=<cf-account-id>`, `CF_API_TOKEN` (**verified valid; read/write Zero Trust Access apps**), `CF_ACCESS_EMAIL=<owner-email>`. Existing Google IdP `7dbf5e99-…` + template app **"Zeal PC Access"** (`local.example.com`, policy "Only <owner-email>", 24h session). ⚠️ `CF_DOMAIN`/`CF_APP_NAME` env values belong to that old app — the new script sets its own.
- **OpenCode:** installed; `opencode serve --service` on `127.0.0.1:49376` (password auth via `~/.config/opencode/service.json`); agents/permissions in `~/.config/opencode/opencode.jsonc`.
- **Privileges:** `/etc/sudoers.d/<user>` = `NOPASSWD: ALL`, `docker` group, `Linger=yes`. **No sudoers/systemd prep needed.**
- **~60 listening ports**: node/next/python/go apps, docker (postgres, odoo, moodle, uptime-kuma…), kubelets, nginx, mailhog, etc.
- **Ports:** `9050` Go (entry), `9051` SvelteKit (internal), `9060` dedicated OpenCode (internal) — all verified free.

---

## 3. Access matrix (requirement 3 + 4) — all portless

| Where | URL | Mechanism | Work |
|---|---|---|---|
| This machine | `http://monitor.local` | `/etc/hosts` → `127.0.0.1` + nginx vhost → `127.0.0.1:9050` | hosts line + `sites-enabled/monitor.conf` |
| Tailnet | `http://monitor.<tailnet-domain>` | Tailscale split-DNS zone `<tailnet-domain> → <tailscale-ip>` → local dnsmasq wildcard → nginx vhost (`Host: monitor.<tailnet-domain>`) → `:9050` | admin-console nameserver entry + dnsmasq unit + nginx vhost (§7) |
| Tailnet fallback | `http://<tailnet-domain>:9050` | Go daemon binds `0.0.0.0:9050` | none (verified) |
| Public | `https://monitor-dev.example.com` | cloudflared ingress → `127.0.0.1:9050`, **Cloudflare Access (Google, allow only you)** at the edge | 1 ingress line + DNS route + Access app via API |

**nginx vhost** (mirrors existing style; SSE/streaming headers included):

```nginx
# /etc/nginx/sites-enabled/monitor.conf
server {
    listen 80;
    server_name monitor.local monitor.<tailnet-domain>;
    location / {
        proxy_pass http://127.0.0.1:9050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;      # chat streaming
    }
}
```

**cloudflared ingress** (add to `/etc/cloudflared/config.yml`, *before* the `http_status:404` catch-all):

```yaml
  - hostname: monitor-dev.example.com
    service: http://127.0.0.1:9050
```

---

## 4. Architecture

```
        ┌──────────────────────── <tailnet-domain> box ─────────────────────────┐
 browser ── monitor.local │ monitor.<tailnet-domain> │ monitor-dev.example.com ─▶ Go daemon :9050 (0.0.0.0)
        │     (nginx :80)    (split-DNS→dnsmasq→nginx)  (cloudflared+Access)   ├─ /api/*  inventory, chat, audit
        │                                                                      ├─ SSE /api/chat/stream, /api/events
        │                                                                      └─ proxy non-/api ─▶ SvelteKit :9051
        │   Go collector: sudo ss/ps/proc + nginx + cloudflared + docker + hosts + systemd (10s poll)
        │   Go harness client ── HTTP API ── dedicated opencode serve :9060 (loopback, own token)
        │   dnsmasq :53 (tailscale IP) — wildcard *.<tailnet-domain> → <tailscale-ip>  [zone routed by Tailscale]
        └────────────────────────────────────────────────────────────────────┘
```

- **Go daemon on `:9050` is the single front door**: API + SSE + reverse-proxy to SvelteKit SSR. SvelteKit (`127.0.0.1:9051`) and OpenCode (`127.0.0.1:9060`) are never directly exposed.
- nginx / cloudflared only map **clean names → :9050**; no port ever needs typing.
- **Tailnet DNS lane** (new): Tailscale splits zone `<tailnet-domain>` to this box → dnsmasq answers `* → <tailscale-ip>` → nginx routes by Host header. One wildcard zone serves **all** apps.

### Repo layout — monorepo, built to split later

One repo, **service-per-directory** from day one, so a BE microservice or FE microfrontend is a new sibling directory — never a re-architecture:

```
~/Zeal/Monitor
├── docs/plans/1-initial.md
├── go.work                  # Go workspace: lists backend/* modules (add new svcs here)
├── backend/                 # ONE directory per backend service
│   ├── main/                # ★ main BE service (v1 monolith: API + SSE + proxy)
│   │   ├── go.mod           # module monitor/backend/main
│   │   ├── cmd/monitord/main.go
│   │   └── internal/
│   │       ├── collect/     # port/process/path + alias discovery
│   │       ├── inventory/   # model, merge, health probes, cache
│   │       ├── harness/     # opencode client (sessions, stream, tool events)
│   │       ├── api/         # HTTP handlers, SSE hub, reverse-proxy
│   │       └── audit/       # append-only action log (JSONL)
│   └── (future) dns/, chat/, collector/ …   # extract when a boundary earns itself
├── frontend/                # ONE directory per frontend app
│   ├── monitor/             # ★ monitor app (SvelteKit SSR, adapter-node, :9051)
│   │   ├── svelte.config.js
│   │   └── src/…
│   └── (future) shell/, inventory/, shared/  # microfrontends + shared UI package
├── ops/
│   ├── systemd/             # user units: monitord.service, monitor-web.service
│   ├── nginx/monitor.conf   # vhost to drop into /etc/nginx/sites-enabled/
│   ├── dnsmasq/<tailnet-domain>.conf  # wildcard zone config for /etc/dnsmasq.d/
│   └── cloudflare/          # Access app + tunnel ingress script (curl + CF_API_TOKEN)
├── justfile                 # dev / build / deploy / dns / cf-access recipes
└── README.md
```

**Extensibility conventions** (kept cheap on purpose):

- **Backend services**: each is its own Go module with its own `cmd/`, bound to its own port (registry kept in the root `justfile`); `backend/main` remains the **front door** and reverse-proxies `/api/<svc>/*` to sibling services when they appear — external URLs never change. Shared code goes in `backend/pkg/` (importable by all modules) only when a second consumer exists. Each new service gets its own systemd user unit pattern (copy `ops/systemd/`).
- **Frontend apps**: each is a self-contained SvelteKit (or other) app with its own build/deploy unit, reached at its own hostname or path prefix via nginx. Microfrontend composition (a shell app + runtime injection, e.g. via Vite module federation or simple iframe/edge-includes) is an *option to take later*, not a constraint now — until then every app is independently deployable and the monitor works standalone.
- **Naming**: directory = service name = systemd unit prefix (`monitord`, `monitor-web`) = tailnet alias prefix (`monitor.<tailnet-domain>`).

---

### v1 composition (this plan)

Everything ships as **two deployable units**: `backend/main` (Go, `:9050`, front door for API + SSE + static proxy) and `frontend/monitor` (SvelteKit SSR, `:9051`, loopback-only). No shared infra beyond `go.work`/the justfile — the split-ready layout costs nothing today and no migrations later.

---

## 5. Feature 1 — Inventory (ports, process, path, DNS links)

### Data sources (merged into one `Service` model)

| Source | Yields |
|---|---|
| `sudo -n ss -tlnpH` | port, bind address, pid, process (full view incl. root procs) |
| `/proc/<pid>/{cmdline,cwd,exe}` + `ps -ef` | full command line, **working directory/path**, executable |
| `systemctl list-units` + cgroup of pid | owning systemd unit → restart target |
| `docker ps` + `docker inspect` | container name/image, published ports |
| `/etc/nginx/sites-enabled/*.conf` parser | `server_name` → `proxy_pass` = **`.local` / `.<tailnet-domain>` / FQDN aliases → port** |
| `/etc/cloudflared/config.yml` parser | `hostname` → service = **public `*.example.com` aliases → port** |
| `/etc/hosts` | `*.local` aliases → IP |
| dnsmasq zone + MagicDNS self-name | every non-loopback service gets an auto-derived `http://<name>.<tailnet-domain>`-style tailnet link |

### Model (per service)

```jsonc
{
  "pid": 1663, "proc": "node", "cmd": "node …/n8n start --tunnel",
  "cwd": "/home/<user>/…", "unit": null, "container": null,
  "listen": [{ "addr": "0.0.0.0", "port": 5678, "proto": "tcp" }],
  "aliases": [
    { "kind": "local",   "host": "9router.local",         "url": "http://9router.local" },
    { "kind": "tailnet", "host": "9router.<tailnet-domain>",  "url": "http://9router.<tailnet-domain>" },
    { "kind": "tailnet", "host": "<tailnet-domain>",          "url": "http://<tailnet-domain>:5678" },  // raw fallback
    { "kind": "public",  "host": "mermaid.example.com", "url": "https://mermaid.example.com" }
  ],
  "health": { "state": "up", "http": 200, "ms": 24 }
}
```

### API

- `GET /api/services` — inventory (10s poll + `POST /api/scan` force refresh).
- `GET /api/events` (SSE) — live diffs + health changes.
- `GET /api/services/:pid` — detail (full cmd, cwd, unit, container, aliases).
- `POST /api/scan` — rescan after chat-driven changes (the agent is prompted to call it).

### UI (SvelteKit)

- Table/grouped cards: port(s) · process + truncated cmd · **path (cwd)** · kind badge (native/docker/systemd) · health dot · alias **link chips (click to open in new tab)**.
- Search (port, name, path, host), sort, SSE auto-refresh, copy URL/curl.
- Detail drawer per process: full cmd, cwd, unit, container, related aliases.

---

## 6. Feature 2 — Chat dock (OpenCode, root-powered)

- **Dedicated instance**: `opencode serve --port 9060`, spawned and supervised by `monitord` (auto-restart), own token + agent `monitor` (model from your `opencode.jsonc` set), working dir `/home/<user>`. Loopback only; the Go daemon is the sole client (browser never sees the token).
- **Transport**: Go → OpenCode HTTP API (create session, send message, subscribe to events; auth header/SDK confirmed against current OpenCode docs at implementation) → normalized SSE → chat dock.
- **Machine context**: the `monitor` agent's system prompt covers box layout, sudo/docker availability, nginx/conf paths, and points at `curl -s http://127.0.0.1:9050/api/services` for live inventory.
- **Quick-action chips**: "Restart nginx" · "Restart the service on port 3000" · "Switch port 3000 → 3001" (edits nginx vhost / compose / unit, restarts, re-scans) · "What's using 5432?" · "Free port 8765".
- **Sessions**: one OpenCode session per chat thread, id persisted in localStorage → history survives reloads; "new thread" button.
- **Streaming + tool visibility**: live token stream; tool calls render as collapsible "ran `…`" blocks with output; inventory auto-refreshes after mutating commands.
- **Audit log** (root-power backstop): every model-issued command → `~/.local/state/monitor/audit.jsonl` + `/audit` view (time, command, exit code, chat thread).
- **Safety (light, per "root-powered" decision)**: only hard deny-patterns in the agent's opencode `permission` config (`rm -rf /`, `mkfs*`, `dd of=/dev/*`, `shutdown|reboot`, `iptables -F`); prompt requires `nginx -t` before reload; everything else allowed — the account is `NOPASSWD: ALL` by design.

---

## 7. Access plumbing — the three legs

### 7a. Local: `http://monitor.local`

1. Add `127.0.0.1  monitor.local` to `/etc/hosts`.
2. Drop `ops/nginx/monitor.conf` → `/etc/nginx/sites-enabled/`, `sudo nginx -t && sudo systemctl reload nginx`.
3. Go daemon binds `0.0.0.0:9050`.

### 7b. Tailnet: `http://monitor.<tailnet-domain>` (wildcard zone for *all* apps)

**How the name resolves** — MagicDNS can't hold arbitrary records, so the zone is routed by split DNS:

1. **Admin console (the one manual step, ~2 min, needs your Tailscale login)** — https://login.tailscale.com/admin/dns → *Nameservers → Add nameserver → Custom* → address `<tailscale-ip>`, domain **`<tailnet-domain>`** (restricted/split — applies to `*.<tailnet-domain>`). Enable *"Use with exit node"* if you ever route through one.
2. **dnsmasq on this box** (`ops/dnsmasq/<tailnet-domain>.conf` → `/etc/dnsmasq.d/`, system service): bind `<tailscale-ip>:53`, answer the whole zone: `address=/<tailnet-domain>/<tailscale-ip>` (wildcard → every `<name>.<tailnet-domain>` resolves to this machine). Port 53 on the tailscale IP is confirmed free.
3. **nginx** vhost `server_name monitor.<tailnet-domain> …` → `:9050` (§3).
4. **Verify**: from this box `dig +short monitor.<tailnet-domain> @100.100.100.100` → `<tailscale-ip>`; from `zeal-staging` the same + browser `http://monitor.<tailnet-domain>`.

**Why this satisfies "several apps must stay accessible":**
- The zone is a **wildcard** — exposing any other app tailnet-wide costs one line: add `<name>.<tailnet-domain>` to that app's nginx `server_name` → instantly `http://<name>.<tailnet-domain>`. (A `just expose <name> <port>` helper can automate this.)
- **Nothing existing breaks**: current access via FQDN (`http://<tailnet-domain>.<tailnet-suffix>`, jobhunt vhost) and `<tailnet-domain>:<port>` direct continues to work — split DNS only *adds* a zone route; MagicDNS and nginx are untouched.
- Optional: a wildcard `*.<tailnet-domain>` nginx server (default → monitor) so typos don't fall through to the stock nginx page.

**If the console rejects the single-label domain `<tailnet-domain>`** (validation behavior unverified — check at step 1), fallbacks in order:
1. **`extraDNSRecords`** in the tailnet ACL policy (`{"Name":"monitor.<tailnet-domain>","Value":"<tailscale-ip>"}` …) — exact result, but the alpha feature must be enabled by Tailscale support (slower).
2. **Per-client `/etc/hosts`** on each machine that needs it (`<tailscale-ip> monitor.<tailnet-domain>`) — works today, manual per device, not for phones.
3. Keep `http://<tailnet-domain>:9050` style raw access meanwhile (always works via MagicDNS).

### 7c. Public: `https://monitor-dev.example.com` — fully scripted via API

No dashboard, no waiting — `CF_API_TOKEN` verified (valid + Access-app perms), Google IdP exists, template app exists.

1. **DNS route**: `sudo cloudflared tunnel route dns <tunnel-name> monitor-dev.example.com` → auto CNAME (cert.pem has Zone:DNS:Edit). Distinct from `monitor.example.com` → uptime-kuma, which stays.
2. **Ingress**: insert `monitor-dev.example.com → http://127.0.0.1:9050` into `/etc/cloudflared/config.yml` before the catch-all; `sudo systemctl restart cloudflared`.
3. **Access app** (`ops/cloudflare/create-access-app.sh`, idempotent — GET by domain, create/update):
   - `POST /accounts/$CF_ACCOUNT_ID/access/apps` — `type=self_hosted`, domain `monitor-dev.example.com`, name "Monitor Dev Access", `session_duration=24h`, `allowed_idps=[7dbf5e99-… (Google)]`
   - policy `decision=allow`, `include=[{email: <owner-email>}]`, name "Only <owner-email>"
   - script passes its own domain/name params (env's `CF_DOMAIN`/`CF_APP_NAME` belong to the old app).
4. Effect: every public request authenticates at Cloudflare's edge (Google, only you); origin only sees Access-authenticated traffic plus loopback/tailnet/local traffic.

**Token security note:** `CF_API_TOKEN` is read from env at script time — never hardcoded, never committed, never exposed to the browser.

---

## 8. Security model

| Surface | Exposure | Control |
|---|---|---|
| nginx :80 (`monitor.local`, `monitor.<tailnet-domain>`, existing vhosts) | this machine + tailnet | intended; machine/tailnet treated as trusted (same as your existing `.local` apps) |
| dnsmasq `:53` (tailscale IP) | tailnet only | answers **only** the `<tailnet-domain>` zone (restricted nameserver); wildcard → this box |
| `:9050` on `0.0.0.0` | tailnet + LAN | direct fallback URL; optional ufw restriction to `100.64.0.0/10` + loopback if LAN exposure is unwanted |
| `monitor-dev.example.com` | internet | **Cloudflare Access — Google IdP, allow only <owner-email>**, 24h session; origin loopback-only behind tunnel |
| `:9051` (SvelteKit), `:9060` (OpenCode) | loopback only | never published; browser talks only to `:9050` |
| Root-capable chat | authenticated/trusted UI only | opencode deny-patterns + full command audit log |

*(Decision: **root-powered** — no sudoers scoping.)*

---

## 9. Deployment

- **systemd user units** (linger already on): `monitord.service` (Go daemon + child `opencode serve :9060`, `Restart=always`) and `monitor-web.service` (SvelteKit `adapter-node` build), journal logging.
- **system services** (via `just setup`, existing NOPASSWD apt/tee): `dnsmasq` drop-in zone config + nginx vhost install.
- **Dev**: `just dev` → Go daemon (`backend/main`) on `127.0.0.1:9050` proxying to the Vite dev server of `frontend/monitor` (`:9065`; Vite's default `:5173` is taken by another app on this box) + `npm run dev` in `frontend/monitor` (HMR through the proxy).
- **Recipes**: `just build` (Go binary + `svelte-kit build`) · `just deploy` (copy + restart units) · `just nginx` (install vhost + `nginx -t` + reload) · `just dns` (dnsmasq zone + restart + verify dig) · `just cf-access` (idempotent Access app + ingress + DNS route + restart).
- Config: env/TOML — ports, opencode port/token, poll interval, probe targets, deny patterns.

---

## 10. Milestones

| # | Milestone | Deliverables | Blocked? |
|---|---|---|---|
| **M0** ✓ | Scaffold | monorepo layout (`backend/main`, `frontend/monitor`, `go.work`), Go module, SvelteKit app, justfile, dev proxy | no |
| **M1** ✓ | Inventory | collector + parsers, `GET /api/services`, dashboard with clickable DNS links, health dots | no |
| **M2** ✓ | Local access | hosts entry + nginx vhost → `http://monitor.local` verified (nginx → monitord → Vite dev) | no |
| **M3** ✓ | Chat dock | dedicated opencode serve, streaming, tool-call rendering, quick actions, audit log | no |
| **M4** ⏳ | Tailnet access | dnsmasq zone + nginx vhost verified on box (`dig monitor.<tailnet-domain> @<tailscale-ip>` → <tailscale-ip>, nginx routes by Host). **Pending:** Tailscale admin-console split-DNS entry (your 2-min step, §7b.1) so other tailnet machines resolve it. | 1 manual admin step |
| **M5** ✓ | Public access | tunnel ingress + DNS route + Access app via API script → `https://monitor-dev.example.com` (302 to Cloudflare Access / Google login) verified | no (all scriptable) |
| **M6** ✓ | Polish | systemd units + enable, `/audit` view, process detail drawer, `just expose <name> <port>` helper, README | no |

Everything except the single Tailscale admin-console nameserver entry (§7b.1) is executable from this machine.

---

## 11. Open questions

1. **Tailscale split-DNS step** — will you add the nameserver entry (`<tailscale-ip>` / domain `<tailnet-domain>`) in the admin console, or should I prepare exact click-by-click instructions + immediate verification? Also confirm the console accepts the **single-label** domain; if not, we fall back per §7b.
2. **"Switch port" semantics** — plan: nginx-fronted apps → edit vhost + upstream; docker → edit compose/port mapping + recreate; bare processes → edit unit/env + restart. Anything specific you had in mind (e.g. a port registry the dashboard itself owns so switching happens in one place)?
3. **LAN exposure of `:9050`** — kept open (`0.0.0.0`, simplest). Want a ufw rule restricting `:9050` to tailnet + loopback? (The named URLs work either way.)
4. **Unknown subdomains** — should `*.<tailnet-domain>` typos fall through to the stock nginx default page, or redirect to the monitor (a "gateway" vhost)?

---

## 12. Risks & notes

- **Single-label split-DNS domain** — `<tailnet-domain>` has no dot; console validation behavior is unverified (docs only show `example.com`). Step 1 of §7b is explicitly a *verify-or-fallback* moment.
- **Exit nodes skip split DNS by default** — enable "Use with exit node" on the nameserver entry if you route through one.
- **OpenCode server API** — exact auth header/SDK surface confirmed against current docs during M3; the running `--service` instance proves the pattern.
- **`/proc` of root processes** — covered by existing `NOPASSWD: ALL`.
- **`jobhunt.conf` owns the FQDN** `<tailnet-domain>.<tailnet-suffix>` → our vhosts register only `monitor.local` / `monitor.<tailnet-domain>`; FQDN keeps serving jobhunt (expected, untouched).
- **Cloudflare edge + SSE** — chat streams through Access/tunnel; Cloudflare supports streaming (nginx `proxy_read_timeout 3600s` set). Fallback: polling for chat events if a stream is cut.
- **`.local` is technically mDNS** — the box resolves `.local` via `/etc/hosts` (hosts wins), existing convention. Tailnet devices never touch `.local` — they use `*.<tailnet-domain>`.
- **dnsmasq vs systemd-resolved** — dnsmasq binds only `<tailscale-ip>:53` (`bind-interfaces`), no clash with resolved on `127.0.0.53/54:53`. It answers only the zone it owns.
- **Env vars `CF_DOMAIN`/`CF_APP_NAME`** hold the old app's values — `just cf-access` passes explicit params instead of relying on them.
