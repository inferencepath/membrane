#!/usr/bin/env bash
# Release smoke test: a freshly built binary must start in managed mode against a
# seeded embedded store, serve its operability endpoints, report READY, and shut
# down gracefully. No real Provider is contacted; only the local operability
# surface (/healthz, /readyz, /version) is probed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${BIN:-$ROOT/bin/membrane}"
SEED="${SEED:-$ROOT/config/seed.example.yaml}"
ADDR="${ADDR:-127.0.0.1:8080}"
BASE="http://${ADDR}"

if [[ ! -x "$BIN" ]]; then
  echo "smoke: building $BIN"
  (cd "$ROOT" && make build >/dev/null)
fi

# Isolated managed state dir + a bootstrap config pointing at it. The store is
# seeded (MEMBRANE_SEED) so the daemon becomes READY without any manual step.
WORK="$(mktemp -d)"
CONFIG="$WORK/membrane.yaml"
cat >"$CONFIG" <<EOF
server:
  address: "${ADDR}"
  shutdown_timeout: "10s"
data_dir: "${WORK}/data"
console:
  enabled: false
EOF

# Credentials the seed references must resolve at startup (fail-closed). They are
# never used because we contact no Provider.
export OPENAI_API_KEY="${OPENAI_API_KEY:-smoke-placeholder}"
export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-smoke-placeholder}"
export MEMBRANE_SEED="$SEED"

echo "smoke: starting Membrane (config=$CONFIG seed=$SEED addr=$ADDR)"
"$BIN" --config "$CONFIG" >/tmp/ip-smoke.log 2>&1 &
PID=$!

cleanup() {
  if kill -0 "$PID" 2>/dev/null; then
    kill -TERM "$PID" 2>/dev/null || true
    # Graceful shutdown must complete within the configured timeout.
    for _ in $(seq 1 50); do
      kill -0 "$PID" 2>/dev/null || break
      sleep 0.2
    done
    if kill -0 "$PID" 2>/dev/null; then
      echo "smoke: FAIL: Membrane did not shut down gracefully"
      kill -KILL "$PID" 2>/dev/null || true
      exit 1
    fi
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# Wait for /healthz to come up.
ready=""
for _ in $(seq 1 50); do
  if curl -fsS "${BASE}/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
done
if [[ -z "$ready" ]]; then
  echo "smoke: FAIL: /healthz never responded"; cat /tmp/ip-smoke.log; exit 1
fi
echo "smoke: /healthz OK"

# Readiness: the seeded store must yield a ready daemon.
ready=""
for _ in $(seq 1 50); do
  if curl -fsS "${BASE}/readyz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.2
done
if [[ -z "$ready" ]]; then
  echo "smoke: FAIL: /readyz never reported ready (seed not applied?)"; cat /tmp/ip-smoke.log; exit 1
fi
echo "smoke: /readyz OK"

version="$(curl -fsS "${BASE}/version")" && echo "smoke: /version -> ${version}"

echo "smoke: PASS"
