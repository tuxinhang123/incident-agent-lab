#!/usr/bin/env bash
# Local fallback for environments without Docker: starts redis-server and
# demo-api directly, seeds the demo key, and writes PIDs to .run/.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="$ROOT/.run"
REDIS_PORT="${REDIS_PORT:-6379}"
HTTP_PORT="${HTTP_PORT:-8080}"

mkdir -p "$RUN_DIR"

# Never take over an already-served redis port: if an external redis is running
# on REDIS_PORT, our daemonized child would fail to bind while ping/SET still
# hit the external instance, and failure cleanup would shut that external
# redis down with NOSAVE, losing its data.
if redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1; then
  echo "redis port $REDIS_PORT already in use; stop it or set REDIS_PORT" >&2
  exit 1
fi

# started_redis is set only after this invocation proves ownership of the redis
# on REDIS_PORT: the port was free before start, our pidfile was generated and
# its PID is alive. Cleanup may touch that redis only.
started_redis=0

redis-server --daemonize yes --port "$REDIS_PORT" \
  --dir "$RUN_DIR" --logfile "$RUN_DIR/redis.log" --pidfile "$RUN_DIR/redis.pid" \
  --save "" --appendonly no

redis_ready=0
for _ in $(seq 1 50); do
  if redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1; then
    redis_ready=1
    break
  fi
  sleep 0.1
done

if [ "$redis_ready" -ne 1 ]; then
  echo "redis failed to become ready on port $REDIS_PORT" >&2
  # The port was free before we daemonized, so a live pid from our pidfile is
  # our own instance: reap it instead of leaving an untracked daemon behind.
  if [ -f "$RUN_DIR/redis.pid" ]; then
    redis_pid="$(cat "$RUN_DIR/redis.pid")"
    if kill -0 "$redis_pid" 2>/dev/null; then
      kill -TERM "$redis_pid" 2>/dev/null || true
      for _ in $(seq 1 30); do
        kill -0 "$redis_pid" 2>/dev/null || break
        sleep 0.1
      done
      kill -KILL "$redis_pid" 2>/dev/null || true
    fi
    rm -f "$RUN_DIR/redis.pid"
  fi
  exit 1
fi
if [ ! -f "$RUN_DIR/redis.pid" ]; then
  echo "redis answered ping but pidfile $RUN_DIR/redis.pid is missing; not touching port $REDIS_PORT" >&2
  exit 1
fi
redis_pid="$(cat "$RUN_DIR/redis.pid")"
if ! kill -0 "$redis_pid" 2>/dev/null; then
  echo "redis pidfile $RUN_DIR/redis.pid has no live pid; not touching port $REDIS_PORT" >&2
  rm -f "$RUN_DIR/redis.pid"
  exit 1
fi
started_redis=1

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
  # Only stop the redis this invocation proved it owns (pidfile-tracked);
  # never issue a port-wide `redis-cli shutdown nosave` that could kill an
  # external redis and drop its data.
  if [ "$started_redis" -eq 1 ] && [ -f "$RUN_DIR/redis.pid" ]; then
    redis_pid="$(cat "$RUN_DIR/redis.pid")"
    kill -TERM "$redis_pid" 2>/dev/null || true
    for _ in $(seq 1 30); do
      kill -0 "$redis_pid" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL "$redis_pid" 2>/dev/null || true
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
