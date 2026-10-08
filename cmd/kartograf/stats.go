package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/dev-manul/kartograf/internal/core/config"
	"github.com/dev-manul/kartograf/internal/core/store"
	"github.com/dev-manul/kartograf/internal/usage"
)

func newStatsCmd() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "stats [root]",
		Short: "Show local tool-call counts and a token estimate",
		Long: `Prints how many MCP tool calls this project has served, how big the
answers were, and an estimate of source bytes those answers stood in
for. The estimate assumes the agent would have opened each named file
instead. It is an upper bound. Set stats.dollars_per_million in
.kartograf.yml to also print a money figure. Nothing is uploaded.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			cfg, err := config.Load(abs)
			if err != nil {
				return err
			}
			if dbPath == "" {
				dbPath, err = store.DefaultPath(abs)
				if err != nil {
					return err
				}
			}
			path := filepath.Join(filepath.Dir(dbPath), "usage.db")
			if _, err := os.Stat(path); err != nil {
				fmt.Println("no tool calls recorded yet")
				return nil
			}
			rec, err := usage.Open(path)
			if err != nil {
				return err
			}
			defer rec.Close()
			rep, err := rec.Since(time.Time{})
			if err != nil {
				return err
			}
			if rep.Calls == 0 {
				fmt.Println("no tool calls recorded yet")
				return nil
			}
			fmt.Print(usage.Format(rep, cfg.Stats.DollarsPerMillion))
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the index database (usage.db sits beside it)")
	return cmd
}
