# kartograf — install instructions for AI agents

You are an AI coding agent with shell access. Install and register the
kartograf MCP server for the project in the current working directory
by following these steps.

1. Detect the OS and architecture (`uname -s`, `uname -m`) and pick the
   matching release asset name:
   - macOS + arm64 → `kartograf-darwin-arm64`
   - macOS + x86_64 → `kartograf-darwin-amd64`
   - Linux + x86_64 → `kartograf-linux-amd64`
   - Linux + aarch64 → `kartograf-linux-arm64`

2. Download it from the latest release and install:

   ```sh
   mkdir -p "$HOME/.local/bin"
   curl -fsSL -o "$HOME/.local/bin/kartograf" \
     "https://github.com/dev-manul/kartograf/releases/latest/download/<asset-name>"
   chmod +x "$HOME/.local/bin/kartograf"
   # macOS only: clear the quarantine attribute
   xattr -d com.apple.quarantine "$HOME/.local/bin/kartograf" 2>/dev/null || true
   ```

3. Verify it runs: `"$HOME/.local/bin/kartograf" --version`.

4. Build the index for the current project and report the summary line
   (file/symbol counts) to the user:

   ```sh
   "$HOME/.local/bin/kartograf" index .
   ```

5. Register the MCP server. In Claude Code (use the absolute binary
   path — `~` is not expanded when the server is spawned):

   ```sh
   claude mcp add kartograf -- "$HOME/.local/bin/kartograf" serve "$PWD"
   ```

   For other MCP clients (Cursor, Windsurf, ...), add a stdio server
   to their config. Use absolute paths, and note that Cursor requires
   the `type` field:

   ```json
   {"mcpServers": {"kartograf": {
     "type": "stdio",
     "command": "/home/user/.local/bin/kartograf",
     "args": ["serve", "/abs/path/to/project"]
   }}}
   ```

   If the server doesn't appear: reload the MCP list (Cursor:
   Settings → MCP), check the client's MCP logs — kartograf prints its
   version, project root, index location and enrich status to stderr
   on startup, and a clear reason when it fails before the handshake.
   Troubleshooting checklist: docs/cursor.md.

6. Tell the user to restart the session so the tools appear, and list
   what they will get: `search_symbols`, `get_symbol`,
   `find_references`, `get_callers`, `get_callees`, `class_hierarchy`,
   `file_outline`.

Notes:

- The index lives in the user cache dir, not in the repo; `serve`
  refreshes it automatically on start.
- Precision layer: `kartograf enrich go .` / `kartograf enrich php .`
  (see the README). For PHP projects run it — without it
  `get_callers` resolves almost nothing through interfaces/DI.
- Supported languages: PHP, Go, TypeScript/JavaScript.
- Updating later: `kartograf self-update` (the serve log also prints a
  hint when a newer release exists).
