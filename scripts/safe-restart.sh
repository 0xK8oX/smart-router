#!/bin/bash
# safe-restart.sh — restart smart-router without dropping in-flight requests.
#
# Strategy:
#   1. Build the new binary FIRST. If build fails, the running service is untouched.
#   2. Run PM2 reload/restart. With kill_timeout=135s (ecosystem.config.js),
#      PM2 waits up to 135s for graceful shutdown before SIGKILL.
#   3. Poll the health endpoint until it responds.
#   4. Optionally reset provider health (pass "--reset-all" or provider names).
#
# Usage:
#   ./scripts/safe-restart.sh
#   ./scripts/safe-restart.sh --reset-all
#   ./scripts/safe-restart.sh jason-kimi zaipu

set -euo pipefail

cd "$(dirname "$0")/.."

PORT="${SMART_ROUTER_PORT:-8790}"
HOST="${SMART_ROUTER_HOST:-127.0.0.1}"
HEALTH_URL="http://${HOST}:${PORT}/v1/health"
BUILD_TIMEOUT=60
START_TIMEOUT=30

# ─── 1. Build first — do NOT touch the running service until we know it compiles ───
echo "[safe-restart] Building binary..."
if ! go build -o smart-router-new .; then
    echo "[safe-restart] ❌ Build failed — leaving running service untouched"
    rm -f smart-router-new
    exit 1
fi

# Atomically swap binaries
mv smart-router smart-router-old 2>/dev/null || true
mv smart-router-new smart-router
echo "[safe-restart] ✅ Build OK, binary swapped"

# ─── 2. Restart via PM2 — force re-read ecosystem.config.js for kill_timeout ───
echo "[safe-restart] Restarting via PM2..."
pm2 startOrRestart ecosystem.config.js --update-env

# ─── 3. Wait for health endpoint ───
echo "[safe-restart] Waiting for health endpoint (${HEALTH_URL})..."
for i in $(seq 1 $START_TIMEOUT); do
    if curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
        echo "[safe-restart] ✅ Service responding after ${i}s"
        break
    fi
    sleep 1
done

if ! curl -sf "$HEALTH_URL" >/dev/null 2>&1; then
    echo "[safe-restart] ⚠️  Health check not responding after ${START_TIMEOUT}s"
    echo "[safe-restart] Check: pm2 logs smart-router"
    exit 1
fi

# ─── 4. Reset provider health if requested ───
if [ $# -gt 0 ]; then
    if [ "$1" == "--reset-all" ]; then
        echo "[safe-restart] Resetting all unhealthy providers..."
        go run ./cmd/reset/main.go 2>/dev/null || true
    else
        for provider in "$@"; do
            echo "[safe-restart] Resetting $provider..."
            go run ./cmd/reset/main.go "$provider" 2>/dev/null || true
        done
    fi
fi

# ─── 5. Cleanup old binary ───
rm -f smart-router-old

echo "[safe-restart] ✅ Restart complete. Service healthy."
