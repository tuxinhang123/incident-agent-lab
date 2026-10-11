#!/usr/bin/env bash
# End-to-end verification of the day-1 failure loop without Docker:
# healthy -> stop redis -> /ready 503 & /api/data 500 with structured
# error log -> restart redis -> healthy again.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="$ROOT/.run"
REDIS_PORT="${REDIS_PORT:-6379}"
HTTP_PORT="${HTTP_PORT:-8080}"
BASE="http://127.0.0.1:$HTTP_PORT"
PASS=0
FAIL=0

check() {
  local name="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "PASS: $name (HTTP $actual)"
    PASS=$((PASS + 1))
  else
    echo "FAIL: $name (want $expected, got $actual)"
    FAIL=$((FAIL + 1))
  fi
}

code() { curl -s -o /dev/null -w "%{http_code}" --max-time 3 "$1"; }

start_redis() {
  redis-server --daemonize yes --port "$REDIS_PORT" \
    --dir "$RUN_DIR" --logfile "$RUN_DIR/redis.log" --pidfile "$RUN_DIR/redis.pid" \
    --save "" --appendonly no
  for _ in $(seq 1 50); do
    redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  echo "redis did not become ready" >&2
  return 1
}

stop_redis() {
  local pid
  pid="$(cat "$RUN_DIR/redis.pid")"
  kill "$pid"
  for _ in $(seq 1 50); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  kill -9 "$pid" || true
}

cleanup() {
  bash "$ROOT/scripts/local-down.sh" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cd "$ROOT"
mkdir -p "$RUN_DIR"
make build >/dev/null
bash scripts/local-up.sh >/dev/null

check "health when healthy" 200 "$(code "$BASE/health")"
check "ready when healthy" 200 "$(code "$BASE/ready")"
check "data when healthy" 200 "$(code "$BASE/api/data")"

echo "--- injecting fault: stop redis ---"
stop_redis
sleep 0.5

check "ready when redis down" 503 "$(code "$BASE/ready")"
check "data when redis down" 500 "$(code "$BASE/api/data")"
check "health process still alive" 200 "$(code "$BASE/health")"

sleep 0.5
if grep -q '"operation": *"redis_get"' "$RUN_DIR/demo-api.log" && \
   grep -q '"error_type": *"connection_refused"' "$RUN_DIR/demo-api.log"; then
  echo "PASS: structured redis_get/connection_refused error logged"
  PASS=$((PASS + 1))
else
  echo "FAIL: expected structured connection_refused log entry"
  FAIL=$((FAIL + 1))
fi

echo "--- recovering: restart redis ---"
start_redis
redis-cli -p "$REDIS_PORT" SET demo:greeting "hello from incident-agent-lab" >/dev/null
sleep 0.5

check "ready after recovery" 200 "$(code "$BASE/ready")"
check "data after recovery" 200 "$(code "$BASE/api/data")"

echo "---"
echo "result: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
