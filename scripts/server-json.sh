#!/usr/bin/env bash
# Point server.json (the MCP registry entry) at one release's bundle.
#
#   scripts/server-json.sh VERSION [MCPB] [OUT]
#
# VERSION is the release tag (v0.1.N). The SHA-256 comes from MCPB when
# given, otherwise from kartograf.mcpb downloaded from that GitHub
# release. OUT defaults to server.json at the repository root; the
# release workflow runs the same step and attaches the result.
set -euo pipefail

if [ $# -lt 1 ] || [ $# -gt 3 ]; then
	echo "usage: $0 VERSION [MCPB] [OUT]" >&2
	exit 2
fi
root=$(cd "$(dirname "$0")/.." && pwd)
tag="v${1#v}"
bundle=${2:-}
out=${3:-$root/server.json}
url="https://github.com/dev-manul/kartograf/releases/download/$tag/kartograf.mcpb"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
if [ -z "$bundle" ]; then
	bundle="$tmp/kartograf.mcpb"
	curl -fsSL -o "$bundle" "$url"
fi
if command -v sha256sum >/dev/null; then
	sum=$(sha256sum "$bundle" | cut -d' ' -f1)
else
	sum=$(shasum -a 256 "$bundle" | cut -d' ' -f1)
fi

jq --arg v "${tag#v}" --arg url "$url" --arg sum "$sum" \
	'.version = $v | .packages[0].identifier = $url | .packages[0].fileSha256 = $sum' \
	"$root/server.json" >"$tmp/server.json"
mv "$tmp/server.json" "$out"
echo "$out: version ${tag#v}, sha256 $sum"
