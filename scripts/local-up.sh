#!/usr/bin/env bash
# Local fallback for environments without Docker: starts redis-server and
# demo-api directly, seeds the demo key, and writes PIDs to .run/.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="$ROOT/.run"
REDIS_PORT="${REDIS_PORT:-6379}"
HTTP_PORT="${HTTP_PORT:-8080}"

mkdir -p "$RUN_DIR"

redis-server --daemonize yes --port "$REDIS_PORT" \
  --dir "$RUN_DIR" --logfile "$RUN_DIR/redis.log" --pidfile "$RUN_DIR/redis.pid" \
  --save "" --appendonly no

for _ in $(seq 1 50); do
  redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1 && break
  sleep 0.1
done
redis-cli -p "$REDIS_PORT" SET demo:greeting "hello from incident-agent-lab" >/dev/null

REDIS_ADDR="127.0.0.1:$REDIS_PORT" HTTP_ADDR=":$HTTP_PORT" \
  "$ROOT/bin/demo-api" >"$RUN_DIR/demo-api.log" 2>&1 &
echo $! > "$RUN_DIR/demo-api.pid"

echo "local stack up: demo-api on :$HTTP_PORT, redis on :$REDIS_PORT"
