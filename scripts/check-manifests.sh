#!/bin/sh
set -eu

go test ./internal/deployment -count=1

ticket_key=$(mktemp)
trap 'rm -f "$ticket_key"' EXIT
dd if=/dev/zero of="$ticket_key" bs=32 count=1 2>/dev/null
./scripts/check-ticket-key.sh "$ticket_key"
truncate -s 31 "$ticket_key"
if ./scripts/check-ticket-key.sh "$ticket_key" >/dev/null 2>&1; then
  echo "ticket key validator accepted a non-32-byte file" >&2
  exit 1
fi

