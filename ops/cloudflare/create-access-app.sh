#!/usr/bin/env bash
# Create (or update) the Cloudflare Access application for monitor-dev.example.com,
# add the tunnel ingress rule, and create the DNS route. Idempotent.
#
# Requires in env: CF_ACCOUNT_ID, CF_API_TOKEN, CF_ACCESS_EMAIL
# Optional: CF_DOMAIN (default monitor-dev.example.com), CF_APP_NAME
set -euo pipefail

: "${CF_ACCOUNT_ID:?CF_ACCOUNT_ID required}"
: "${CF_API_TOKEN:?CF_API_TOKEN required}"
: "${CF_ACCESS_EMAIL:?CF_ACCESS_EMAIL required}"

DOMAIN="${MONITOR_DOMAIN:-monitor-dev.example.com}"
APP_NAME="${CF_APP_NAME:-Monitor Dev Access}"
GOOGLE_IDP="7dbf5e99-900e-4613-be14-166b27ca0fe4"
API="https://api.cloudflare.com/client/v4/accounts/${CF_ACCOUNT_ID}/access"
AUTH="Authorization: Bearer ${CF_API_TOKEN}"

echo "==> 1/4  tunnel DNS route"
cloudflared tunnel route dns zeal-pc "$DOMAIN" 2>&1 | tail -2 || true

echo "==> 2/4  tunnel ingress (insert before catch-all if missing)"
python3 - "$DOMAIN" <<'PY'
import sys
domain = sys.argv[1]
path = "/etc/cloudflared/config.yml"
s = open(path).read()
rule = f"  - hostname: {domain}\n    service: http://127.0.0.1:9050\n"
if f"hostname: {domain}" in s:
    print("   ingress rule already present")
else:
    marker = "  - service: http_status:404"
    assert marker in s, "catch-all not found"
    s = s.replace(marker, rule + marker, 1)
    # write via sudo tee
    import subprocess
    subprocess.run(["sudo","tee",path], input=s.encode(), stdout=subprocess.DEVNULL, check=True)
    print("   inserted ingress rule")
PY

echo "==> 3/4  Cloudflare Access application"
APP_ID=$(python3 - "$DOMAIN" "$APP_NAME" "$GOOGLE_IDP" "$CF_ACCESS_EMAIL" "$API" "$CF_API_TOKEN" <<'PY'
import json, sys, urllib.request, urllib.error
domain, app_name, idp, email, api, token = sys.argv[1:]
headers={"Authorization":f"Bearer {token}","Content-Type":"application/json"}
# find existing app by domain
apps=json.load(urllib.request.urlopen(urllib.request.Request(f"{api}/apps",headers={"Authorization":f"Bearer {token}"})))["result"]
app=next((a for a in apps if a.get("domain")==domain), None)
if app is None:
    body={"name":app_name,"domain":domain,"type":"self_hosted","session_duration":"24h",
          "allowed_idps":[idp],"auto_redirect_to_identity":True,"app_launcher_visible":True}
    req=urllib.request.Request(f"{api}/apps",data=json.dumps(body).encode(),headers=headers,method="POST")
    with urllib.request.urlopen(req) as r:
        app=json.load(r)["result"]
    print("created app", app["id"], file=__import__("sys").stderr)
# policy
pol_name="Only "+email
pol=None
# list policies for this app
try:
    pols=json.load(urllib.request.urlopen(urllib.request.Request(f"{api}/apps/{app['id']}/policies",headers={"Authorization":f"Bearer {token}"})))["result"]
    pol=next((p for p in (pols or []) if p.get("name")==pol_name), None)
except Exception:
    pols=None
if pol is None:
    body={"name":pol_name,"decision":"allow","include":[{"email":{"email":email}}]}
    req=urllib.request.Request(f"{api}/policies",data=json.dumps(body).encode(),headers=headers,method="POST")
    with urllib.request.urlopen(req) as r:
        pol=json.load(r)["result"]
    print("created policy", pol["id"], file=__import__("sys").stderr)
# attach policy to app (PUT)
body={"name":app["name"],"domain":app["domain"],"type":app["type"],"session_duration":app["session_duration"],
      "allowed_idps":app["allowed_idps"],"auto_redirect_to_identity":app["auto_redirect_to_identity"],
      "app_launcher_visible":app["app_launcher_visible"],"policies":[{"id":pol["id"]}]}
req=urllib.request.Request(f"{api}/apps/{app['id']}",data=json.dumps(body).encode(),headers=headers,method="PUT")
with urllib.request.urlopen(req) as r:
    json.load(r)
print("attached policy", pol["id"], file=__import__("sys").stderr)
print(app["id"])
PY
)
echo "   app id: $APP_ID"

echo "==> 4/4  restart cloudflared"
sudo systemctl restart cloudflared
sleep 2
sudo systemctl is-active cloudflared && echo "cloudflared active"

echo "==> verify"
curl -sI "https://${DOMAIN}" | head -5