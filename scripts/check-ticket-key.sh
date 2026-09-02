#!/bin/sh
set -eu

if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
  echo "usage: $0 <ticket-key-file>" >&2
  exit 2
fi

bytes=$(wc -c < "$1" | tr -d ' ')
if [ "$bytes" != "32" ]; then
  echo "ticket key must contain exactly 32 raw bytes; got $bytes" >&2
  exit 1
fi

echo "ticket key length: 32 raw bytes"
