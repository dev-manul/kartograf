package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/indexer"
	"github.com/dev-manul/kartograf/internal/core/query"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/enrich"
	"github.com/dev-manul/kartograf/internal/mcpserver"
	"github.com/dev-manul/kartograf/internal/selfupdate"
)

func newServeCmd() *cobra.Command {
	var (
		dbPath  string
		noIndex bool
	)
	cmd := &cobra.Command{
		Use:   "serve [root]",
		Short: "Serve the code index over MCP (stdio)",
		Long: `Brings the index up to date (unless --no-index) and serves it to MCP
clients over stdio. Register in Claude Code / Cursor as a stdio server:

  {"mcpServers": {"kartograf": {"command": "kartograf", "args": ["serve", "/path/to/project"]}}}

All progress output goes to stderr; stdout carries the MCP protocol.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			absRoot, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			cfg, err := config.Load(absRoot)
			if err != nil {
				return err
			}
			if dbPath == "" {
				if dbPath, err = store.DefaultPath(absRoot); err != nil {
					return err
				}
			}
			logf := func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, "kartograf: "+format+"\n", args...)
			}
			logf("%s, project %s", version, absRoot)
			logf("index db %s", dbPath)

			s, err := store.Open(dbPath, absRoot)
			if err != nil {
				return fmt.Errorf("open index db %s: %w", dbPath, err)
			}
			defer s.Close()

			refresh := func() error {
				stats, err := indexer.Run(indexer.Options{Root: absRoot, Store: s, Cfg: cfg})
				if err != nil {
					return fmt.Errorf("index update: %w", err)
				}
				logf("index refresh: %d reindexed, %d unchanged, %d removed, %s",
					stats.Indexed, stats.Unchanged, stats.Removed,
					stats.Duration.Round(10_000_000))
				if err := enrich.AutoImport(s, absRoot, logf); err != nil {
					logf("enrich auto-import: %v", err)
				}
				return nil
			}

			// An index that already has data serves immediately and
			// refreshes in the background — agents get their first
			// tool response in milliseconds instead of waiting for a
			// full-tree scan. An empty index must be built first.
			if !noIndex {
				existing, err := s.Stats()
				if err != nil {
					return err
				}
				if existing.Files == 0 {
					logf("empty index — building before serving...")
					if err := refresh(); err != nil {
						return err
					}
				} else {
					go func() {
						if err := refresh(); err != nil {
							logf("background %v", err)
						}
					}()
				}
			}
			logServeStatus(s, absRoot, logf)

			// Fire-and-forget daily update check; the hint lands in
			// the MCP server's stderr log.
			go func() {
				if notice := selfupdate.Notice(version); notice != "" {
					fmt.Fprintln(os.Stderr, notice)
				}
			}()

			srv := mcpserver.New(query.New(s, absRoot), version)
			fmt.Fprintln(os.Stderr, "kartograf: serving MCP on stdio")
			return srv.Run(context.Background(), &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the index database (default: user cache dir)")
	cmd.Flags().BoolVar(&noIndex, "no-index", false, "serve the existing index without updating it")
	return cmd
}

// logServeStatus reports what the server is actually working with:
// index size, enrichment sources, and a warning when a PHP project
// runs without type-inference edges (callers would be heuristic-only).
func logServeStatus(s *store.Store, root string, logf func(format string, args ...any)) {
	if total, err := s.Stats(); err == nil {
		logf("index ready: %d files, %d symbols", total.Files, total.Symbols)
	}
	enrichStats, err := s.EnrichStats()
	if err != nil {
		return
	}
	if len(enrichStats) == 0 {
		logf("enrich: none")
	}
	for source, n := range enrichStats {
		logf("enrich %s: %d edges", source, n)
	}
	if langs, err := s.LangCounts(); err == nil {
		if langs["php"] > 0 && enrichStats["phpstan"] == 0 {
			logf("warning: PHP project without PHPStan enrichment — get_callers/get_callees "+
				"resolve heuristically only; run `kartograf enrich php %s` for exact edges", root)
		}
	}
}
