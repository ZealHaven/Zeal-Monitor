#!/usr/bin/env bash
# Expose an app on this machine to the tailnet.
# Usage: ops/nginx/expose.sh <name> <port>
# Requires the tailnet domain(s) to resolve to this box (see ops/dnsmasq).
set -euo pipefail

name="${1:?usage: expose.sh <name> <port>}"
port="${2:?usage: expose.sh <name> <port>}"

# Validate inputs to avoid injection into nginx config / filenames.
if ! [[ "$name" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
    echo "invalid name: $name" >&2
    exit 2
fi
if ! [[ "$port" =~ ^[0-9]+$ ]] || (( port < 1 || port > 65535 )); then
    echo "invalid port: $port" >&2
    exit 2
fi

# Configurable tailnet domains (space-separated). Defaults to a local-only name.
domains="${TAILNET_DOMAINS:-local}"
conf="/etc/nginx/sites-enabled/${name}.conf"

server_names=""
for d in $domains; do
    server_names="${server_names} ${name}.${d}"
done

sudo tee "$conf" >/dev/null <<NGINX
server {
    listen 80;
    server_name${server_names};
    location / {
        proxy_pass http://127.0.0.1:${port};
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 3600s;
    }
}
NGINX
sudo nginx -t
sudo systemctl reload nginx
echo "exposed:${server_names} (-> 127.0.0.1:${port})"
