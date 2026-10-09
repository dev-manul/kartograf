# Publishing kartograf to MCP catalogs

How kartograf gets into the MCP registry and the catalogs that list MCP
servers. Most of it is one-time setup; after that every release keeps
the listings current on its own.

## What every release carries

The release workflow (`.github/workflows/release.yml`) runs on every
push to master. Besides the four binaries it attaches:

| Asset | What it is for |
|---|---|
| `kartograf.mcpb` | [MCP bundle](https://github.com/modelcontextprotocol/mcpb): one zip with all four binaries, a `manifest.json` and a small sh launcher that picks the binary for the OS/CPU. Claude Desktop installs it in one click; the MCP registry points at it. |
| `kartograf-darwin-arm64.mcpb` | The same bundle with the Apple Silicon binary only. Smithery takes at most 25 MB per bundle, and the full one is ~55 MB. |
| `server.json` | The MCP registry entry for this release: version, bundle URL and its SHA-256. |
| `SHA256SUMS` | Now also lists both bundles. |

Bundle details:

- MCPB can switch the command per OS but not per CPU, hence one bundle
  with every binary behind `packaging/mcpb/kartograf`.
- The user picks the project directory at install time
  (`user_config.project_dir`); the bundle runs
  `kartograf serve <project_dir>`.
- `KARTOGRAF_NO_UPDATE_CHECK=1` is set in the bundle: the host app
  updates bundles, so `self-update` hints would only mislead.
- The tool list in the bundle's `manifest.json` is read from the
  binary at build time, so it cannot drift from the server.
- No Windows build, so the manifest offers the bundle on macOS and
  Linux only. Bundles are not signed (`mcpb sign` needs a code-signing
  certificate).

Two jobs run after the release and stay off until you turn them on
(see below):

- `mcp-registry` publishes the release's `server.json` to
  registry.modelcontextprotocol.io with GitHub OIDC, no secret. It runs
  when the repository variable `MCP_REGISTRY_PUBLISH` is `true`.
- `smithery` uploads `kartograf-darwin-arm64.mcpb` to Smithery. It runs
  when the `SMITHERY_API_KEY` secret exists. A Smithery failure never
  fails the release.

Locally:

```sh
make mcpb                            # dist/kartograf.mcpb with this machine's binary only
make serverjson VERSION=v0.1.20      # point ./server.json at release v0.1.20 (downloads its bundle for the hash)
make serverjson VERSION=v0.1.20 MCPB=dist/kartograf.mcpb   # hash a local bundle instead
```

`server.json` in the repository is a template: version `0.0.0` and a
zero hash. CI fills both in per release; never publish the template
itself.

## One-time setup

### 1. MCP registry (registry.modelcontextprotocol.io)

The name `io.github.dev-manul/kartograf` belongs to the GitHub account
dev-manul, so the GitHub login is all the proof the registry needs.
The bundle's URL already contains "mcp" (`.mcpb`), which is what the
registry checks for MCPB packages. `server.json` is already in the
repository, so skip `mcp-publisher init`: it would overwrite it.

Wait for the first release that carries `kartograf.mcpb`, then either:

**Automatic (recommended).** Turn the CI job on; the next release
publishes, and so does every one after it:

```sh
gh variable set MCP_REGISTRY_PUBLISH --body true --repo dev-manul/kartograf
```

(or Settings → Secrets and variables → Actions → Variables.)

**By hand**, for the current release:

```sh
brew install mcp-publisher       # or the binary from github.com/modelcontextprotocol/registry/releases
mcp-publisher login github       # browser login as dev-manul
curl -fsSLO https://github.com/dev-manul/kartograf/releases/latest/download/server.json
mcp-publisher publish server.json
```

Check it:

```sh
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.dev-manul/kartograf"
```

A version can be published only once. If the CI job is on, do not also
publish by hand.

The registry also accepts Docker images (`registryType: oci`, e.g.
`ghcr.io/dev-manul/kartograf:0.1.20`, built with
`LABEL io.modelcontextprotocol.server.name="io.github.dev-manul/kartograf"`
and added as a second entry in `packages`). kartograf does not ship one:
it reads code on the user's machine, and a container would need that
code mounted in.

### 2. Smithery

```sh
npx @smithery/cli auth login
npx @smithery/cli mcp publish ./kartograf-darwin-arm64.mcpb -n dev-manul/kartograf
```

Download the bundle from the latest release first. If Smithery says the
namespace does not exist, create it with
`npx @smithery/cli namespace create dev-manul`. For later releases, add an
API key from your Smithery account settings as the repository secret
`SMITHERY_API_KEY` (tokens from `smithery auth token` expire within 24
hours, too soon for CI):

```sh
gh secret set SMITHERY_API_KEY --repo dev-manul/kartograf
```

### 3. Glama

`glama.json` at the repository root names dev-manul as maintainer.

1. Open <https://glama.ai/mcp/servers>, add the server by its GitHub URL
   (or find it if Glama has already indexed it).
2. Claim it (sign in with GitHub as dev-manul). The repository is under
   a personal account, so the GitHub login alone proves ownership;
   `glama.json` covers the case where it moves to an organization.
3. In the server's admin page, give Glama a Dockerfile so it can start
   the server and score it (awesome-mcp-servers wants that score):

   ```dockerfile
   FROM debian:bookworm-slim
   RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*
   RUN curl -fsSL -o /usr/local/bin/kartograf \
         https://github.com/dev-manul/kartograf/releases/latest/download/kartograf-linux-amd64 \
    && chmod +x /usr/local/bin/kartograf \
    && mkdir /workspace
   ENV KARTOGRAF_NO_UPDATE_CHECK=1
   ENTRYPOINT ["kartograf", "serve", "/workspace"]
   ```

   An empty `/workspace` is enough: the server starts and lists its
   tools.

Glama re-reads the repository itself; nothing to do per release.

### 4. awesome-mcp-servers

After Glama shows a score, open a PR against
[punkpeye/awesome-mcp-servers](https://github.com/punkpeye/awesome-mcp-servers)
that adds one line to **💻 Developer Tools** in `README.md`. Their CI
checks that the name is `owner/repo`, that a Glama score badge is
present and that only the legend's emojis are used (🏎️ Go, 🏠 local,
🍎 macOS, 🐧 Linux):

```markdown
- [dev-manul/kartograf](https://github.com/dev-manul/kartograf) [![dev-manul/kartograf MCP server](https://glama.ai/mcp/servers/dev-manul/kartograf/badges/score.svg)](https://glama.ai/mcp/servers/dev-manul/kartograf) 🏎️ 🏠 🍎 🐧 - Code map (symbols, references, call graph) for PHP, Go and TypeScript codebases: incremental tree-sitter + SQLite index, impact analysis and per-branch handoff notes.
```

If Glama gives the server a different path, use that path in both
badge URLs. PR title: `Add dev-manul/kartograf`.

### 5. mcp.so and cursor.directory

Both are web forms; sign in with GitHub.

- **mcp.so**: <https://mcp.so/submit>. Type: MCP Server; URL:
  `https://github.com/dev-manul/kartograf`; server config:

  ```json
  {"mcpServers": {"kartograf": {"command": "kartograf", "args": ["serve", "/path/to/project"]}}}
  ```

- **cursor.directory**: <https://cursor.directory/plugins/new>, paste
  the repository URL. The page detects MCP servers from a `.mcp.json`
  at the repository root. kartograf has none on purpose: Claude Code
  would also load it for everyone working in this repository. If the
  page finds no components, that is why.

## Every release

Nothing by hand. Each push to master builds the bundles and
`server.json`, attaches them to the release, publishes the registry
entry when `MCP_REGISTRY_PUBLISH` is `true` and uploads to Smithery when
`SMITHERY_API_KEY` is set. Glama, awesome-mcp-servers, mcp.so and
cursor.directory point at the repository and need no update.
