# kartograf in Cursor (and other stdio MCP clients)

Config example — project `.cursor/mcp.json` or global `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "kartograf": {
      "type": "stdio",
      "command": "/Users/you/.local/bin/kartograf",
      "args": ["serve", "/abs/path/to/project"]
    }
  }
}
```

## Troubleshooting checklist

1. **`"type": "stdio"` is present** — Cursor requires it; without it the
   server silently doesn't load.
2. **Absolute paths only** — `~` is not expanded in `command` or `args`.
3. **Check MCP logs** (Output → MCP Logs). kartograf logs to stderr on
   startup: version, project root, index db path, file/symbol counts,
   enrich status — and a one-line reason if it exits before the MCP
   handshake.
4. **Reload the MCP list** after config changes (Settings → MCP →
   reload); Cursor does not always pick changes up live.
5. **Global vs project config** — a server registered globally may show
   up under a prefixed name (e.g. `user-kartograf`).
6. **macOS quarantine** — a downloaded binary needs
   `xattr -d com.apple.quarantine /path/to/kartograf` (the install
   prompt does this). Symptom: process dies instantly with no output.
7. **First start on a big repo** takes seconds (index build); watch the
   stderr log. Subsequent starts reuse the index.
