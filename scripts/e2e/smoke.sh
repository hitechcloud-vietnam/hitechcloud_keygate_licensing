#!/usr/bin/env bash
# E2E smoke test against a running HiTechCloud instance (plan §99/§109).
#
# Usage (bash):
#   ./scripts/e2e/smoke.sh                              # defaults to http://localhost:9000
#   ./scripts/e2e/smoke.sh https://lic.example.com
#   BASE_URL=https://lic.example.com ./scripts/e2e/smoke.sh
#
# Checks: /health, /api/v1/config, /api/v1/site-config, setup status,
# marketplace list, and the 404 envelope shape. Exits non-zero when any
# check fails. Requires curl; uses python3 for JSON parsing when available,
# falls back to grep otherwise.

set -u

BASE_URL="${1:-${BASE_URL:-http://localhost:9000}}"
PASSED=0
FAILED=0

pass() { PASSED=$((PASSED + 1)); printf 'PASS  %s\n' "$1"; }
fail() { FAILED=$((FAILED + 1)); printf 'FAIL  %s  %s\n' "$1" "${2:-}"; }

# has_json <json> <python-expr-on-d> — true when the expression holds.
has_json() {
  local json="$1" expr="$2"
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "$json" | python3 -c "import json,sys; d=json.load(sys.stdin); sys.exit(0 if ($expr) else 1)" 2>/dev/null
  else
    # crude fallback: grep for the literal fragment
    printf '%s' "$json" | grep -qiF "$3"
  fi
}

echo "E2E smoke against $BASE_URL"
echo "------------------------------------------------------------"

# 1. /health — liveness + DB check.
code=$(curl -s -o /tmp/kg_health.json -w '%{http_code}' "$BASE_URL/health")
if [ "$code" = "200" ]; then
  pass "GET /health returns 200"
  body=$(cat /tmp/kg_health.json)
  if has_json "$body" "d.get('status') == 'ok'" '"status": "ok"'; then
    pass "GET /health status is ok"
  else
    fail "GET /health status is ok" "body: $body"
  fi
  if has_json "$body" "isinstance(d.get('checks'), dict)" '"checks"'; then
    pass "GET /health reports database check"
  else
    fail "GET /health reports database check"
  fi
else
  fail "GET /health returns 200" "got $code"
fi

# 2. /api/v1/config — public config + attribution (AGPL §7(b) fields).
code=$(curl -s -o /tmp/kg_config.json -w '%{http_code}' "$BASE_URL/api/v1/config")
if [ "$code" = "200" ]; then
  pass "GET /api/v1/config returns 200"
  body=$(cat /tmp/kg_config.json)
  if has_json "$body" "d.get('success') is True" '"success": true'; then
    pass "config envelope success=true"
  else
    fail "config envelope success=true" "body: $body"
  fi
  if has_json "$body" "bool(d.get('data', {}).get('attribution_text'))" '"attribution_text"'; then
    pass "config carries attribution_text"
  else
    fail "config carries attribution_text"
  fi
  if has_json "$body" "bool(d.get('data', {}).get('attribution_url'))" '"attribution_url"'; then
    pass "config carries attribution_url"
  else
    fail "config carries attribution_url"
  fi
else
  fail "GET /api/v1/config returns 200" "got $code"
fi

# 3. /api/v1/site-config — surface/branding config.
code=$(curl -s -o /tmp/kg_site.json -w '%{http_code}' "$BASE_URL/api/v1/site-config")
if [ "$code" = "200" ]; then
  pass "GET /api/v1/site-config returns 200"
  if has_json "$(cat /tmp/kg_site.json)" "d.get('success') is True" '"success": true'; then
    pass "site-config envelope success=true"
  else
    fail "site-config envelope success=true"
  fi
else
  fail "GET /api/v1/site-config returns 200" "got $code"
fi

# 4. Setup status — always answers with the wizard state.
code=$(curl -s -o /tmp/kg_setup.json -w '%{http_code}' "$BASE_URL/api/v1/setup/status")
if [ "$code" = "200" ]; then
  pass "GET /api/v1/setup/status returns 200"
  if has_json "$(cat /tmp/kg_setup.json)" "'needed' in d.get('data', {})" '"needed"'; then
    pass "setup status has needed flag"
  else
    fail "setup status has needed flag"
  fi
else
  fail "GET /api/v1/setup/status returns 200" "got $code"
fi

# 5. Marketplace list — public catalog answers with the envelope.
code=$(curl -s -o /tmp/kg_market.json -w '%{http_code}' "$BASE_URL/api/v1/marketplace/products?limit=5")
if [ "$code" = "200" ]; then
  pass "GET /api/v1/marketplace/products returns 200"
  if has_json "$(cat /tmp/kg_market.json)" "d.get('success') is True" '"success": true'; then
    pass "marketplace envelope success=true"
  else
    fail "marketplace envelope success=true"
  fi
else
  fail "GET /api/v1/marketplace/products returns 200" "got $code"
fi

# 6. 404 shape — unknown API path must answer the JSON error envelope,
#    not gin's plain-text 404 (contract: {"success":false,"error":{...}}).
code=$(curl -s -o /tmp/kg_404.json -w '%{http_code}' "$BASE_URL/api/v1/definitely-not-a-route-$RANDOM$RANDOM")
if [ "$code" = "404" ]; then
  pass "unknown API path returns 404"
else
  fail "unknown API path returns 404" "got $code"
fi
body=$(cat /tmp/kg_404.json)
if has_json "$body" "d.get('success') is False and isinstance(d.get('error'), dict)" '"error": {'; then
  pass "404 uses error envelope"
else
  fail "404 uses error envelope" "body: $body"
fi
if has_json "$body" "d.get('error', {}).get('code') == 'NOT_FOUND'" '"NOT_FOUND"'; then
  pass "404 error code is NOT_FOUND"
else
  fail "404 error code is NOT_FOUND"
fi

echo "------------------------------------------------------------"
echo "passed: $PASSED  failed: $FAILED"
[ "$FAILED" -gt 0 ] && exit 1
exit 0
