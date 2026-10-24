#!/bin/sh
set -eu
snapshot="$(mktemp -d)"
trap 'rm -rf "$snapshot"' EXIT INT TERM
if [ -d api/gen ]; then cp -R api/gen "$snapshot/gen"; else mkdir "$snapshot/gen"; fi
sh ./scripts/proto.sh generate
diff -ru "$snapshot/gen" api/gen
