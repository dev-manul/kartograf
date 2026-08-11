# AGENTS.md

Guidance for AI agents working in this repository.

## What this is

kartograf builds a code map (symbols, references, call graph) of a
project into SQLite and serves it to AI agents over MCP (stdio).
Tree-sitter parsing, language-agnostic core, PHP/Go/TS adapters, an
optional type-inference enrichment layer (go/types, PHPStan).

## Build & test

cgo is required (tree-sitter, mattn/go-sqlite3) and so is the
`sqlite_fts5` build tag — a compile guard
(`internal/core/store/fts5guard.go`) fails the build without it.
Always go through the Makefile:

```sh
make check      # vet + test + fmt + build — run before every commit
make install    # go install into $GOPATH/bin (MCP configs point there)
```

Plain `go build ./...` will fail with `BUILD_WITH_TAG_sqlite_fts5...` —
that is intentional, add `-tags sqlite_fts5` or use make.

## Layout

- `cmd/kartograf` — CLI (cobra): index, serve, enrich, outline,
  hidden parse-tree.
- `internal/core/model` — language-neutral types (`Symbol`, `Ref`,
  `Import`, `FileIndex`). Nothing language-specific belongs here.
- `internal/core/lang` — adapter contract + registry.
- `internal/core/indexer` — walk (gitignore-aware), worker pool,
  mtime/sha256 change detection, go.mod module map collection.
- `internal/core/store` — SQLite schema, bulk/incremental writers,
  FTS5, enrichment table.
- `internal/core/query` — read side used by MCP tools.
- `internal/lang/php`, `internal/lang/golang`, `internal/lang/ts` —
  tree-sitter adapters.
- `internal/enrich` — go/types pass and PHPStan rule scaffolding +
  JSONL import.
- `internal/mcpserver` — MCP tool definitions.

## Hard-won rules (do not relearn these the hard way)

- **Schema changes**: any change to the SQLite schema requires bumping
  `schemaVersion` in `internal/core/store/store.go`. A mismatch
  silently deletes and rebuilds the database — that is the designed
  migration strategy; never write ALTER migrations.
- **FQN dialects**: PHP `App\Ns\Class::method()`, Go
  `module/pkg.Type.Method()`, TS `path/module#Class.method()` (module
  = extensionless file path, trailing `/index` stripped). Symbol IDs
  prefix the language (`php:...`, `go:...`, `ts:...`). Query helpers
  (`methodSplit`, `memberSep`, suffix lookup) must stay
  separator-agnostic across all dialects.
- **tree-sitter node kinds**: never trust documentation or memory —
  dump the real CST with `kartograf parse-tree file.{php,go}` (hidden
  command) and verify. Grammar quirks live in the adapter tests'
  `testdata/` fixtures; extend those fixtures when touching extraction.
- **MCP output schemas**: Claude Code rejects tools whose
  `outputSchema` is not a JSON object — wrap list results in a struct
  (`{results: [...]}`), never return a bare slice as the Out type.
- **PHPStan enrichment**: the rule class is loaded via
  `--autoload-file` (bootstrapFiles is too late for DI container
  construction). Edges travel as pseudo-errors with identifier
  `kartograf.edge` through `--error-format=json` — do NOT switch to a
  side-channel output file: PHPStan's result cache skips unchanged
  files and would silently drop their edges.
- **Enrichment lifecycle**: `.kartograf/enrich.<source>.jsonl` at the
  project root is the source of truth; `ext_edges` are replaced
  wholesale per source on import, auto-imported by index/serve on
  mtime change, and dropped when the file is deleted.
- **TS specifics**: JSX component renders are `calls` edges with a
  `()` target so they join function-component FQNs; unqualified names
  resolve only through imports or file-local declarations (JS scoping
  — unknown names are globals and are skipped); imports through barrel
  files (`index.ts` re-exports) stay heuristic and do not join the
  graph — a known limitation.
- **Vendor code** is indexed shallow (`SkipRefs`): declarations and
  hierarchy only. Don't emit call edges from vendor files.
- **Bulk vs incremental writes**: an empty database takes
  `BeginBulkWrite` (indexes/triggers dropped, FTS rebuilt once at
  commit). Incremental runs rely on the FTS triggers; keep both paths
  working when touching the store.

## Testing against real projects

Index a real project of each language and eyeball the numbers (a large
PHP monolith with vendor: ~79k files, cold ~19s; typical Go/TS repos:
under 2s):

```sh
kartograf index /path/to/some/project
```

To smoke-test the MCP server without a client, pipe JSON-RPC to
`kartograf serve --no-index <root>` — and keep stdin open (append a
`sleep` in the pipe), otherwise the server exits on EOF before
replying.

## Conventions

- Comments and identifiers in English; README is bilingual
  (README.md en, README.ru.md ru) — update both.
- Commit messages: what changed and why, with measured numbers when
  the change is about performance or coverage.
- New language adapters implement `lang.Language`, register in
  `cmd/kartograf/main.go`, put grammar fixtures in `testdata/`, and
  must not leak language specifics into `internal/core`.
