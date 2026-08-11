package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/indexer"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/enrich"
)

func newIndexCmd() *cobra.Command {
	var (
		dbPath  string
		rebuild bool
		force   bool
		quiet   bool
	)
	cmd := &cobra.Command{
		Use:   "index [root]",
		Short: "Build or update the code index for a project",
		Long: `Walks the project, extracts symbols from changed files (content-hash
based incrementality) and stores them in SQLite. The database lives in
the user cache dir unless --db is given; project settings are read from
.kartograf.yml at the root.`,
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
			if rebuild {
				for _, suffix := range []string{"", "-wal", "-shm"} {
					os.Remove(dbPath + suffix)
				}
			}

			s, err := store.Open(dbPath, absRoot)
			if err != nil {
				return err
			}
			defer s.Close()

			logf := func(format string, args ...any) {
				if !quiet {
					fmt.Fprintf(os.Stderr, format+"\n", args...)
				}
			}
			stats, err := indexer.Run(indexer.Options{
				Root:  absRoot,
				Store: s,
				Cfg:   cfg,
				Force: force,
				Log:   logf,
			})
			if err != nil {
				return err
			}

			if err := enrich.AutoImport(s, absRoot, logf); err != nil {
				logf("enrich auto-import: %v", err)
			}

			total, err := s.Stats()
			if err != nil {
				return err
			}
			fmt.Printf("indexed %d files (%d unchanged, %d removed, %d parse errors) in %s\n",
				stats.Indexed, stats.Unchanged, stats.Removed, stats.ParseErr,
				stats.Duration.Round(time.Millisecond))
			fmt.Printf("index: %d files, %d symbols at %s\n", total.Files, total.Symbols, s.Path())
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the index database (default: user cache dir)")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "delete the database and index from scratch")
	cmd.Flags().BoolVar(&force, "force", false, "re-extract all files even if unchanged")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress output")
	return cmd
}
