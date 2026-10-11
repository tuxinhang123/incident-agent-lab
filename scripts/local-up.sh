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

fail_demo_api() {
  echo "demo-api failed to become ready on http://127.0.0.1:$HTTP_PORT/health" >&2
  if [ -f "$RUN_DIR/demo-api.log" ]; then
    tail -n 20 "$RUN_DIR/demo-api.log" >&2 || true
  fi
  if [ -f "$RUN_DIR/demo-api.pid" ]; then
    api_pid="$(cat "$RUN_DIR/demo-api.pid")"
    kill "$api_pid" 2>/dev/null || true
    rm -f "$RUN_DIR/demo-api.pid"
  fi
  redis-cli -p "$REDIS_PORT" shutdown nosave >/dev/null 2>&1 || true
  if [ -f "$RUN_DIR/redis.pid" ]; then
    redis_pid="$(cat "$RUN_DIR/redis.pid")"
    kill "$redis_pid" 2>/dev/null || true
    rm -f "$RUN_DIR/redis.pid"
  fi
  exit 1
}

ready=0
for _ in $(seq 1 50); do
  api_pid="$(cat "$RUN_DIR/demo-api.pid")"
  if ! kill -0 "$api_pid" 2>/dev/null; then
    fail_demo_api
  fi
  http_code="$(curl --max-time 1 -s -o /dev/null -w '%{http_code}' \
    "http://127.0.0.1:$HTTP_PORT/health" || true)"
  if [ "$http_code" = "200" ]; then
    ready=1
    break
  fi
  sleep 0.1
done

if [ "$ready" -ne 1 ]; then
  fail_demo_api
fi

echo "local stack up: demo-api on :$HTTP_PORT, redis on :$REDIS_PORT"
