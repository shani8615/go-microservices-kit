#!/usr/bin/env bash
# Register, log in, place an order (twice, with the same Idempotency-Key),
# list orders. Then check `make logs` for the notification.
set -euo pipefail
API=${API:-http://localhost:8080/api}
EMAIL="demo+$RANDOM@example.com"
PASS="correct horse battery staple"

echo "→ register $EMAIL"
curl -fsS -X POST "$API/auth/register" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}"; echo

TOKEN=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASS\"}" | sed -E 's/.*"access_token":"([^"]+)".*/\1/')

echo "→ me"
curl -fsS "$API/auth/me" -H "Authorization: Bearer $TOKEN"; echo

KEY=$(date +%s%N)
for i in 1 2; do
  echo "→ create order (attempt $i, Idempotency-Key $KEY)"
  curl -fsS -X POST "$API/orders" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: $KEY" -d '{"sku":"MUG-GOPHER","quantity":2}' -D - -o - | grep -iE 'HTTP/|idempotent|^\{' ; echo
done

echo "→ list orders"
curl -fsS "$API/orders?limit=10" -H "Authorization: Bearer $TOKEN"; echo
