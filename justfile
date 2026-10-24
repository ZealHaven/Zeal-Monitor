set dotenv-load := true
# Monitor monorepo — see docs/plans/1-initial.md
set shell := ["bash", "-uc"]

backend_dir  := "backend/main"
frontend_dir := "frontend/monitor"
bin_dir      := "bin"
dev_addr     := "127.0.0.1:9050"
dev_proxy    := "http://127.0.0.1:9065"

default:
    @just --list

# --- dev -------------------------------------------------------------------

# Run monitord (:9050) with the Vite dev server (:9065) behind it
dev:
    #!/usr/bin/env bash
    set -euo pipefail
    ( cd {{frontend_dir}} && npm run dev ) &
    vite_pid=$!
    trap 'kill $vite_pid 2>/dev/null || true' EXIT
    cd {{backend_dir}}
    go run ./cmd/monitord --addr ${MONITORD_ADDR:-{{dev_addr}}} --proxy http://127.0.0.1:${VITE_PORT:-9065} --oc-port ${OPENCODE_PORT:-9060}

# Backend only (expects a frontend somewhere, or use --proxy)
dev-backend:
    cd {{backend_dir}} && go run ./cmd/monitord --addr ${MONITORD_ADDR:-{{dev_addr}}} --proxy http://127.0.0.1:${VITE_PORT:-9065} --oc-port ${OPENCODE_PORT:-9060}

dev-frontend:
    cd {{frontend_dir}} && npm run dev

# --- build / deploy --------------------------------------------------------

build: build-backend build-frontend

build-backend:
    mkdir -p {{bin_dir}}
    cd {{backend_dir}} && go build -o ../../{{bin_dir}}/monitord ./cmd/monitord

build-frontend:
    cd {{frontend_dir}} && npm run build

# Install systemd user units and restart both services
deploy: build
    install -d ~/.config/systemd/user
    install -m 644 ops/systemd/*.service ~/.config/systemd/user/
    systemctl --user daemon-reload
    systemctl --user restart monitord monitor-web
    systemctl --user --no-pager status monitord monitor-web || true

# Rebuild + restart only the frontend (SvelteKit) service
deploy-frontend: build-frontend
    systemctl --user restart monitor-web
    systemctl --user --no-pager status monitor-web || true

# --- system plumbing -------------------------------------------------------

# /etc/hosts entry so http://monitor.local resolves locally
hosts:
    @grep -q 'monitor.local' /etc/hosts || echo '127.0.0.1  monitor.local' | sudo tee -a /etc/hosts
    @grep -n 'monitor.local' /etc/hosts

# Expose an app on this box under a configured name/port
expose name port:
    ops/nginx/expose.sh {{name}} {{port}}
    sudo install -m 644 ops/nginx/monitor.conf /etc/nginx/sites-enabled/monitor.conf
    sudo nginx -t
    sudo systemctl reload nginx
    @echo "OK: http://monitor.local"

# Install the dnsmasq zone. Requires the Tailscale admin-console split-DNS
# entry: nameserver <tailscale-ip> for domain <tailnet-domain>.
dns:
    sudo install -m 644 ops/dnsmasq/zealhq-local.conf /etc/dnsmasq.d/<tailnet-domain>.conf
    sudo systemctl enable --now dnsmasq
    sudo systemctl restart dnsmasq
    sleep 1
    @echo "local check:   dig +short <name>.<tailnet-domain> @<tailscale-ip>"
    @dig +short <name>.<tailnet-domain> @<tailscale-ip> || true
    @echo "tailnet check: (run on another machine) dig +short <name>.<tailnet-domain> @100.100.100.100"

# Create/update the Cloudflare Access app + tunnel ingress (M5)
cf-access:
    ops/cloudflare/create-access-app.sh

# --- quality ---------------------------------------------------------------

fmt:
    cd {{backend_dir}} && gofmt -w .

check:
    cd {{backend_dir}} && go vet ./...
    cd {{frontend_dir}} && npm run check

test:
    cd {{backend_dir}} && go test ./...
