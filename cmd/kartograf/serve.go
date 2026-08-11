package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/config"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/indexer"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/query"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/core/store"
	"gitlab.stripchat.dev/stripcash/kartograf/internal/mcpserver"
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
			s, err := store.Open(dbPath, absRoot)
			if err != nil {
				return err
			}
			defer s.Close()

			if !noIndex {
				fmt.Fprintln(os.Stderr, "kartograf: updating index...")
				stats, err := indexer.Run(indexer.Options{Root: absRoot, Store: s, Cfg: cfg})
				if err != nil {
					return fmt.Errorf("index update: %w", err)
				}
				fmt.Fprintf(os.Stderr, "kartograf: index ready (%d reindexed, %d unchanged, %s)\n",
					stats.Indexed, stats.Unchanged, stats.Duration.Round(10_000_000))
			}

			srv := mcpserver.New(query.New(s, absRoot), version)
			fmt.Fprintln(os.Stderr, "kartograf: serving MCP on stdio")
			return srv.Run(context.Background(), &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the index database (default: user cache dir)")
	cmd.Flags().BoolVar(&noIndex, "no-index", false, "serve the existing index without updating it")
	return cmd
}
