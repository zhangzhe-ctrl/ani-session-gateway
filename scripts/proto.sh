#!/bin/sh
set -eu
mode="${1:-generate}"
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"
dependency="$(go list -m -f '{{.Dir}}' github.com/zhangzhe-ctrl/ani-resource-service/api/vm/console)"
test -f "$dependency/v1/console.proto"
workspace="$(mktemp -d)"
trap 'rm -rf "$workspace"' EXIT INT TERM
mkdir -p "$workspace/vm/console/v1"
cp -R api/proto/session "$workspace/"
cp "$dependency/v1/console.proto" "$workspace/vm/console/v1/console.proto"
cp buf.yaml "$workspace/buf.yaml"
# The dependency source is read from its Go module. It is never copied into
# the repository, generated as Session Gateway code, or kept as a second API.
sed -i 's#path: api/proto#path: .#' "$workspace/buf.yaml"
case "$mode" in
  generate)
    buf generate "$workspace" --template "$root/buf.gen.yaml" --path "$workspace/session/v1/session.proto"
    ;;
  lint)
    buf lint "$workspace" --path "$workspace/session/v1/session.proto"
    ;;
  *) echo 'usage: scripts/proto.sh [generate|lint]' >&2; exit 2 ;;
esac
