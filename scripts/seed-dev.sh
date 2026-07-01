#!/usr/bin/env bash
# Seed the dev Panel (in-memory store) with a node, the Palworld spec, and a
# running demo server. Idempotent enough for repeated dev restarts: it tolerates
# "already exists" responses. Requires the Panel on :8080 and the Agent on :9090.
set -uo pipefail

PANEL="${PANEL_URL:-http://localhost:8080}"
ADMIN_USER="${KRAKEN_BOOTSTRAP_ADMIN_USER:-admin}"
ADMIN_PASS="${KRAKEN_BOOTSTRAP_ADMIN_PASSWORD:-admin}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

jqget() { python -c "import sys,json;print(json.load(sys.stdin).get('$1',''))" 2>/dev/null; }

echo "→ login as $ADMIN_USER"
TOKEN=$(curl -s -X POST "$PANEL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" | jqget token)
if [ -z "$TOKEN" ]; then echo "  ✗ login failed"; exit 1; fi
AUTH="Authorization: Bearer $TOKEN"

echo "→ register node abyss-node-01"
curl -s -X POST "$PANEL/api/v1/nodes" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"name":"abyss-node-01","os":"linux","wine_enabled":true,"address":"127.0.0.1:9090","total_memory_mb":16384,"port_start":28000,"port_end":28100}' \
  >/dev/null

echo "→ ping node (brings it online + auto-fills public_host)"
NODE_ID=$(curl -s "$PANEL/api/v1/nodes" -H "$AUTH" | \
  python -c "import sys,json;n=json.load(sys.stdin).get('nodes') or [];print(n[0]['id'] if n else '')")
[ -n "$NODE_ID" ] && curl -s "$PANEL/api/v1/nodes/$NODE_ID/info" -H "$AUTH" >/dev/null

echo "→ upload Palworld spec"
SPEC_ID=$(curl -s -X POST "$PANEL/api/v1/specs" -H "$AUTH" -H 'Content-Type: application/yaml' \
  --data-binary @"$ROOT/specs/palworld.yaml" | jqget id)
if [ -z "$SPEC_ID" ]; then
  # Already present — find it by slug.
  SPEC_ID=$(curl -s "$PANEL/api/v1/specs" -H "$AUTH" | \
    python -c "import sys,json;print(next((s['id'] for s in json.load(sys.stdin).get('specs') or [] if s.get('slug')=='palworld'),''))")
fi
echo "  spec_id=$SPEC_ID"

echo "→ create server leviathan-01"
SERVER_ID=$(curl -s -X POST "$PANEL/api/v1/servers" -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"spec_id\":\"$SPEC_ID\",\"name\":\"leviathan-01\"}" | jqget id)
if [ -z "$SERVER_ID" ]; then
  SERVER_ID=$(curl -s "$PANEL/api/v1/servers" -H "$AUTH" | \
    python -c "import sys,json;print(next((s['id'] for s in json.load(sys.stdin).get('servers') or [] if s.get('name')=='leviathan-01'),''))")
fi
echo "  server_id=$SERVER_ID"

echo "→ waiting for install to finish…"
for i in $(seq 1 30); do
  STATE=$(curl -s "$PANEL/api/v1/servers/$SERVER_ID" -H "$AUTH" | jqget state)
  echo "  state=$STATE"
  if [ "$STATE" = "offline" ] || [ "$STATE" = "running" ]; then break; fi
  if [ "$STATE" = "crashed" ]; then echo "  ✗ install crashed"; break; fi
  sleep 2
done

echo "→ starting server"
curl -s -X POST "$PANEL/api/v1/servers/$SERVER_ID/power" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"action":"start"}' >/dev/null

echo "✓ seed complete. server_id=$SERVER_ID"
