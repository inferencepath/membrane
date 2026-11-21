#!/usr/bin/env bash
# Smoke every Community data-plane route against a running Compose deployment.
# A missing Caller Key must produce 401 from the operation handler; 404 would
# mean the route was not mounted. No provider is contacted.
set -euo pipefail

BASE="${BASE:-http://127.0.0.1:8080}"
routes=(
  /v1/chat
  /v1/embeddings
  /v1/rerank
  /v1/responses
  /v1/images/generations
  /v1/audio/transcriptions
  /v1/audio/speech
  /v1/audio/conversations
  /v1/moderations
  /v1/multimodal/chat
)

for route in "${routes[@]}"; do
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}${route}")"
  if [[ "$status" != "401" ]]; then
    echo "smoke-operations: FAIL: ${route} returned ${status}, want 401"
    exit 1
  fi
  echo "smoke-operations: ${route} OK"
done

echo "smoke-operations: PASS"
