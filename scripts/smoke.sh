#!/usr/bin/env bash
set -euo pipefail

BASE="${CINDY_BASE_URL:-http://127.0.0.1:19828}"
EMAIL="${CINDY_SMOKE_EMAIL:-smoke-$(date +%s)@cindy.local}"
CODE="${CINDY_DEV_FIXED_CODE:-123456}"

pass() { printf '  \033[32mOK\033[0m  %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; exit 1; }

echo "Cindy smoke test -> $BASE"

curl -fsS "$BASE/healthz" >/dev/null && pass "healthz"
curl -fsS "$BASE/readyz" >/dev/null && pass "readyz"

PROVIDERS=$(curl -fsS "$BASE/api/auth/providers")
echo "$PROVIDERS" | grep -q '"region"' && pass "auth providers"

curl -fsS -X POST "$BASE/api/auth/email/request-code" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\"}" >/dev/null && pass "request code"

LOGIN=$(curl -fsS -X POST "$BASE/api/auth/email/verify-code" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"code\":\"$CODE\",\"deviceId\":\"smoke-device\",\"clientType\":\"desktop\"}")
TOKEN=$(echo "$LOGIN" | python3 -c 'import json,sys; print(json.load(sys.stdin)["accessToken"])')
[ -n "$TOKEN" ] && pass "email login"

curl -fsS "$BASE/api/me" -H "Authorization: Bearer $TOKEN" | grep -q '"membership"' && pass "me"

curl -fsS "$BASE/api/model-catalog/catalog?registrySchemaVersion=5" | grep -q '"providers"' && pass "model catalog"

curl -fsS "$BASE/api/model-access/models?schemaVersion=5" -H "Authorization: Bearer $TOKEN" | grep -q '"models"' && pass "model access"

curl -fsS "$BASE/v1/me" -H "Authorization: Bearer $TOKEN" | grep -q '"actor"' && pass "chat me"

curl -fsS "$BASE/v1/conversations" -H "Authorization: Bearer $TOKEN" | grep -q '\[' && pass "chat conversations"

curl -fsS "$BASE/heartbeat" -X POST -H 'Content-Type: application/json' -d '{"uid":"00000000-0000-0000-0000-000000000000"}' | grep -q '"ok"' && pass "heartbeat"

echo "Smoke test passed."
