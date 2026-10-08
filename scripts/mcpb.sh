#!/usr/bin/env bash
# Build kartograf.mcpb, the MCP bundle (https://github.com/modelcontextprotocol/mcpb)
# that Claude Desktop installs in one click and the MCP registry and
# Smithery list.
#
#   scripts/mcpb.sh VERSION DIR [NAME]
#
# DIR holds the release binaries (kartograf-<os>-<arch>); the bundle is
# written to DIR/NAME (default kartograf.mcpb). MCPB can switch the
# command per OS but not per CPU, so one bundle carries every binary
# behind a small sh launcher (packaging/mcpb/kartograf). The tool list in
# manifest.json is read from the binary in DIR that runs on this
# machine, so it never drifts from the server.
#
# Env: MCPB_PLATFORMS  binaries to include (default: all four release targets;
#                      Smithery takes at most 25 MB, which fits one)
#      MCPB_CLI        mcpb command (default: pinned @anthropic-ai/mcpb via npx)
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
	echo "usage: $0 VERSION DIR [NAME]" >&2
	exit 2
fi
version=${1#v}
dir=$(cd "$2" && pwd)
name=${3:-kartograf.mcpb}
root=$(cd "$(dirname "$0")/.." && pwd)
platforms=${MCPB_PLATFORMS:-darwin-arm64 darwin-amd64 linux-amd64 linux-arm64}
mcpb=${MCPB_CLI:-npx --yes @anthropic-ai/mcpb@2.1.2}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
stage="$tmp/bundle"
mkdir -p "$stage/server" "$tmp/empty" "$tmp/home"

for p in $platforms; do
	src="$dir/kartograf-$p"
	if [ ! -f "$src" ]; then
		echo "mcpb: missing $src" >&2
		exit 1
	fi
	install -m 0755 "$src" "$stage/server/kartograf-$p"
done
install -m 0755 "$root/packaging/mcpb/kartograf" "$stage/server/kartograf"
cp "$root/assets/icon.png" "$stage/icon.png"
cp "$root/LICENSE" "$stage/LICENSE"

# Ask the server for its tools over stdio. stdin stays open for a
# moment: the server exits on EOF before it answers.
host="$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
if [ ! -f "$dir/kartograf-$host" ]; then
	echo "mcpb: no kartograf-$host in $dir to read the tool list from" >&2
	exit 1
fi
probe="$tmp/kartograf-probe"
install -m 0755 "$dir/kartograf-$host" "$probe"
tools=$(
	{
		printf '%s\n' \
			'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcpb-build","version":"0"}}}' \
			'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
			'{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
		sleep 3
	} | HOME="$tmp/home" XDG_CACHE_HOME="$tmp/home/.cache" KARTOGRAF_NO_UPDATE_CHECK=1 \
		"$probe" serve --no-index "$tmp/empty" 2>/dev/null |
		jq -s '[.[] | select(.id == 2) | .result.tools[]
			| {name, description: (.description | gsub("\\s+"; " ")
				| (capture("^(?<s>.*?[.?!])( |$)").s // .))}]'
)
if [ "$(jq length <<<"$tools")" -eq 0 ]; then
	echo "mcpb: the server listed no tools" >&2
	exit 1
fi

# A bundle without, say, Linux binaries must not offer itself on Linux.
oses=$(for p in $platforms; do echo "${p%-*}"; done | sort -u | jq -R . | jq -s .)
jq --arg v "$version" --argjson tools "$tools" --argjson oses "$oses" \
	'.version = $v | .tools = $tools | .compatibility.platforms = $oses' \
	"$root/packaging/mcpb/manifest.json" >"$stage/manifest.json"

$mcpb validate "$stage/manifest.json"
rm -f "$dir/$name"
$mcpb pack "$stage" "$dir/$name"
